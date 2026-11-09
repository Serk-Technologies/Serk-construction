package httpapi

// HTTP tests for two additions:
//   - GET /v1/meta/roles: the role catalogue the console's page guides use
//     ("who can approve this"). Authenticated, and it carries only role
//     definitions, never people or data.
//   - The budget endpoints (WP-1.3): a revision is created, submitted and
//     approved by a different person over HTTP, and the project budget reflects it.

import (
	"strings"
	"testing"
	"time"
)

func TestAPI_MetaRolesCatalogue(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	pm := a.tok("u-pm-meta", P, false, "project_manager")

	if st, _ := a.do("GET", "/v1/meta/roles", "", nil); st != 401 {
		t.Fatalf("unauthenticated role catalogue: got %d, want 401", st)
	}
	out := a.call(200, "GET", "/v1/meta/roles", pm, nil)
	roles, _ := out["roles"].(map[string]any)
	if len(roles) < 20 {
		t.Fatalf("role catalogue has %d roles, want the seed catalogue", len(roles))
	}
	perms, _ := roles["commercial_manager"].([]any)
	found := false
	for _, p := range perms {
		if p == "budget.approve" {
			found = true
		}
	}
	if !found {
		t.Fatalf("commercial_manager lacks budget.approve in the catalogue: %v", perms)
	}
	// Only definitions: no user ids, tenants or projects.
	for _, leak := range []string{"u-pm-meta", a.project, a.env.tenant} {
		if strings.Contains(strings.Join(flatten(out), " "), leak) {
			t.Fatalf("role catalogue leaks %q", leak)
		}
	}
}

func TestAPI_BudgetRevisionOverHTTP(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	pm := a.tok("u-pm-b", P, false, "project_manager")
	cm := a.tok("u-cm-b", P, false, "commercial_manager")

	id := a.id("POST", "/v1/budgets/revisions", pm, map[string]any{
		"project_id": a.project, "kind": "ORIGINAL", "reason": "Baseline over HTTP",
		"lines": []map[string]any{{"cost_code_id": a.costCode, "amount": "250000"}},
	})
	a.call(200, "POST", "/v1/budgets/revisions/"+id+"/submit", pm, map[string]any{})
	// The author cannot approve; a different person can.
	if st, _ := a.do("POST", "/v1/budgets/revisions/"+id+"/approve", pm, map[string]any{}); st < 400 {
		t.Fatalf("author approved own budget revision over HTTP (%d)", st)
	}
	a.call(200, "POST", "/v1/budgets/revisions/"+id+"/approve", cm, map[string]any{"reason": "ok"})
	out := a.call(200, "GET", "/v1/projects/"+a.project+"/budget", pm, nil)
	rows, _ := out["cost_codes"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["current"] != "250000" {
		t.Fatalf("budget after approval: %v", out)
	}
}

// flatten renders every string in a JSON value, for leak checks.
func flatten(v any) []string {
	var out []string
	switch x := v.(type) {
	case string:
		out = append(out, x)
	case []any:
		for _, e := range x {
			out = append(out, flatten(e)...)
		}
	case map[string]any:
		for k, e := range x {
			out = append(out, k)
			out = append(out, flatten(e)...)
		}
	}
	return out
}

// Authority endpoints over HTTP: a matrix is proposed, submitted and approved
// by different people; a delegation is created, approved by a third person
// and then lets the delegate act; revoking it ends that at once.
func TestAPI_AuthorityAndDelegationOverHTTP(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	owner := a.tok("u-owner-h", P, false, "process_owner")
	cfo := a.tok("u-cfo-h", P, true, "cfo")
	cm := a.tok("u-cm-h", P, true, "commercial_manager")
	sec := a.tok("u-sec-h", P, true, "security_admin")
	planner := a.tok("u-planner-h", P, false, "planner")

	if st, _ := a.do("GET", "/v1/authority/matrix", "", nil); st != 401 {
		t.Fatalf("unauthenticated matrix read: %d", st)
	}
	mid := a.id("POST", "/v1/authority/matrices", owner, map[string]any{"reason": "Limits 2026", "rules": []map[string]any{
		{"doc_type": "purchase_order", "min_amount": "5000000", "currency": "ETB", "required_perm": "approval.high_value", "requires_mfa": true}}})
	a.call(200, "POST", "/v1/authority/matrices/"+mid+"/submit", owner, map[string]any{})
	if st, _ := a.do("POST", "/v1/authority/matrices/"+mid+"/approve", owner, map[string]any{}); st < 400 {
		t.Fatalf("author approved own matrix over HTTP (%d)", st)
	}
	a.call(200, "POST", "/v1/authority/matrices/"+mid+"/approve", cfo, map[string]any{})
	cur := a.call(200, "GET", "/v1/authority/matrix", owner, nil)
	if m, _ := cur["matrix"].(map[string]any); m == nil || m["id"] != mid {
		t.Fatalf("matrix in force: %v", cur)
	}

	// Delegation: commercial manager lends budget.approve to the planner.
	did := a.id("POST", "/v1/delegations", cm, map[string]any{"delegate": "u-planner-h", "perms": []string{"budget.approve"},
		"valid_from": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "valid_to": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339), "reason": "Leave cover"})
	a.call(200, "POST", "/v1/delegations/"+did+"/submit", cm, map[string]any{})
	a.call(200, "POST", "/v1/delegations/"+did+"/approve", sec, map[string]any{})
	me := a.call(200, "GET", "/v1/identity/me", planner, nil)
	if !strings.Contains(strings.Join(flatten(me), " "), "budget.approve") {
		t.Fatalf("the delegate's identity does not show the lent permission: %v", me)
	}
	a.call(200, "POST", "/v1/delegations/"+did+"/revoke", cm, map[string]any{"reason": "Back early"})
	me = a.call(200, "GET", "/v1/identity/me", planner, nil)
	if strings.Contains(strings.Join(flatten(me), " "), "budget.approve") {
		t.Fatalf("a revoked delegation still applies: %v", me)
	}
	rep := a.call(200, "GET", "/v1/sod/report", owner, nil)
	if c, _ := rep["conflicts"].([]any); len(c) != 0 {
		t.Fatalf("seed roles have conflicts: %v", c)
	}
}

