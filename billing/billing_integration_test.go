//go:build integration

package billing

// The billing component against PostgreSQL and the bus over its real surfaces (L2). mdm/org, mdm/currency, erp/sales
// and infra/print are stand-ins that speak their published contracts over mTLS (standins_integration_test.go). Needs
// BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL, as be-sdk-go's integration tests. The member listens on 28089 (user)
// and 9089 (system); the stand-ins on 19108-19111.
// Run: go test -tags integration ./billing/
//
// What it proves (protocol 05's BL, the billing side): BL01 (two drafts of one order, one wins), BL02, BL04, BL05,
// BL21 (numbered only after the claim is confirmed), BL24, BL25 (a void invoice takes no number), BL26; numbers
// continuous per legal entity and year; the issue that stopped after confirming is numbered by its work item; the
// print snapshot; the legal entity dimension.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/bizdate"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	billingv3 "github.com/brickKit/erp-billing/contract/v3/gen/go/be/erp/billing/v3"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	printv3 "github.com/brickKit/infra-print/contract/v3/gen/go/be/infra/print/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	itPort     = "28089"
	itBase     = "http://localhost:" + itPort
	itSystem   = "localhost:9089"
	itOrg      = "localhost:19108"
	itCurrency = "localhost:19109"
	itSales    = "localhost:19110"
	itPrint    = "localhost:19111"
	itSchema   = "erp_billing_it"
)

type fixture struct {
	t     *testing.T
	ctx   context.Context
	w     *sectest.World
	admin *pgx.Conn
	alice client
	org   *orgStandIn
	sales *salesStandIn
	print *printStandIn
	le    string
	year  string
	cust  string
	// the first issued invoice, for the print and list steps
	first map[string]any
}

func TestBillingEndToEnd(t *testing.T) {
	fx := start(t)
	fx.draftAndIssue()
	fx.concurrentDrafts()
	fx.voidAndExpiry()
	fx.recovery()
	fx.printing()
	fx.listsAndScope()
	fx.outbox()
}

