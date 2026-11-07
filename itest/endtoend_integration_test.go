//go:build integration

package itest

// The billing component against PostgreSQL and the bus over its real surfaces (L2). mdm/org, mdm/currency, erp/sales
// and infra/print are stand-ins that speak their published contracts over mTLS (standins_integration_test.go). Needs
// BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL, as be-sdk-go's integration tests. The member listens on 28089 (user)
// and 9089 (system); the stand-ins on 19108-19111.
// Run: go test -tags integration ./itest/ (make test-integration); the index of the steps is itest/AGENTS.md.
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
	"strings"
	"sync"
	"testing"
	"time"

	billing "github.com/brickKit/erp-billing/v3/billing"

	core "github.com/brickKit/erp-billing/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/bizdate"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	printv3 "github.com/brickKit/infra-print/contract/v3/gen/go/be/infra/print/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	"github.com/jackc/pgx/v5"
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
	fx.recoveryChecks()
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
	w.GrantAll(sectest.Alice, core.Definitions())

	fx := &fixture{t: t, ctx: ctx, w: w, admin: admin, le: ids.New().String(), cust: ids.New().String()}
	fx.org = &orgStandIn{les: map[string]map[string]any{fx.le: {"id": fx.le, "code": "SH", "name": "上海公司",
		"state": "active", "timezone": "Asia/Shanghai"}}}
	fx.sales = &salesStandIn{orders: map[string]map[string]any{}, ledger: map[string]*ledgerLine{}, lineOf: map[string]string{},
		claims: map[string]*claimIT{}, mode: map[string]string{}, expired: map[string]bool{}, calls: map[string]int{}}
	fx.print = &printStandIn{current: 3, data: map[int64][]byte{}}
	portFree(t, "localhost:"+itPort, itSystem, itOrg, itCurrency, itSales, itPrint)
	serve(t, itOrg, w, core.Org, func(s *grpc.Server) { orgv3.RegisterSourceServiceServer(s, fx.org) })
	serve(t, itCurrency, w, core.Currency, func(s *grpc.Server) { currencyv3.RegisterSourceServiceServer(s, currencyStandIn{}) })
	serve(t, itSales, w, core.Sales, func(s *grpc.Server) {
		salesv3.RegisterOrderServiceServer(s, fx.sales)
		salesv3.RegisterBillingSourceServiceServer(s, fx.sales)
	})
	serve(t, itPrint, w, core.Print, func(s *grpc.Server) { printv3.RegisterRenderServiceServer(s, fx.print) })

	env := w.Env(t, core.ID)
	for k, v := range map[string]string{"COMPONENT_ID": core.ID, "COMPONENT_VERSION": billing.Version, "PORT": itPort,
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
	require.Equal(t, launcher.ExitOK, launcher.Run(ctx, billing.Definition(), migrate))
	sctx, stop := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- launcher.Run(sctx, billing.Definition(), opts) }()
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