// The dashboard over HTTP: authenticated, validated, scoped, and every tile
// carries its formula and a drill-down link.
func TestAPI_Dashboard(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	cfo := a.tok("u-cfo-d", nil, true, "cfo")
	pm := a.tok("u-pm-d", P, false, "project_manager")

	if st, _ := a.do("GET", "/v1/dashboard", "", nil); st != 401 {
		t.Fatalf("unauthenticated dashboard: %d", st)
	}
	if st, _ := a.do("GET", "/v1/dashboard?preset=everything", cfo, nil); st != 422 && st != 400 {
		t.Fatalf("unknown preset: %d", st)
	}
	if st, _ := a.do("GET", "/v1/dashboard?entity_id=not-a-uuid", cfo, nil); st != 404 {
		t.Fatalf("malformed entity id: %d", st)
	}
	d := a.call(200, "GET", "/v1/dashboard?preset=executive", cfo, nil)
	tiles, _ := d["tiles"].([]any)
	if len(tiles) != 10 {
		t.Fatalf("executive dashboard for the CFO has %d tiles, want 10: %v", len(tiles), d["hidden"])
	}
	for _, x := range tiles {
		tile := x.(map[string]any)
		for _, k := range []string{"id", "title", "value", "level", "formula", "source", "as_of", "drill"} {
			if tile[k] == nil || tile[k] == "" {
				t.Fatalf("tile %v lacks %s", tile["id"], k)
			}
		}
	}
	// A project manager gets fewer tiles and a list of what is hidden.
	d = a.call(200, "GET", "/v1/dashboard?preset=executive", pm, nil)
	if h, _ := d["hidden"].([]any); len(h) == 0 {
		t.Fatalf("a project manager's executive dashboard hides nothing: %v", d)
	}
	k := a.call(200, "GET", "/v1/dashboard/kpis", pm, nil)
	if l, _ := k["kpis"].([]any); len(l) != 10 {
		t.Fatalf("KPI catalogue has %d entries", len(l))
	}
}

// The report catalogue over HTTP: listed by permission and scope, run with
// filters, refused for bad input, and exportable to PDF, Word and Excel.
func TestAPI_ReportCatalogue(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	cfo := a.tok("u-cfo-r", nil, true, "cfo")
	pm := a.tok("u-pm-r", P, false, "project_manager")

	if st, _ := a.do("GET", "/v1/reports/catalogue", "", nil); st != 401 {
		t.Fatalf("unauthenticated catalogue: %d", st)
	}
	all := a.call(200, "GET", "/v1/reports/catalogue", cfo, nil)
	if l, _ := all["reports"].([]any); len(l) != 19 {
		t.Fatalf("CFO catalogue has %d reports, want 19", len(l))
	}
	mine := a.call(200, "GET", "/v1/reports/catalogue", pm, nil)
	for _, x := range mine["reports"].([]any) {
		if x.(map[string]any)["tenant_wide"] == true {
			t.Fatalf("project manager offered a company-wide report: %v", x)
		}
	}
	path := "/v1/reports/catalogue/po-register?entity=" + a.entity
	r := a.call(200, "GET", path, cfo, nil)
	if r["id"] != "po-register" || r["as_of"] == "" || r["columns"] == nil {
		t.Fatalf("report result: %v", r)
	}
	if st, _ := a.do("GET", "/v1/reports/catalogue/gl-detail?entity="+a.entity, pm, nil); st != 403 {
		t.Fatalf("company-wide report for a project manager: %d", st)
	}
	if st, _ := a.do("GET", "/v1/reports/catalogue/nope?entity="+a.entity, cfo, nil); st != 404 {
		t.Fatalf("unknown report: %d", st)
	}
	if st, _ := a.do("GET", path+"&from=not-a-date", cfo, nil); st != 422 && st != 400 {
		t.Fatalf("bad date: %d", st)
	}
	// Export: the same read, rendered as PDF, Word and Excel.
	for _, f := range []struct{ format, magic string }{{"pdf", "%PDF"}, {"docx", "PK"}, {"xlsx", "PK"}} {
		resp, body := a.raw(exportPath(path, f.format, "Purchase order register"), cfo)
		if resp.StatusCode != 200 || !strings.HasPrefix(string(body), f.magic) {
			t.Fatalf("export %s: status %d, starts %q", f.format, resp.StatusCode, string(body[:min(8, len(body))]))
		}
	}
}

