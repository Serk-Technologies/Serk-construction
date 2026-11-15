package httpapi

import (
	"time"

	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"
)

// TestProblemDetailsRFC9457 (XCT-API.2.a): every kind of error answer is an
// RFC 9457 problem document — media type application/problem+json and the
// members type, title, status and detail — whether it comes from the kernel,
// request decoding, authentication or the router itself. The older error
// object is still present for clients written before.
func TestProblemDetailsRFC9457(t *testing.T) {
	e := newEnv(t)
	clerk := e.token("u-clerk", "ap_clerk")
	cases := []struct {
		name, method, path, token, ctype, body string
		status                                 int
		code                                   string
	}{
		{"no credentials", "GET", "/v1/journals", "", "application/json", "", http.StatusUnauthorized, "unauthenticated"},
		{"missing permission", "POST", "/v1/journals", clerk, "application/json", `{}`, http.StatusForbidden, "forbidden"},
		{"unknown document type", "GET", "/v1/p2p/no_such_type", clerk, "application/json", "", http.StatusNotFound, "not_found"},
		{"no such route", "GET", "/v1/no-such-endpoint", clerk, "application/json", "", http.StatusNotFound, "not_found"},
		{"wrong method", "DELETE", "/v1/journals", clerk, "application/json", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"malformed JSON", "POST", "/v1/suppliers", e.token("u-buyer", "procurement_officer"), "application/json", `{"name":`, http.StatusBadRequest, "malformed_json"},
		{"wrong media type", "POST", "/v1/suppliers", e.token("u-buyer", "procurement_officer"), "text/plain", `x`, http.StatusUnsupportedMediaType, "unsupported_media_type"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, e.srv.URL+c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", c.ctype)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("%s: status %d, want %d (%v)", c.name, resp.StatusCode, c.status, p)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s: Content-Type %q, want application/problem+json", c.name, ct)
		}
		if p["type"] != ProblemType(c.code) || p["code"] != c.code {
			t.Errorf("%s: type %v code %v, want %s", c.name, p["type"], p["code"], c.code)
		}
		if st, _ := p["status"].(float64); int(st) != c.status {
			t.Errorf("%s: status member %v, want %d", c.name, p["status"], c.status)
		}
		if s, _ := p["title"].(string); s == "" {
			t.Errorf("%s: no title", c.name)
		}
		if s, _ := p["detail"].(string); s == "" {
			t.Errorf("%s: no detail", c.name)
		}
		if old, _ := p["error"].(map[string]any); old == nil || old["code"] != c.code {
			t.Errorf("%s: compatibility error object missing: %v", c.name, p["error"])
		}
		if c.status == http.StatusMethodNotAllowed && resp.Header.Get("Allow") == "" {
			t.Errorf("%s: 405 without an Allow header", c.name)
		}
	}
}

// TestListCursorPagination (XCT-API.2.b-d): document lists page by an opaque
// cursor in a stable newest-first order; rows inserted while paging are
// neither repeated nor cause skips among the rows that existed at the first
// page; a cursor cannot be used with another tenant, document type or status
// filter, and a damaged cursor is refused.
func TestListCursorPagination(t *testing.T) {
	e := newEnv(t)
	buyer := e.token("u-buyer", "procurement_officer")
	mk := func(code string) {
		st, out := e.do("POST", "/v1/suppliers", buyer, map[string]any{"legal_entity_id": e.entity, "code": code, "name": code + " PLC", "tax_id": "TIN-" + code})
		if st != http.StatusCreated && st != http.StatusOK {
			t.Fatalf("create supplier %s: %d %v", code, st, out)
		}
	}
	for _, c := range []string{"CUR-A", "CUR-B", "CUR-C", "CUR-D", "CUR-E", "CUR-F", "CUR-G"} {
		mk(c)
	}
	ids := func(out map[string]any) []string {
		var r []string
		items, _ := out["items"].([]any)
		for _, x := range items {
			r = append(r, x.(map[string]any)["id"].(string))
		}
		return r
	}
	st, all := e.do("GET", "/v1/p2p/supplier?limit=500", buyer, nil)
	if st != http.StatusOK || len(ids(all)) != 7 || all["next"] != nil {
		t.Fatalf("full list: %d %v", st, all)
	}
	want := ids(all)

	// Page 1, then two suppliers are added, then the remaining pages.
	st, p1 := e.do("GET", "/v1/p2p/supplier?limit=3", buyer, nil)
	next, _ := p1["next"].(string)
	if st != http.StatusOK || len(ids(p1)) != 3 || next == "" {
		t.Fatalf("page 1: %d %v", st, p1)
	}
	mk("CUR-NEW1")
	mk("CUR-NEW2")
	got := ids(p1)
	for pages := 0; next != "" && pages < 10; pages++ {
		st, pg := e.do("GET", "/v1/p2p/supplier?limit=3&cursor="+next, buyer, nil)
		if st != http.StatusOK {
			t.Fatalf("next page: %d %v", st, pg)
		}
		got = append(got, ids(pg)...)
		next, _ = pg["next"].(string)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paging while rows were inserted returned\n %v\nwant the rows that existed at page 1, in order, once each\n %v", got, want)
	}

	// The cursor is bound to what it was issued for.
	_, p1 = e.do("GET", "/v1/p2p/supplier?limit=3", buyer, nil)
	cur, _ := p1["next"].(string)
	for name, path := range map[string]string{
		"other status filter": "/v1/p2p/supplier?status=ACTIVE&cursor=" + cur,
		"other document type": "/v1/p2p/customer?cursor=" + cur,
		"damaged cursor":      "/v1/p2p/supplier?cursor=" + cur[:len(cur)-4] + "AAAA",
		"not a cursor":        "/v1/p2p/supplier?cursor=not-a-cursor",
		"limit out of range":  "/v1/p2p/supplier?limit=501",
	} {
		if st, out := e.do("GET", path, buyer, nil); st != http.StatusUnprocessableEntity && st != http.StatusBadRequest {
			t.Errorf("%s: %d %v, want a refusal", name, st, out)
		}
	}
	other := uuid.NewString()
	e.db.ProvisionTenant(t, other, "cursor-other")
	now := time.Now()
	tok, err := testVerifier.Sign(Claims{Subject: "u-buyer", TenantID: other, Roles: []string{"procurement_officer"}, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), Issuer: "serk-id", Audience: "serk-construction", Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if st, out := e.do("GET", "/v1/p2p/supplier?limit=3&cursor="+cur, tok, nil); st != http.StatusUnprocessableEntity {
		t.Errorf("another tenant's cursor: %d %v, want 422", st, out)
	}
}
