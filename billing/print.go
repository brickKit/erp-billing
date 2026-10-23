package billing

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/money"
	"github.com/cockroachdb/apd/v3"
)

// Printing an issued invoice through infra/print (docs/design.md, "打印"): the first render records the template
// version and the view model (the print snapshot); every later render passes the same two and is marked as a copy, so
// a changed template or changed formatting code never changes an old invoice. A failed first render records nothing.

func (m *Module) printInvoice(c *httpx.Context) error {
	cur, err := m.invoice(c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := check(c, OpPrint, cur.LegalEntityID)
	if err != nil {
		return err
	}
	if cur.State != "issued" {
		return errState.New("only an issued invoice prints; this one is " + cur.State)
	}
	if cur.PrintData == nil {
		first, err := m.firstPrint(ctx, cur, actor)
		if err != nil || first != nil {
			if err != nil {
				return err
			}
			return c.Bytes(200, first.mediaType, first.content)
		}
		cur = m.fresh(ctx, cur) // another request recorded the snapshot first: this one is a copy
	}
	res, err := m.render(ctx, *cur.Print.TemplateVersion, cur.PrintData, true)
	if err != nil {
		return err
	}
	if err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE invoice SET print_count = print_count + 1, version = version + 1, updated_at = now()
			WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		_, err := m.emitInvoice(ctx, tx, cur.ID, actor, OpPrint)
		return err
	}); err != nil {
		return err
	}
	return c.Bytes(200, res.GetMediaType(), res.GetContent())
}

type rendered struct {
	mediaType string
	content   []byte
}

// firstPrint formats the view model, renders the template's current version and records both as the snapshot; nil
// when another request recorded a snapshot meanwhile.
func (m *Module) firstPrint(ctx context.Context, v Invoice, actor string) (*rendered, error) {
	le, err := m.legalEntityOf(ctx, v.LegalEntityID, false)
	if err != nil {
		return nil, err
	}
	scale, err := m.currencyScale(ctx, v.Currency)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(viewModel(v, le, scale))
	if err != nil {
		return nil, err
	}
	res, err := m.render(ctx, 0, data, false)
	if err != nil {
		return nil, err
	}
	recorded := false
	err = m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE invoice SET print_template = $2, print_version = $3, print_data = $4,
			print_digest = $5, print_count = 1, version = version + 1, updated_at = now()
			WHERE id = $1 AND print_data IS NULL`, v.ID, res.GetTemplate(), res.GetVersion(), string(data),
			res.GetVersionDigest())
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		recorded = true
		_, err = m.emitInvoice(ctx, tx, v.ID, actor, OpPrint)
		return err
	})
	if err != nil || !recorded {
		return nil, err
	}
	return &rendered{mediaType: res.GetMediaType(), content: res.GetContent()}, nil
}

// viewModel is the invoice template's data, formatted once for Chinese (dates, grouped amounts, rates as percents,
// the gross amount in capital characters); the template only places it.
func viewModel(v Invoice, le legalEntity, scale int32) map[string]any {
	lines := make([]map[string]any, len(v.Lines))
	for i, l := range v.Lines {
		lines[i] = map[string]any{"no": l.LineNo, "code": l.Product.Code, "name": l.Product.Name,
			"quantity": l.Quantity, "unit": l.Unit, "unit_price": grouped(l.UnitPrice, max(scale, places(l.UnitPrice))),
			"net": grouped(l.Net, scale), "tax_rate": percent(l.TaxRate), "tax": grouped(l.Tax, scale)}
	}
	date := *v.InvoiceDate
	taxID := ""
	if v.Customer.TaxID != nil {
		taxID = *v.Customer.TaxID
	}
	return map[string]any{
		"invoice": map[string]any{"number": *v.Number, "date": date[:4] + "年" + date[5:7] + "月" + date[8:10] + "日",
			"currency": v.Currency},
		"seller": map[string]any{"name": le.Name, "code": le.Code},
		"buyer":  map[string]any{"name": v.Customer.Name, "tax_id": taxID},
		"lines":  lines,
		"totals": map[string]any{"net": grouped(v.Totals.Net, scale), "tax": grouped(v.Totals.Tax, scale),
			"gross": grouped(v.Totals.Gross, scale), "gross_in_words": inWords(v.Totals.Gross, v.Currency)},
	}
}

func places(s string) int32 {
	if _, frac, ok := strings.Cut(s, "."); ok {
		return int32(len(frac))
	}
	return 0
}

// grouped writes a decimal with scale places and thousands separators: 1234.5 → 1,234.50.
func grouped(s string, scale int32) string {
	d, _, _ := apd.NewFromString(s)
	t := money.Format(d, scale)
	whole, frac, _ := strings.Cut(t, ".")
	var b strings.Builder
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if frac != "" {
		b.WriteString("." + frac)
	}
	return b.String()
}

// percent writes a rate as a percent: 0.13 → 13%.
func percent(rate string) string {
	d, _, _ := apd.NewFromString(rate)
	var p apd.Decimal
	_, _ = arith.Mul(&p, d, apd.New(100, 0))
	p.Reduce(&p)
	return p.Text('f') + "%"
}

// inWords writes an amount (at most two places) in capital characters, as invoices in China show the total:
// 430.2 → 人民币肆佰叁拾元贰角整.
func inWords(amount, currency string) string {
	digits := []rune("零壹贰叁肆伍陆柒捌玖")
	small := []string{"", "拾", "佰", "仟"}
	big := []string{"", "万", "亿", "兆"}
	d, _, _ := apd.NewFromString(amount)
	whole, frac, _ := strings.Cut(money.Format(d, 2), ".")
	whole = strings.TrimLeft(whole, "0")
	var b strings.Builder
	if currency == "CNY" {
		b.WriteString("人民币")
	} else {
		b.WriteString(currency)
	}
	zero := false
	for i := 0; i < len(whole); i++ {
		pos := len(whole) - 1 - i
		if n := int(whole[i] - '0'); n == 0 {
			zero = true
		} else {
			if zero {
				b.WriteRune('零')
				zero = false
			}
			b.WriteRune(digits[n])
			b.WriteString(small[pos%4])
		}
		if pos%4 == 0 && pos > 0 && strings.Trim(whole[max(0, i-3):i+1], "0") != "" {
			b.WriteString(big[pos/4])
		}
	}
	if whole != "" {
		b.WriteString("元")
	}
	jiao, fen := int(frac[0]-'0'), int(frac[1]-'0')
	switch {
	case jiao == 0 && fen == 0:
		if whole == "" {
			b.WriteString("零元")
		}
		b.WriteString("整")
	default:
		if jiao > 0 {
			b.WriteRune(digits[jiao])
			b.WriteString("角")
		} else if whole != "" {
			b.WriteRune('零')
		}
		if fen > 0 {
			b.WriteRune(digits[fen])
			b.WriteString("分")
		} else {
			b.WriteString("整")
		}
	}
	return b.String()
}