func start(t *testing.T) *fixture {
	adminURL, natsURL := os.Getenv("BE_SDK_TEST_PG_ADMIN"), os.Getenv("BE_SDK_TEST_NATS_URL")
	if adminURL == "" || natsURL == "" {
		t.Fatal("BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL are needed")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close(ctx) })
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+itSchema+" CASCADE; CREATE SCHEMA "+itSchema)
	require.NoError(t, err)
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	w := sectest.New(t)
	w.GrantAll(sectest.Alice, Definitions())

	fx := &fixture{t: t, ctx: ctx, w: w, admin: admin, le: ids.New().String(), cust: ids.New().String()}
	fx.org = &orgStandIn{les: map[string]map[string]any{fx.le: {"id": fx.le, "code": "SH", "name": "上海公司",
		"state": "active", "timezone": "Asia/Shanghai"}}}
	fx.sales = &salesStandIn{orders: map[string]map[string]any{}, ledger: map[string]*ledgerLine{}, lineOf: map[string]string{},
		claims: map[string]*claimIT{}, mode: map[string]string{}, expired: map[string]bool{}, calls: map[string]int{}}
	fx.print = &printStandIn{current: 3, data: map[int64][]byte{}}
	portFree(t, "localhost:"+itPort, itSystem, itOrg, itCurrency, itSales, itPrint)
	serve(t, itOrg, w, Org, func(s *grpc.Server) { orgv3.RegisterSourceServiceServer(s, fx.org) })
	serve(t, itCurrency, w, Currency, func(s *grpc.Server) { currencyv3.RegisterSourceServiceServer(s, currencyStandIn{}) })
	serve(t, itSales, w, Sales, func(s *grpc.Server) {
		salesv3.RegisterOrderServiceServer(s, fx.sales)
		salesv3.RegisterBillingSourceServiceServer(s, fx.sales)
	})
	serve(t, itPrint, w, Print, func(s *grpc.Server) { printv3.RegisterRenderServiceServer(s, fx.print) })

	env := w.Env(t, ID)
	for k, v := range map[string]string{"COMPONENT_ID": ID, "COMPONENT_VERSION": Version, "PORT": itPort,
		"STOP_DRAIN_DELAY": "50ms", "PG_HOST": u.Hostname(), "PG_PORT": u.Port(), "PG_DATABASE": strings.TrimPrefix(u.Path, "/"),
		"PG_USER": u.User.Username(), "PG_PASSWORD": pw, "PG_SCHEMA": itSchema, "PG_OWNER_USER": u.User.Username(),
		"PG_OWNER_PASSWORD": pw, "BUS_URL": natsURL, "IMPLEMENTATION_INSTANCE": ids.New().String(),
		"MDM_ORG_GRPC_ENDPOINT": "http://" + itOrg, "MDM_CURRENCY_GRPC_ENDPOINT": "http://" + itCurrency,
		"ERP_SALES_GRPC_ENDPOINT": "http://" + itSales, "INFRA_PRINT_GRPC_ENDPOINT": "http://" + itPrint} {
		env[k] = v
	}
	var logs bytes.Buffer
	var logMu sync.Mutex
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(logs.String())
			logMu.Unlock()
		}
	})
	out := &lockedWriter{w: &logs, mu: &logMu}
	opts := launcher.Options{Stdout: out, Stderr: out,
		Env: config.Env{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, Read: os.ReadFile}}
	migrate := opts
	migrate.Args = []string{"migrate"}
	require.Equal(t, launcher.ExitOK, launcher.Run(ctx, Definition(), migrate))
	sctx, stop := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- launcher.Run(sctx, Definition(), opts) }()
	t.Cleanup(func() { stop(); <-done })
	require.Eventually(t, func() bool {
		r, err := http.Get(itBase + "/readyz")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	}, 20*time.Second, 100*time.Millisecond)
	fx.alice = client{t, w.Bearer(sectest.Alice)}
	today, err := bizdate.Today("Asia/Shanghai")
	require.NoError(t, err)
	fx.year = today[:4]
	return fx
}

// order adds an order at the sales stand-in: a goods line (2 at 120, 13%) and a service line (1.5 for 150, 6%).
func (fx *fixture) order() (string, string, string) {
	o, goods, service := ids.New().String(), ids.New().String(), ids.New().String()
	fx.sales.addOrder(o, fx.le, fx.cust, [][6]string{{goods, ids.New().String(), "120", "0.13", "2", "240"},
		{service, ids.New().String(), "100", "0.06", "1.5", "150"}})
	return o, goods, service
}

func (fx *fixture) draft(order string, key ...string) (int, map[string]any) {
	return fx.alice.do("POST", "/api/billing/invoices", map[string]any{"order_id": order}, key...)
}

func (fx *fixture) issue(id string) (int, map[string]any) {
	return fx.alice.do("POST", "/api/billing/invoices/"+id+"/issue", nil)
}

func (fx *fixture) get(id string) map[string]any {
	fx.t.Helper()
	code, v := fx.alice.do("GET", "/api/billing/invoices/"+id, nil)
	require.Equal(fx.t, 200, code, v)
	return v
}

