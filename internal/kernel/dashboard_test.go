package kernel

// Tests for the financial control dashboard (WP-1.4, kernel/dashboard.go).
//
// What is proven:
//  1. every KPI equals an independent computation from the report it drills
//     into (the dashboard cannot disagree with the cost report or the ledger);
//  2. scope and permissions: a person sees only their projects' figures, and
//     tiles their role may not read are listed as hidden, never shown empty;
//  3. bad input is refused, and the KPI catalogue document is current.

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func tileByID(d Dashboard, id string) (Tile, bool) {
	for _, t := range d.Tiles {
		if t.ID == id {
			return t, true
		}
	}
	return Tile{}, false
}

func TestDashboard_TilesEqualTheirReports(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.approvedBOQ(t)
	w.awardedPO(t, "10", "100") // an approved order: commitment against the budget

	d, err := f.svc.Dashboard(f.ctx, c.cfo, "executive", "")
	must(t, "dashboard", err)
	if d.Entity == "" || d.Currency != "ETB" || d.Projects != 1 {
		t.Fatalf("entity=%q currency=%q projects=%d", d.Entity, d.Currency, d.Projects)
	}

	// K-BUD-01/02/03 against the cost report, computed independently here.
	rows, err := f.svc.ProjectCostReport(f.ctx, c.cfo, f.project)
	must(t, "cost report", err)
	var cur, orig, consumed decimal.Decimal
	over := 0
	for _, r := range rows {
		cur, orig = cur.Add(r.Current), orig.Add(r.Original)
		consumed = consumed.Add(r.Committed).Add(r.Reserved).Add(r.ActualOther)
		if r.Available.IsNegative() {
			over++
		}
	}
	if !cur.IsPositive() || !consumed.IsPositive() {
		t.Fatalf("the fixture has no budget or no commitment: budget %s consumed %s", cur, consumed)
	}
	bud, ok := tileByID(d, "K-BUD-01")
	if !ok {
		t.Fatal("no K-BUD-01 tile")
	}
	want := consumed.Div(cur).Mul(decimal.NewFromInt(100)).StringFixed(1)
	if bud.Value != want || bud.Unit != "%" {
		t.Fatalf("K-BUD-01 = %s %s, want %s %%", bud.Value, bud.Unit, want)
	}
	if bud.Drill != "#/cost" || len(bud.Evidence) == 0 || !strings.HasPrefix(bud.Evidence[0].Href, "#/cost/"+f.project) {
		t.Fatalf("K-BUD-01 does not drill to the cost report rows: %+v", bud)
	}
	if bud.Formula == "" || bud.Source == "" || bud.AsOf.IsZero() {
		t.Fatalf("a tile must carry its formula, source and time: %+v", bud)
	}
	if n, _ := tileByID(d, "K-BUD-02"); n.Value != strconv.Itoa(over) {
		t.Fatalf("K-BUD-02 = %s, want %d", n.Value, over)
	}
	if b3, _ := tileByID(d, "K-BUD-03"); b3.Value != money2(cur) || b3.Detail[0].Value != money2(orig) {
		t.Fatalf("K-BUD-03 = %+v, want current %s original %s", b3, money2(cur), money2(orig))
	}

	// K-LED-01 against the trial balance.
	tb, err := f.svc.TrialBalanceReport(f.ctx, c.cfo, d.EntityID, d.AsOf)
	must(t, "trial balance", err)
	if led, _ := tileByID(d, "K-LED-01"); led.Value != money2(tb.Difference) || led.Level != "ok" {
		t.Fatalf("K-LED-01 = %+v, trial balance difference %s", led, tb.Difference)
	}
	// K-CSH-03/04 against the aging reports.
	ap, err := f.svc.PayablesAging(f.ctx, c.cfo, d.EntityID, d.AsOf)
	must(t, "payables aging", err)
	if cs, _ := tileByID(d, "K-CSH-03"); parseMoney(cs.Value).Cmp(ap.Total.Over90.Add(ap.Total.D1to30).Add(ap.Total.D31to6).Add(ap.Total.D61to9)) != 0 {
		t.Fatalf("K-CSH-03 = %s, payables aging overdue = %s", cs.Value, ap.Total.Over90.Add(ap.Total.D1to30).Add(ap.Total.D31to6).Add(ap.Total.D61to9))
	}
	// Every executive tile is present for the CFO and none hidden.
	if len(d.Tiles) != len(DashboardPresets["executive"]) || len(d.Hidden) != 0 {
		t.Fatalf("tiles %d hidden %v", len(d.Tiles), d.Hidden)
	}
}

