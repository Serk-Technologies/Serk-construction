package httpapi

import (
	"testing"

	"github.com/google/uuid"
)

// Guarantees and price indices through the API (0050): roles reach only
// their routes, an index value goes through maker/checker with fresh MFA,
// and unknown contracts and certificates answer as not found.
func TestAPIBondsAndIndices(t *testing.T) {
	e := newEnv(t)
	qs := e.token("u-qs-api", "quantity_surveyor")
	commercial := e.token("u-cm-api", "commercial_manager")
	commercialMFA := e.mfaToken("u-cm-api", "commercial_manager")
	treasury := e.token("u-tr-api", "treasury_officer")

	if code, out := e.do("POST", "/v1/price-indices", commercial, map[string]any{"code": "CEM", "name": "Cement price index", "source": "CSA"}); code != 403 {
		t.Fatalf("the approver created an index: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/price-indices", qs, map[string]any{"code": "CEM", "name": "Cement price index", "source": "CSA"}); code != 201 {
		t.Fatalf("index: %d %v", code, out)
	}
	code, v := e.do("POST", "/v1/price-index-values", qs, map[string]any{"index_code": "CEM", "period": "2026-01", "value": "120.5", "source_ref": "CSA bulletin 2026-01"})
	if code != 201 {
		t.Fatalf("value: %d %v", code, v)
	}
	id := v["id"].(string)
	if code, out := e.do("POST", "/v1/price-index-values/"+id+"/submit", qs, nil); code != 200 {
		t.Fatalf("submit: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/price-index-values/"+id+"/approve", commercial, nil); code == 200 {
		t.Fatalf("approved without fresh MFA: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/price-index-values/"+id+"/approve", commercialMFA, nil); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	code, list := e.do("GET", "/v1/price-indices", commercial, nil)
	idx := list["indices"].([]any)
	if code != 200 || len(idx) != 1 {
		t.Fatalf("indices: %d %v", code, list)
	}
	vals := idx[0].(map[string]any)["values"].([]any)
	if len(vals) != 1 || vals[0].(map[string]any)["status"] != "APPROVED" || vals[0].(map[string]any)["period"] != "2026-01" {
		t.Fatalf("values: %v", vals)
	}
	if code, out := e.do("GET", "/v1/p2p/price_index_value/"+id, qs, nil); code != 200 || out["status"] != "APPROVED" {
		t.Fatalf("value as a document: %d %v", code, out)
	}

	missing := uuid.NewString()
	if code, out := e.do("POST", "/v1/guarantees", qs, map[string]any{"contract_id": missing, "kind": "BID", "issuer": "Bank", "reference": "R-1",
		"amount": "100", "issued_on": "2026-10-01", "expires_on": "2027-10-01"}); code != 403 {
		t.Fatalf("a quantity surveyor registered a guarantee: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/guarantees", treasury, map[string]any{"contract_id": missing, "kind": "BID", "issuer": "Bank", "reference": "R-1",
		"amount": "100", "issued_on": "2026-10-01", "expires_on": "2027-10-01"}); code != 404 {
		t.Fatalf("a guarantee on an unknown contract: %d %v", code, out)
	}
	if code, out := e.do("GET", "/v1/guarantees", treasury, nil); code != 200 || len(out["guarantees"].([]any)) != 0 {
		t.Fatalf("register: %d %v", code, out)
	}
	if code, out := e.do("GET", "/v1/escalation?contract="+missing, commercial, nil); code != 404 {
		t.Fatalf("escalation of an unknown contract: %d %v", code, out)
	}
	if code, out := e.do("GET", "/v1/price-adjustments/preview?ipc="+missing+"&period=2026-09", qs, nil); code != 404 {
		t.Fatalf("preview of an unknown certificate: %d %v", code, out)
	}
	if code, out := e.do("GET", "/v1/obligations", commercial, nil); code != 200 || len(out["obligations"].([]any)) != 0 {
		t.Fatalf("obligations: %d %v", code, out)
	}
	if code, out := e.do("GET", "/v1/contracts/"+missing+"/time", commercial, nil); code != 404 {
		t.Fatalf("time of an unknown contract: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/ld-terms", e.token("u-ar-api", "ar_clerk"), map[string]any{"contract_id": missing, "clause_ref": "8.8", "completion_date": "2027-01-01",
		"rate_per_day": "1000", "cap_amount": "10000"}); code != 403 {
		t.Fatalf("an AR clerk set damages terms: %d %v", code, out)
	}
}