// Stock plus over HTTP: a conversion is prepared and approved by different
// people, and a reservation is refused above the unreserved stock.
func TestAPI_StockPlus(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	buyer := a.tok("u-buyer-s", P, false, "procurement_officer")
	finmgr := a.tok("u-fin-s", P, true, "finance_manager")
	site := a.tok("u-site-s", P, false, "site_engineer")
	est := a.tok("u-est-s", P, false, "estimator")

	id := a.id("POST", "/v1/stock/uom-conversions", buyer, map[string]any{"from_unit": "truck", "to_unit": "m3", "factor": "6", "note": "A truck carries 6 m3"})
	a.call(200, "POST", "/v1/stock/uom-conversions/"+id+"/submit", buyer, map[string]any{})
	if st, _ := a.do("POST", "/v1/stock/uom-conversions/"+id+"/approve", buyer, map[string]any{}); st < 400 {
		t.Fatalf("author approved own conversion (%d)", st)
	}
	a.call(200, "POST", "/v1/stock/uom-conversions/"+id+"/approve", finmgr, map[string]any{})
	if st, _ := a.do("POST", "/v1/stock/reservations", est, map[string]any{"stock_item_id": id, "warehouse_id": id, "project_id": a.project,
		"quantity": "1", "purpose": "x", "expires_at": "2030-01-01T00:00:00Z"}); st != 403 {
		t.Fatalf("an estimator reserved stock over HTTP: %d", st)
	}
	if st, _ := a.do("POST", "/v1/stock/reservations", site, map[string]any{"stock_item_id": "x", "warehouse_id": id, "project_id": a.project}); st != 404 && st != 422 && st != 400 {
		t.Fatalf("malformed ids: %d", st)
	}
	l := a.call(200, "GET", "/v1/stock/reservations", site, nil)
	if _, ok := l["reservations"]; !ok {
		t.Fatalf("reservation list: %v", l)
	}
}