// draftAndIssue: all the remaining claimed under the invoice's ID (BL02 on retry), a lowered draft, the issue that
// confirms, numbers and formalizes with the lowered quantities.
func (fx *fixture) draftAndIssue() {
	t := fx.t
	o, goods, _ := fx.order()
	key := ids.New().String()
	code, v := fx.draft(o, key)
	require.Equal(t, 200, code, v)
	assert.Equal(t, "draft", v["state"])
	assert.Nil(t, v["number"])
	assert.Equal(t, map[string]any{"net": "390", "tax": "40.2", "gross": "430.2"}, v["totals"])
	code, again := fx.draft(o, key)
	require.Equal(t, 200, code, again)
	assert.Equal(t, v["id"], again["id"], "BL02: the same key drafts the same invoice")
	assert.Equal(t, 1, fx.sales.count("claim"), "the claim is taken once")

	code, low := fx.alice.do("PATCH", "/api/billing/invoices/"+v["id"].(string), map[string]any{"lines": []any{
		map[string]any{"order_line_id": goods, "quantity": "1", "amount": "120"}}})
	require.Equal(t, 200, code, low)
	assert.Equal(t, "15.6", at(low, "lines", 0, "tax"))
	assert.Equal(t, "24.6", at(low, "totals", "tax"))
	code, high := fx.alice.do("PATCH", "/api/billing/invoices/"+v["id"].(string), map[string]any{"lines": []any{
		map[string]any{"order_line_id": goods, "quantity": "3", "amount": "360"}}})
	assert.Equal(t, 409, code)
	assert.Equal(t, "ABOVE_CLAIM", high["reason"])

	fx.sales.set("confirm", "lost") // BL24: the confirm's answer is lost once; the same ID asks again
	code, issued := fx.issue(v["id"].(string))
	require.Equal(t, 200, code, issued)
	assert.Equal(t, "issued", issued["state"])
	assert.Regexp(t, regexp.MustCompile(`^INV-SH-`+fx.year+`-000001$`), issued["number"])
	assert.NotNil(t, issued["invoice_date"])
	assert.Equal(t, "done", issued["formalization"])
	c := fx.sales.claim(v["id"].(string))
	assert.Equal(t, "formalized", c.state)
	assert.Equal(t, "1", c.billed[0].GetMeasure().GetQuantity(), "the source records what was billed, not the claim")
	code, repeat := fx.issue(v["id"].(string))
	require.Equal(t, 200, code)
	assert.Equal(t, issued["number"], repeat["number"])
	fx.first = issued
}

// concurrentDrafts: BL01, two people draft the same order at once; one claim wins, the other draft is never made.
func (fx *fixture) concurrentDrafts() {
	o, _, _ := fx.order()
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i], _ = fx.draft(o) }()
	}
	wg.Wait()
	assert.ElementsMatch(fx.t, []int{200, 409}, codes)
	code, nothing := fx.draft(o)
	assert.Equal(fx.t, 409, code)
	assert.Equal(fx.t, "NOT_BILLABLE", nothing["reason"])
}

// voidAndExpiry: a voided draft releases its claim and takes no number (BL25); an expired claim voids the draft
// at issue (BL05, BL26); the next number follows on without a gap.
func (fx *fixture) voidAndExpiry() {
	t := fx.t
	o, _, _ := fx.order()
	_, v := fx.draft(o)
	code, void := fx.alice.do("POST", "/api/billing/invoices/"+v["id"].(string)+"/void", nil)
	require.Equal(t, 200, code, void)
	assert.Equal(t, "void", void["state"])
	assert.Equal(t, "released", fx.sales.claim(v["id"].(string)).state)
	code, refused := fx.issue(v["id"].(string))
	assert.Equal(t, 409, code)
	assert.Equal(t, "STATE_CONFLICT", refused["reason"])

	o2, _, _ := fx.order()
	_, v2 := fx.draft(o2)
	fx.sales.expire(v2["id"].(string))
	code, expired := fx.issue(v2["id"].(string))
	assert.Equal(t, 409, code)
	assert.Equal(t, "CLAIM_EXPIRED", expired["reason"])
	assert.Equal(t, "void", fx.get(v2["id"].(string))["state"])
	assert.Nil(t, fx.get(v2["id"].(string))["number"])

	o3, _, _ := fx.order()
	_, v3 := fx.draft(o3)
	code, issued := fx.issue(v3["id"].(string))
	require.Equal(t, 200, code, issued)
	assert.Regexp(t, regexp.MustCompile(`-000002$`), issued["number"], "void drafts take no number")
}

