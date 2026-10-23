// Command seed fills a running erp/billing with the seed data for people (be-assembly-standard's seed and test data
// conventions):
//
//   - access, by the authorization admin: the role maintain (the seed user) and read in a legal entity (CN01:
//     test.sales.cn and test.sales.multi; ES01: test.sales.es);
//   - invoices from erp/sales's orders that have something left to bill, all the remaining at once, in order number
//     order: one in five stays a draft, one in five is drafted and voided (it takes no number), the others are issued
//     (numbered, the invoiced fact published, formalized at erp/sales: a delivered and billed order completes);
//   - the first three issued invoices of each legal entity printed (the print snapshot), the first one twice (a copy).
//
// Invoices are seeded once, into a legal entity that has no invoice yet; a run that broke off is finished by running it
// again within a day (the idempotency keys answer the requests already made). The orders come from erp/sales (its
// seed runs first).
//
//	SEED_IAM_URL (http://localhost:18200)    the identity member's development exchange
//	SEED_AUTHZ_URL (http://localhost:18223)  the authorization member's effect point
//	SEED_ADMIN (test.alice)                  the authorization admin (the install claim)
//	SEED_USER (test.bob), SEED_PASSWORD      the user that writes the data; the test accounts' password
//	ERP_BILLING_URL (http://localhost:18089), ERP_SALES_URL (http://localhost:18084), MDM_ORG_URL (http://localhost:18083)
package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/seed"
)

const prefix = "「本地测试」"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed erp/billing: "+format+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

type client struct{ seed.Client }

func (c client) must(method, path string, body any, header ...string) map[string]any {
	out, err := c.Must(method, path, body, header...)
	check(err)
	return out
}

func segment(op string, legalEntities ...string) sdkauthz.Segment {
	s := seed.Segment("erp.billing.invoice", op)
	le := sdkauthz.Atom{Kind: "all"}
	if len(legalEntities) > 0 {
		le = sdkauthz.Atom{Kind: "in", Values: legalEntities}
	}
	s.Atoms = map[string]sdkauthz.Atom{"legal_entity": le}
	return s
}

func main() {
	base := env("ERP_BILLING_URL", "http://localhost:18089")
	if !seed.Ready(base) {
		fail("erp/billing at %s is not ready", base)
	}
	pw := os.Getenv("SEED_PASSWORD")
	if pw == "" {
		fail("SEED_PASSWORD is not set (the test accounts' password in Casdoor)")
	}
	iam, user, admin := env("SEED_IAM_URL", "http://localhost:18200"), env("SEED_USER", "test.bob"), env("SEED_ADMIN", "test.alice")
	accounts, err := seed.Accounts(iam, pw, admin, user, "test.sales.cn", "test.sales.es", "test.sales.multi")
	check(err)
	t := accounts[user].Token
	les := map[string]string{}
	org := client{seed.NewClient(env("MDM_ORG_URL", "http://localhost:18083"), t)}
	for _, e := range org.must("GET", "/api/org/legal-entities?limit=200", nil)["legal_entities"].([]any) {
		e := e.(map[string]any)
		les[e["code"].(string)] = e["id"].(string)
	}
	for _, code := range []string{"CN01", "ES01", "US01"} {
		if les[code] == "" {
			fail("mdm/org has no legal entity %s: run its seed first", code)
		}
	}
	a, err := seed.NewAuthz(env("SEED_AUTHZ_URL", "http://localhost:18223"), accounts[admin].Token, "erp/billing")
	check(err)
	_, err = a.GrantRoles([]seed.RoleSpec{
		{Key: "maintain", Name: prefix + "出账维护", To: []string{user}, Segments: []sdkauthz.Segment{
			segment("erp.billing.invoice.read"), segment("erp.billing.invoice.draft"), segment("erp.billing.invoice.issue"),
			segment("erp.billing.invoice.print")}},
		{Key: "read-cn01", Name: prefix + "账单只读（CN01）", To: []string{"test.sales.cn", "test.sales.multi"},
			Segments: []sdkauthz.Segment{segment("erp.billing.invoice.read", les["CN01"])}},
		{Key: "read-es01", Name: prefix + "账单只读（ES01）", To: []string{"test.sales.es"},
			Segments: []sdkauthz.Segment{segment("erp.billing.invoice.read", les["ES01"])}},
	}, accounts)
	check(err)

	c := client{seed.NewClient(base, t)}
	sales := client{seed.NewClient(env("ERP_SALES_URL", "http://localhost:18084"), t)}
	for _, le := range []string{"CN01", "ES01", "US01"} {
		st, out, err := c.UntilGranted(30*time.Second, "GET", "/api/billing/invoices?limit=1&legal_entity_id="+les[le], nil)
		check(err)
		if st != 200 {
			fail("listing invoices: %d %v", st, out)
		}
		if len(out["invoices"].([]any)) > 0 {
			fmt.Printf("seed erp/billing: %s has invoices already; they are not seeded again\n", le)
			continue
		}
		var orders []map[string]any
		cursor := ""
		for {
			page := sales.must("GET", "/api/sales/orders?to_bill=true&limit=200&legal_entity_id="+les[le]+"&cursor="+url.QueryEscape(cursor), nil)
			for _, o := range page["orders"].([]any) {
				orders = append(orders, o.(map[string]any))
			}
			if cursor, _ = page["next_cursor"].(string); cursor == "" {
				break
			}
		}
		sort.Slice(orders, func(i, j int) bool { return orders[i]["number"].(string) < orders[j]["number"].(string) })
		issued, printed := 0, 0
		for n, o := range orders {
			k := func(kind string) string { return fmt.Sprintf("seed:erp/billing:%s:%s:%d", le, kind, n) }
			st, v, err := c.Do("POST", "/api/billing/invoices", map[string]any{"order_id": o["id"]}, "Idempotency-Key", k("draft"))
			check(err)
			if st != 200 {
				fmt.Printf("seed erp/billing: order %v not drafted: %d %v\n", o["number"], st, v["reason"])
				continue
			}
			id := v["id"].(string)
			switch n % 5 {
			case 0: // stays a draft
				continue
			case 1:
				c.must("POST", "/api/billing/invoices/"+id+"/void", nil, "Idempotency-Key", k("void"))
				continue
			}
			c.must("POST", "/api/billing/invoices/"+id+"/issue", nil, "Idempotency-Key", k("issue"))
			issued++
			if printed < 3 {
				times := 1
				if printed == 0 {
					times = 2 // the second print is a copy
				}
				for i := 0; i < times; i++ {
					st, _, err := c.Do("POST", "/api/billing/invoices/"+id+"/print", nil)
					check(err)
					if st != 200 {
						fail("printing invoice %s: %d", id, st)
					}
				}
				printed++
			}
		}
		fmt.Printf("seed erp/billing: %s: %d orders to bill, %d invoices issued\n", le, len(orders), issued)
	}
}