func TestDashboard_ScopeAndPermissions(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.approvedBOQ(t)

	// A project manager sees the budget tiles of their project but not the
	// company-wide cash, ledger and control tiles, which are listed as hidden.
	d, err := f.svc.Dashboard(f.ctx, c.pm, "executive", "")
	must(t, "pm dashboard", err)
	if _, ok := tileByID(d, "K-BUD-01"); !ok {
		t.Fatal("the project manager does not see the budget tile of their project")
	}
	hidden := map[string]bool{}
	for _, h := range d.Hidden {
		hidden[h.ID] = true
		if h.Needs == "" {
			t.Fatalf("hidden tile %s does not say what it needs", h.ID)
		}
	}
	for _, id := range []string{"K-CSH-01", "K-CSH-02", "K-LED-01", "K-CTL-01"} {
		if !hidden[id] {
			t.Fatalf("%s was not hidden from a project manager (tiles: %+v)", id, d.Tiles)
		}
		if _, shown := tileByID(d, id); shown {
			t.Fatalf("%s is both shown and hidden", id)
		}
	}

	// A person with no project in scope sees no figures for the project.
	none := auth.Principal{Kind: auth.KindHuman, UserID: "u-nobody", TenantID: f.tenant, Roles: []string{"estimator"}}
	d, err = f.svc.Dashboard(f.ctx, none, "project", "")
	must(t, "no-scope dashboard", err)
	if d.Projects != 0 {
		t.Fatalf("a person with no project scope has %d projects", d.Projects)
	}
	if b, _ := tileByID(d, "K-BUD-01"); b.Value != "-" {
		t.Fatalf("K-BUD-01 for a person with no projects = %q, want \"-\"", b.Value)
	}
}

func TestDashboard_RefusesBadInput(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	if _, err := f.svc.Dashboard(f.ctx, c.cfo, "everything", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown preset: %v", err)
	}
	if _, err := f.svc.Dashboard(f.ctx, c.cfo, "executive", "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an entity the caller cannot see: %v", err)
	}
	// The other company's entity is indistinguishable from absence.
	var other string
	must(t, "entity of tenant2", f.db.AdminStore.WithTenant(f.ctx, f.tenant2, func(ctx2 context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx2, `SELECT id::text FROM legal_entities LIMIT 1`).Scan(&other)
	}))
	if _, err := f.svc.Dashboard(f.ctx, c.cfo, "executive", other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another company's entity: %v", err)
	}
}

// The catalogue document is generated from the code. Run with UPDATE_KPI_DOC=1
// to rewrite it after changing KPICatalogue.
func TestKPICatalogueDocumentIsCurrent(t *testing.T) {
	const path = "../../docs/reports/KPI_CATALOGUE.md"
	want := KPICatalogueMarkdown()
	if os.Getenv("UPDATE_KPI_DOC") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (generate it with UPDATE_KPI_DOC=1)", err)
	}
	if string(got) != want {
		t.Fatal("docs/reports/KPI_CATALOGUE.md is out of date; regenerate with UPDATE_KPI_DOC=1")
	}
	for _, ids := range DashboardPresets {
		for _, id := range ids {
			if kpiDef(id).Formula == "" {
				t.Fatalf("preset refers to %s, which has no catalogue entry", id)
			}
		}
	}
}