// recovery: BL04, the source does not take the formalize and the work item sends it again; an issue that stopped
// after confirming (the legal entity was suspended) is numbered by its work item once the legal entity is back.
func (fx *fixture) recovery() {
	t := fx.t
	o, _, _ := fx.order()
	_, v := fx.draft(o)
	fx.sales.set("formalize", "down")
	code, issued := fx.issue(v["id"].(string))
	require.Equal(t, 200, code, issued)
	assert.Equal(t, "issued", issued["state"])
	assert.Equal(t, "pending", issued["formalization"])
	fx.sales.set("formalize", "")
	require.Eventually(t, func() bool { return fx.get(v["id"].(string))["formalization"] == "done" }, 60*time.Second,
		300*time.Millisecond)

	o2, _, _ := fx.order()
	_, v2 := fx.draft(o2)
	fx.org.set(fx.le, "suspended")
	code, stopped := fx.issue(v2["id"].(string))
	assert.Equal(t, 409, code)
	assert.Equal(t, "LEGAL_ENTITY_NOT_USABLE", stopped["reason"])
	assert.Equal(t, "confirmed", fx.get(v2["id"].(string))["state"], "BL21: confirmed, never expires, not numbered")
	fx.org.set(fx.le, "active")
	_, err := fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".invoice SET updated_at = now() - interval '2 minutes' WHERE id = $1",
		v2["id"])
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		v := fx.get(v2["id"].(string))
		return v["state"] == "issued" && v["formalization"] == "done"
	}, 60*time.Second, 300*time.Millisecond, "the continue item numbers it")
}

// printing: the first render records the template version and view model; a later one uses them, marked as a copy,
// even after the template changed.
func (fx *fixture) printing() {
	t := fx.t
	id := fx.first["id"].(string)
	o, _, _ := fx.order()
	_, draft := fx.draft(o)
	code, _ := fx.alice.do("POST", "/api/billing/invoices/"+draft["id"].(string)+"/print", nil)
	assert.Equal(t, 409, code, "a draft does not print")
	fx.print.mu.Lock()
	fx.print.down = true
	fx.print.mu.Unlock()
	code, _ = fx.alice.do("POST", "/api/billing/invoices/"+id+"/print", nil)
	assert.Equal(t, 503, code)
	assert.EqualValues(t, 0, at(fx.get(id), "print", "count"), "a failed first render records nothing")
	fx.print.mu.Lock()
	fx.print.down = false
	fx.print.mu.Unlock()

	first := fx.raw("POST", "/api/billing/invoices/"+id+"/print")
	assert.Contains(t, first, "invoice v3 copy=false")
	v := fx.get(id)
	assert.EqualValues(t, 3, at(v, "print", "template_version"))
	assert.EqualValues(t, 1, at(v, "print", "count"))
	var model map[string]any
	fx.print.mu.Lock()
	require.NoError(t, json.Unmarshal(fx.print.data[fx.print.n], &model))
	fx.print.current = 4 // the template changes
	fx.print.mu.Unlock()
	assert.Equal(t, "人民币贰佰玖拾肆元陆角整", at(model, "totals", "gross_in_words"), "120 + 15.6 + 150 + 9")
	assert.Equal(t, "13%", at(model, "lines", 0, "tax_rate"))
	second := fx.raw("POST", "/api/billing/invoices/"+id+"/print")
	assert.Contains(t, second, "invoice v3 copy=true", "the snapshot's version, marked as a copy")
	assert.Equal(t, first[strings.LastIndex(first, " "):], second[strings.LastIndex(second, " "):], "the same data")
	assert.EqualValues(t, 2, at(fx.get(id), "print", "count"))
}

