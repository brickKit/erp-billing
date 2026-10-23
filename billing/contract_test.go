package billing

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/mergesafety"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheManifestDeclaresWhatTheSDKReads: component.yaml's configSchema is exactly the keys the SDK reads.
func TestTheManifestDeclaresWhatTheSDKReads(t *testing.T) {
	raw, err := os.ReadFile("../component.yaml")
	require.NoError(t, err)
	if p := launcher.ManifestProblems(raw, Definition(), nil); len(p) > 0 {
		t.Errorf("component.yaml's configSchema differs from what the SDK reads:\n%v\nthe block:\n%s", p,
			launcher.ConfigSchemaYAML(Definition(), nil))
	}
}

// TestTheManifestListsThePublishedEvents: component.yaml's events.publishes is exactly the event contracts.
func TestTheManifestListsThePublishedEvents(t *testing.T) {
	raw, err := os.ReadFile("../component.yaml")
	require.NoError(t, err)
	assert.Empty(t, launcher.EventProblems(raw, os.DirFS("../contract/events")))
}

// TestTheRoutesAreTheOpenAPIDocument: every route is in contract/openapi.yaml with its operation, and nothing else is.
func TestTheRoutesAreTheOpenAPIDocument(t *testing.T) {
	raw, err := os.ReadFile("../contract/openapi.yaml")
	require.NoError(t, err)
	var r httpx.Routes
	require.NoError(t, (&Module{}).Routes(&r))
	assert.Empty(t, httpx.OpenAPIProblems(raw, r.List()))
}

// TestThePayloadsFitTheirSchemas: an issued invoice's state and its "invoiced" fact are what contract/events promises.
func TestThePayloadsFitTheirSchemas(t *testing.T) {
	at := "2026-10-23T08:00:00.000000Z"
	number, date, tax, tpl, ver := "INV-SH-2026-000001", "2026-10-23", "91310000MA1", "invoice", int64(2)
	issued, _ := time_(at)
	v := Invoice{ID: "01a15ab0-0000-7000-8000-0000000000a1", Number: &number,
		LegalEntityID: "01a15ab0-0000-7000-8000-0000000000a2",
		Customer:      Customer{PartnerID: "01a15ab0-0000-7000-8000-0000000000a3", Name: "上海客户", TaxID: &tax},
		Currency:      "USD", ExchangeRate: &Rate{Rate: "7.1", Date: date, Type: "spot"},
		Source: Source{Kind: "sales_order", ID: "01a15ab0-0000-7000-8000-0000000000a4"}, State: "issued",
		Formalization: "done", InvoiceDate: &date,
		Lines: []Line{{ID: "01a15ab0-0000-7000-8000-0000000000a5", LineNo: 1, SourceLineID: "01a15ab0-0000-7000-8000-0000000000a6",
			Product: Product{ID: "01a15ab0-0000-7000-8000-0000000000a7", Code: "SCREW", Name: "螺丝"}, Quantity: "2",
			Unit: "BX", UnitPrice: "120", Net: "240", TaxRate: "0.13", Tax: "31.2"}},
		Totals: Totals{Net: "240", Tax: "31.2", Gross: "271.2"}, Print: PrintState{Template: &tpl, TemplateVersion: &ver, Count: 2},
		Version: 4, CreatedAt: at, UpdatedAt: at, IssuedAt: &issued}
	validate(t, InvoiceChanged, v)
	validate(t, InvoiceIssued, issuedFact(v))
	draft := Invoice{ID: v.ID, LegalEntityID: v.LegalEntityID, Customer: v.Customer, Currency: "CNY", Source: v.Source,
		State: "draft", Formalization: "none", Lines: v.Lines, Totals: v.Totals, Version: 1, CreatedAt: at, UpdatedAt: at}
	validate(t, InvoiceChanged, draft)
}

func validate(t *testing.T, typ string, v any) {
	t.Helper()
	s, err := jsonschema.NewCompiler().Compile("../contract/events/" + typ + ".schema.json")
	require.NoError(t, err, typ)
	b, err := json.Marshal(v)
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	require.NoError(t, err)
	assert.NoError(t, s.Validate(doc), typ)
}

// TestTheModuleIsMergeSafe: the module is compiled into the Go core shell.
func TestTheModuleIsMergeSafe(t *testing.T) {
	findings, err := mergesafety.CheckDir(".")
	require.NoError(t, err)
	for _, f := range findings {
		t.Errorf("%v", f)
	}
}
