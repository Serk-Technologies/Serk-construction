package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestJournalListCursorPagination (XCT-API.2.e): the journal worklist pages by
// an opaque cursor in a stable newest-first order. Journals added between
// pages do not appear in later pages and none is returned twice; the cursor is
// refused with another status, source filter or tenant, or when damaged.
func TestJournalListCursorPagination(t *testing.T) {
	e := newEnv(t)
	prep, reader := e.token("u-jc-prep", "accountant"), e.token("u-jc-reader", "controller")
	n := 0
	mk := func() {
		n++
		if st, out := e.do("POST", "/v1/journals", prep, e.journalBody(fmt.Sprintf("jcur-%d", n), "250.00")); st != http.StatusCreated {
			t.Fatalf("journal %d: %d %v", n, st, out)
		}
	}
	for i := 0; i < 7; i++ {
		mk()
	}
	ids := func(out map[string]any) []string {
		var r []string
		items, _ := out["journals"].([]any)
		for _, x := range items {
			r = append(r, x.(map[string]any)["id"].(string))
		}
		return r
	}
	const list = "/v1/journals?status=DRAFT&source_prefix=MANUAL"
	st, all := e.do("GET", list+"&limit=500", reader, nil)
	if st != http.StatusOK || len(ids(all)) != 7 || all["next"] != nil {
		t.Fatalf("full list: %d %v", st, all)
	}
	want := ids(all)
	if st, def := e.do("GET", list, reader, nil); st != http.StatusOK || strings.Join(ids(def), ",") != strings.Join(want, ",") {
		t.Fatalf("default page differs from the full list: %d %v", st, def)
	}

	st, p1 := e.do("GET", list+"&limit=3", reader, nil)
	next, _ := p1["next"].(string)
	if st != http.StatusOK || len(ids(p1)) != 3 || next == "" {
		t.Fatalf("page 1: %d %v", st, p1)
	}
	mk()
	mk()
	got := ids(p1)
	for pages := 0; next != "" && pages < 10; pages++ {
		st, pg := e.do("GET", list+"&limit=3&cursor="+next, reader, nil)
		if st != http.StatusOK {
			t.Fatalf("next page: %d %v", st, pg)
		}
		got = append(got, ids(pg)...)
		next, _ = pg["next"].(string)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paging while journals were added returned\n %v\nwant the journals that existed at page 1, in order, once each\n %v", got, want)
	}

	_, p1 = e.do("GET", list+"&limit=3", reader, nil)
	cur, _ := p1["next"].(string)
	for name, path := range map[string]string{
		"other status filter": "/v1/journals?status=APPROVED&source_prefix=MANUAL&cursor=" + cur,
		"other source filter": "/v1/journals?status=DRAFT&source_prefix=P2P_&cursor=" + cur,
		"no filters":          "/v1/journals?cursor=" + cur,
		"damaged cursor":      list + "&cursor=" + cur[:len(cur)-4] + "AAAA",
		"not a cursor":        list + "&cursor=not-a-cursor",
		"limit out of range":  list + "&limit=501",
		"limit not a number":  list + "&limit=ten",
	} {
		if st, out := e.do("GET", path, reader, nil); st != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %v, want 422", name, st, out)
		}
	}
	// A document-list cursor is not a journal cursor.
	buyer := e.token("u-jc-buyer", "procurement_officer")
	for _, c := range []string{"JC-SUP-A", "JC-SUP-B"} {
		if st, out := e.do("POST", "/v1/suppliers", buyer, map[string]any{"legal_entity_id": e.entity, "code": c, "name": c + " PLC", "tax_id": "TIN-" + c}); st != http.StatusCreated {
			t.Fatalf("supplier %s: %d %v", c, st, out)
		}
	}
	_, sup := e.do("GET", "/v1/p2p/supplier?limit=1", buyer, nil)
	dc, _ := sup["next"].(string)
	if dc == "" {
		t.Fatalf("no supplier-list cursor to try: %v", sup)
	}
	if st, out := e.do("GET", "/v1/journals?cursor="+dc, reader, nil); st != http.StatusUnprocessableEntity {
		t.Errorf("a document-list cursor: %d %v, want 422", st, out)
	}
	other := uuid.NewString()
	e.db.ProvisionTenant(t, other, "journal-cursor-other")
	now := time.Now()
	tok, err := testVerifier.Sign(Claims{Subject: "u-jc-reader", TenantID: other, Roles: []string{"controller"}, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), Issuer: "serk-id", Audience: "serk-construction", Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if st, out := e.do("GET", list+"&limit=3&cursor="+cur, tok, nil); st != http.StatusUnprocessableEntity {
		t.Errorf("another tenant's cursor: %d %v, want 422", st, out)
	}
	// Project-scoped people still cannot read the worklist, cursor or not.
	scoped := e.tok("u-jc-scoped", []string{uuid.NewString()}, false, "accountant")
	if st, _ := e.do("GET", list+"&cursor="+cur, scoped, nil); st != http.StatusForbidden {
		t.Errorf("a project-scoped reader: %d, want 403", st)
	}
}