// listsAndScope: the keyword finds an invoice by number, the date filters, the system read, and a reader of
// another legal entity sees nothing.
func (fx *fixture) listsAndScope() {
	t := fx.t
	number := fx.first["number"].(string)
	code, page := fx.alice.do("GET", "/api/billing/invoices?q="+url.QueryEscape(number), nil)
	require.Equal(t, 200, code, page)
	require.Len(t, page["invoices"], 1)
	assert.Equal(t, fx.first["id"], at(page, "invoices", 0, "id"))
	_, page = fx.alice.do("GET", "/api/billing/invoices?state=issued&from="+fx.first["invoice_date"].(string), nil)
	assert.Len(t, page["invoices"], 4)

	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(t, Sales).ClientConfig(ID))))
	require.NoError(t, err)
	defer conn.Close()
	read, err := billingv3.NewInvoiceServiceClient(conn).Read(fx.ctx, &billingv3.ReadRequest{Ids: []string{fx.first["id"].(string)}})
	require.NoError(t, err)
	assert.Contains(t, string(read.GetResults()[0].GetData()), number)

	other := ids.New().String()
	fx.w.Grant(sectest.Bob, sdkauthz.Segment{ResourceType: Invoices, Operation: OpRead,
		ReadFields: []string{"id", "number", "legal_entity_id", "customer", "currency", "exchange_rate", "source", "state",
			"formalization", "invoice_date", "lines", "totals", "print", "version", "created_at", "updated_at"},
		Atoms: map[string]sdkauthz.Atom{DimLegalEntity: {Kind: "in", Values: []string{other}}},
		Valid: sdkauthz.Window{From: "2026-01-01T00:00:00Z"}})
	bob := client{t, fx.w.Bearer(sectest.Bob)}
	// without the grant the read is refused (403); with it, an invoice of another legal entity is not visible (404)
	require.Eventually(t, func() bool {
		code, _ := bob.do("GET", "/api/billing/invoices/"+fx.first["id"].(string), nil)
		return code == 404
	}, 30*time.Second, 200*time.Millisecond)
	_, page = bob.do("GET", "/api/billing/invoices", nil)
	assert.Empty(t, page["invoices"])
}

// outbox: every event fits its schema; every issued invoice has exactly one "invoiced" fact.
func (fx *fixture) outbox() {
	t := fx.t
	rows, err := fx.admin.Query(fx.ctx, "SELECT type, payload FROM "+itSchema+".be_outbox ORDER BY ord")
	require.NoError(t, err)
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var typ string
		var payload []byte
		require.NoError(t, rows.Scan(&typ, &payload))
		counts[typ]++
		s, err := jsonschema.NewCompiler().Compile("../contract/events/" + typ + ".schema.json")
		require.NoError(t, err, typ)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
		require.NoError(t, err)
		assert.NoError(t, s.Validate(doc), "%s: %s", typ, payload)
	}
	var issued int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".invoice WHERE state = 'issued'").Scan(&issued))
	assert.Equal(t, issued, counts[InvoiceIssued])
	assert.NotZero(t, counts[InvoiceChanged])
}

// --- helpers ---

type client struct {
	t     *testing.T
	token string
}

// do calls the user surface; a POST carries a new Idempotency-Key unless key gives one.
func (c client) do(method, path string, body any, key ...string) (int, map[string]any) {
	c.t.Helper()
	resp := c.send(method, path, body, key...)
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (c client) send(method, path string, body any, key ...string) *http.Response {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, itBase+path, r)
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	if method == "POST" {
		k := ids.New().String()
		if len(key) > 0 {
			k = key[0]
		}
		req.Header.Set("Idempotency-Key", k)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	return resp
}

// raw answers a byte response's body, requiring 200 and a PDF.
func (fx *fixture) raw(method, path string) string {
	fx.t.Helper()
	resp := fx.alice.send(method, path, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(fx.t, 200, resp.StatusCode, string(b))
	assert.Equal(fx.t, "application/pdf", resp.Header.Get("Content-Type"))
	return string(b)
}

func serve(t *testing.T, addr string, w *sectest.World, as string, register func(*grpc.Server)) {
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(w.TLS(t, as).ServerConfig())))
	register(srv)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
}

// at reads a value along a path of keys and indexes from a decoded JSON object.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// portFree fails at once when a port the member needs is taken.
func portFree(t *testing.T, addrs ...string) {
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		require.NoError(t, err, fmt.Sprintf("port %s is taken: is a cell or another member running?", a))
		_ = ln.Close()
	}
}