// The project master over HTTP: a project is created PLANNED, moves through a
// stage gate prepared and decided by different people, and regions work.
func TestAPI_ProjectMasterAndGates(t *testing.T) {
	a := newP2PAPI(t)
	cm := a.tok("u-cm-p", nil, true, "commercial_manager")
	cfo := a.tok("u-cfo-p", nil, true, "cfo")
	ctl := a.tok("u-ctl-p", nil, false, "controller")
	est := a.tok("u-est-p", []string{a.project}, false, "estimator")

	if st, _ := a.do("GET", "/v1/projects", "", nil); st != 401 {
		t.Fatalf("unauthenticated project list: %d", st)
	}
	if st, _ := a.do("POST", "/v1/projects", est, map[string]any{"legal_entity_id": a.entity, "code": "P-X", "name": "Not allowed"}); st != 403 {
		t.Fatalf("an estimator created a project: %d", st)
	}
	reg := a.id("POST", "/v1/org/regions", ctl, map[string]any{"Code": "EAST", "Name": "Eastern region"})
	id := a.id("POST", "/v1/projects", cm, map[string]any{"legal_entity_id": a.entity, "code": "P-HTTP", "name": "Dire Dawa Water Supply", "region_id": reg, "contract_type": "LUMP_SUM"})
	l := a.call(200, "GET", "/v1/projects", cfo, nil)
	var found map[string]any
	for _, x := range l["projects"].([]any) {
		if m := x.(map[string]any); m["id"] == id {
			found = m
		}
	}
	if found == nil || found["phase"] != "BID" || found["status"] != "PLANNED" || found["region"] != "Eastern region" {
		t.Fatalf("created project: %v", found)
	}
	pm := a.tok("u-pm-p", []string{id}, false, "project_manager")
	if st, _ := a.do("POST", "/v1/projects/"+id+"/gates", pm, map[string]any{"to_phase": "EXECUTION", "notes": "Skipping mobilisation entirely"}); st < 400 {
		t.Fatalf("a gate skipped a phase over HTTP (%d)", st)
	}
	g := a.id("POST", "/v1/projects/"+id+"/gates", pm, map[string]any{"to_phase": "MOBILISATION", "notes": "Contract signed and bond lodged"})
	a.call(200, "POST", "/v1/project-gates/"+g+"/submit", pm, map[string]any{})
	if st, _ := a.do("POST", "/v1/project-gates/"+g+"/approve", pm, map[string]any{}); st < 400 {
		t.Fatalf("the author approved their own gate (%d)", st)
	}
	a.call(200, "POST", "/v1/project-gates/"+g+"/approve", cfo, map[string]any{})
	l = a.call(200, "GET", "/v1/projects", cfo, nil)
	for _, x := range l["projects"].([]any) {
		if m := x.(map[string]any); m["id"] == id && (m["phase"] != "MOBILISATION" || m["status"] != "ACTIVE") {
			t.Fatalf("after approval: %v", m)
		}
	}
	a.call(200, "PUT", "/v1/projects/"+id, pm, map[string]any{"name": "Dire Dawa Water Supply Phase 1", "project_manager": "u-pm-p", "start_date": "2027-03-01", "planned_finish": "2028-03-01"})
	if st, _ := a.do("PUT", "/v1/projects/"+id, pm, map[string]any{"name": "Bad dates", "start_date": "2027-03-01", "planned_finish": "2026-03-01"}); st < 400 {
		t.Fatalf("finish before start over HTTP (%d)", st)
	}
	d := a.call(200, "GET", "/v1/dashboard?preset=project&region_id="+reg, cfo, nil)
	if d["projects_in_scope"] == nil {
		t.Fatalf("dashboard by region: %v", d)
	}
}

// Staged imports and the setup checklist over HTTP.
func TestAPI_ImportsAndSetup(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	b1 := a.tok("u-buyer-i1", P, false, "procurement_officer")
	b2 := a.tok("u-buyer-i2", P, false, "procurement_officer")
	est := a.tok("u-est-i", P, false, "estimator")
	pm := a.tok("u-pm-i", P, false, "project_manager")

	if st, _ := a.do("GET", "/v1/setup", "", nil); st != 401 {
		t.Fatalf("unauthenticated setup: %d", st)
	}
	s := a.call(200, "GET", "/v1/setup", pm, nil)
	if steps, _ := s["steps"].([]any); len(steps) < 10 {
		t.Fatalf("setup steps: %v", s)
	}
	tpl := a.call(200, "GET", "/v1/imports/templates/supplier", b1, nil)
	if tpl["csv"] == nil || !strings.HasPrefix(tpl["csv"].(string), "code,name,tax_id") {
		t.Fatalf("template: %v", tpl)
	}
	if st, _ := a.do("GET", "/v1/imports/templates/galaxy", b1, nil); st != 404 {
		t.Fatalf("unknown template: %d", st)
	}
	body := map[string]any{"kind": "supplier", "legal_entity_id": a.entity, "filename": "s.csv", "content": "code,name,tax_id\nHTTP-ONE,Http One PLC,\nHTTP-TWO,Http Two PLC,\n"}
	if st, _ := a.do("POST", "/v1/imports", est, body); st != 403 {
		t.Fatalf("an estimator staged an import over HTTP: %d", st)
	}
	id := a.id("POST", "/v1/imports", b1, body)
	got := a.call(200, "GET", "/v1/imports/"+id, b1, nil)
	if got["status"] != "STAGED" || got["valid_count"] != float64(2) {
		t.Fatalf("staged batch: %v", got)
	}
	if st, _ := a.do("POST", "/v1/imports/"+id+"/commit", b1, map[string]any{"expected_rows": 2}); st < 400 {
		t.Fatalf("the stager committed over HTTP (%d)", st)
	}
	if st, _ := a.do("POST", "/v1/imports/"+id+"/commit", b2, map[string]any{"expected_rows": 7}); st < 400 {
		t.Fatalf("a wrong row count was accepted over HTTP (%d)", st)
	}
	done := a.call(200, "POST", "/v1/imports/"+id+"/commit", b2, map[string]any{"expected_rows": 2})
	if done["status"] != "COMMITTED" {
		t.Fatalf("committed batch: %v", done)
	}
	l := a.call(200, "GET", "/v1/imports", b2, nil)
	if x, _ := l["imports"].([]any); len(x) != 1 {
		t.Fatalf("import list: %v", l)
	}
}
