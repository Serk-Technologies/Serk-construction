package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

// TestAPIMaintenance drives fleet maintenance (0056, ADR 0023) over HTTP: the
// work order through its four steps, a part as a stock issue, the return to
// service that the work order gates, plans and the due list.
func TestAPIMaintenance(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	if err := a.db.AdminStore.WithTenant(a.db.Ctx, a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: a.tenant, LegalEntityID: a.entity, Code: "1200", Name: "Inventory", Type: "ASSET", Active: true})
		a.accounts["1200"] = id
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a.seedInventoryGRN(t)
	buyer, finMgr := a.tok("u-buyer", P, false, "procurement_officer"), a.tok("u-finance-manager", nil, true, "finance_manager")
	procMgr, store := a.tok("u-proc-manager", P, false, "procurement_manager"), a.tok("u-storekeeper", P, false, "storekeeper")
	eng, pm := a.tok("u-mt-eng", P, false, "site_engineer"), a.tok("u-mt-pm", P, false, "project_manager")
	officer, officer2 := a.tok("u-mt-officer", nil, false, "plant_officer"), a.tok("u-mt-officer-2", nil, false, "plant_officer")
	managerMFA, manager := a.tok("u-mt-manager", nil, true, "plant_manager"), a.tok("u-mt-manager", nil, false, "plant_manager")
	inspectorMFA, inspector := a.tok("u-mt-inspector", nil, true, "plant_inspector"), a.tok("u-mt-inspector", nil, false, "plant_inspector")
	elsewhere := a.tok("u-mt-else", []string{uuid.NewString()}, false, "site_engineer", "project_manager")
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }
	want := func(what string, want, code int, out map[string]any) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d %v, want %d", what, code, out, want)
		}
	}

	oil := a.id("POST", "/v1/stock-items", buyer, map[string]any{"legal_entity_id": a.entity, "code": "DIESEL", "name": "Diesel fuel", "unit": "L", "valuation_method": "WAC", "inventory_account_id": a.accounts["1200"]})
	a.call(200, "POST", "/v1/stock-items/"+oil+"/approve", finMgr, nil)
	wh := a.id("POST", "/v1/warehouses", procMgr, map[string]any{"legal_entity_id": a.entity, "project_id": a.project, "code": "P-TANK", "name": "Site store"})
	grn := a.buyDiesel(t, oil)
	a.call(201, "POST", "/v1/goods-receipts/"+grn+"/put-away", store, map[string]any{"po_line_no": 1, "stock_item_id": oil, "warehouse_id": wh, "quantity": "300"})

	eq := a.id("POST", "/v1/equipment", officer, map[string]any{"legal_entity_id": a.entity, "name": "Dozer 03", "class": "BULLDOZER", "ownership": "OWNED", "manufacturer": "CAT", "model": "D8R", "serial_number": "CATD8R-0303", "opening_meter": "4000"})
	move := func(tok, to, project string) (int, map[string]any) {
		return a.do("POST", "/v1/equipment/"+eq+"/move", tok, map[string]any{"to": to, "reason": "Maintenance test", "project_id": project})
	}
	mustMove := func(tok, to, project string) {
		t.Helper()
		if code, out := move(tok, to, project); code != 200 {
			t.Fatalf("move to %s: %d %v", to, code, out)
		}
	}
	mustMove(managerMFA, "AVAILABLE", "")
	res := a.id("POST", "/v1/equipment-reservations", eng, map[string]any{"equipment_id": eq, "project_id": a.project, "purpose": "Ripping", "starts_on": day(0), "ends_on": day(10)})
	a.call(200, "POST", "/v1/equipment-reservations/"+res+"/submit", eng, nil)
	a.call(200, "POST", "/v1/equipment-reservations/"+res+"/approve", managerMFA, nil)
	mustMove(officer, "MOBILIZING", a.project)
	mustMove(eng, "ASSIGNED", "")

	// Preparing: site staff report breakdowns of their own machines only; plant staff prepare any kind.
	wo := map[string]any{"equipment_id": eq, "kind": "PREVENTIVE", "description": "500 hour service due by the meter"}
	code, out := a.do("POST", "/v1/maintenance-work-orders", eng, wo)
	want("preventive order by site staff", 403, code, out)
	wo["kind"] = "BREAKDOWN"
	code, out = a.do("POST", "/v1/maintenance-work-orders", elsewhere, wo)
	want("breakdown reported from another project", 404, code, out)
	code, out = a.do("POST", "/v1/maintenance-work-orders", store, wo)
	want("work order by a storekeeper", 403, code, out)
	wo["kind"] = "REPAINT"
	code, out = a.do("POST", "/v1/maintenance-work-orders", officer, wo)
	want("unknown kind", 422, code, out)
	wo["kind"], wo["description"] = "BREAKDOWN", "Right track thrown on the haul road; idler damaged"
	id := a.id("POST", "/v1/maintenance-work-orders", eng, wo)
	mustMove(eng, "BREAKDOWN", "")
	mustMove(eng, "UNDER_MAINTENANCE", "")

	// Authorisation.
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/approve", managerMFA, nil)
	if code < 400 || code >= 500 {
		t.Fatalf("a draft was authorised: %d %v", code, out)
	}
	a.call(200, "POST", "/v1/maintenance-work-orders/"+id+"/submit", eng, nil)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/approve", officer, nil)
	want("authorised by the plant officer", 403, code, out)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/approve", manager, nil)
	want("authorised without a fresh second factor", 401, code, out)
	a.call(200, "POST", "/v1/maintenance-work-orders/"+id+"/approve", managerMFA, nil)

	// The open work order holds the machine.
	code, out = move(inspectorMFA, "IN_SERVICE", "")
	if code < 400 || code >= 500 {
		t.Fatalf("returned to service with an open work order: %d %v", code, out)
	}

	// A part: a stock issue, approved by someone else.
	part := map[string]any{"warehouse_id": wh, "stock_item_id": oil, "cost_code_id": a.costCode, "expense_account_id": a.accounts["5000"], "quantity": "25"}
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/parts", managerMFA, part)
	want("part requested without stock.issue", 403, code, out)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/parts", store, map[string]any{"warehouse_id": "x"})
	want("malformed part", 422, code, out)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/parts", store, part)
	want("part", 201, code, out)
	issue := out["stock_issue_id"].(string)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/complete", officer, map[string]any{"work_done": "Idler replaced, track refitted and tensioned", "labour_hours": "6"})
	if code < 400 || code >= 500 {
		t.Fatalf("completed with a part awaiting approval: %d %v", code, out)
	}
	a.call(200, "POST", "/v1/stock-issues/"+issue+"/approve", eng, nil)
	parts := a.call(200, "GET", "/v1/maintenance-work-orders/"+id+"/parts", pm, nil)["items"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["issue_status"] != "APPROVED" || parts[0].(map[string]any)["amount"] != "23500" {
		t.Fatalf("parts (25 at 940): %v", parts)
	}
	code, out = a.do("GET", "/v1/maintenance-work-orders/"+id+"/parts", elsewhere, nil)
	want("parts read from another project", 404, code, out)

	// Completion and certification.
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/complete", eng, map[string]any{"work_done": "Idler replaced, track refitted and tensioned", "labour_hours": "6"})
	want("completed by site staff", 403, code, out)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/complete", officer, map[string]any{"work_done": "ok", "labour_hours": "6"})
	want("empty completion report", 422, code, out)
	a.call(200, "POST", "/v1/maintenance-work-orders/"+id+"/complete", officer, map[string]any{"work_done": "Idler replaced, track refitted and tensioned", "labour_hours": "6"})
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/close", officer2, nil)
	want("closed by plant staff", 403, code, out)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/close", inspector, nil)
	want("closed without a fresh second factor", 401, code, out)
	code, out = a.do("POST", "/v1/maintenance-work-orders/"+id+"/send-back", inspectorMFA, map[string]any{"reason": ""})
	if code < 400 || code >= 500 {
		t.Fatalf("sent back without a reason: %d %v", code, out)
	}
	a.call(200, "POST", "/v1/maintenance-work-orders/"+id+"/send-back", inspectorMFA, map[string]any{"reason": "Track tension out of tolerance on the left"})
	a.call(200, "POST", "/v1/maintenance-work-orders/"+id+"/complete", officer2, map[string]any{"work_done": "Left track re-tensioned to specification", "labour_hours": "1.5"})
	a.call(200, "POST", "/v1/maintenance-work-orders/"+id+"/close", inspectorMFA, nil)
	doc := a.call(200, "GET", "/v1/p2p/maintenance_work_order/"+id, pm, nil)
	if doc["status"] != "CLOSED" {
		t.Fatalf("work order: %v", doc)
	}
	code, out = move(inspectorMFA, "IN_SERVICE", "")
	if code != 200 || out["status"] != "ASSIGNED" {
		t.Fatalf("return to service after the close: %d %v", code, out)
	}

	// History, plans, due list.
	h := a.call(200, "GET", "/v1/equipment/"+eq+"/maintenance", pm, nil)
	if wos := h["work_orders"].([]any); len(wos) != 1 || wos[0].(map[string]any)["parts_cost"] != "23500" || wos[0].(map[string]any)["status"] != "CLOSED" {
		t.Fatalf("history: %v", h)
	}
	plan := map[string]any{"equipment_id": eq, "title": "500 hour service", "every_units": "500", "every_days": 180}
	code, out = a.do("POST", "/v1/maintenance-plans", eng, plan)
	want("plan by site staff", 403, code, out)
	code, out = a.do("POST", "/v1/maintenance-plans", officer, map[string]any{"equipment_id": eq, "title": "No interval"})
	want("plan without an interval", 422, code, out)
	planID := a.id("POST", "/v1/maintenance-plans", officer, plan)
	due := a.call(200, "GET", "/v1/fleet/maintenance-due", pm, nil)["items"].([]any)
	if len(due) != 1 || due[0].(map[string]any)["state"] != "OK" || due[0].(map[string]any)["units_left"] != "500" || due[0].(map[string]any)["plan_id"] != planID {
		t.Fatalf("due: %v", due)
	}
	if l := a.call(200, "GET", "/v1/fleet/maintenance-due", elsewhere, nil)["items"].([]any); len(l) != 0 {
		t.Fatalf("another project sees %d plans", len(l))
	}
	code, out = a.do("GET", "/v1/fleet/maintenance-due", "", nil)
	want("no token", 401, code, out)
	// 0057: the plan is revised (old one retired and kept) and then retired; the policy is a two-person decision.
	code, out = a.do("POST", "/v1/maintenance-plans/"+planID+"/revise", eng, map[string]any{"title": "500 hour service", "every_units": "600"})
	want("plan revised by site staff", 403, code, out)
	rev := a.id("POST", "/v1/maintenance-plans/"+planID+"/revise", officer, map[string]any{"title": "500 hour service", "every_units": "600"})
	due = a.call(200, "GET", "/v1/fleet/maintenance-due", pm, nil)["items"].([]any)
	if len(due) != 1 || due[0].(map[string]any)["plan_id"] != rev || due[0].(map[string]any)["every_units"] != "600" {
		t.Fatalf("due after revision: %v", due)
	}
	code, out = a.do("POST", "/v1/maintenance-plans/"+rev+"/retire", officer, map[string]any{"reason": ""})
	if code < 400 || code >= 500 {
		t.Fatalf("retired without a reason: %d %v", code, out)
	}
	a.call(200, "POST", "/v1/maintenance-plans/"+rev+"/retire", officer, map[string]any{"reason": "Covered by the dealer's service contract"})
	if due = a.call(200, "GET", "/v1/fleet/maintenance-due", pm, nil)["items"].([]any); len(due) != 0 {
		t.Fatalf("a retired plan is still due: %v", due)
	}
	if pol := a.call(200, "GET", "/v1/fleet/policy", pm, nil); pol["source"] != "ENGINEERING_DEFAULT" || pol["note"] == "" {
		t.Fatalf("policy before any is approved: %v", pol)
	}
	policy := map[string]any{"effective_from": day(-1), "fuel_variance_tolerance": "0.05", "fuel_variance_min_litres": "5", "due_soon_fraction": "0.2", "due_soon_days": 14, "basis": "Synthetic policy for the test environment"}
	code, out = a.do("POST", "/v1/fleet-policies", eng, policy)
	want("policy prepared by site staff", 403, code, out)
	code, out = a.do("POST", "/v1/fleet-policies", officer, map[string]any{"effective_from": day(-1), "fuel_variance_tolerance": "2", "basis": "Out of range tolerance"})
	want("policy out of range", 422, code, out)
	polID := a.id("POST", "/v1/fleet-policies", officer, policy)
	a.call(200, "POST", "/v1/fleet-policies/"+polID+"/submit", officer, nil)
	code, out = a.do("POST", "/v1/fleet-policies/"+polID+"/approve", manager, nil)
	want("policy approved without a fresh second factor", 401, code, out)
	code, out = a.do("POST", "/v1/fleet-policies/"+polID+"/approve", inspectorMFA, nil)
	want("policy approved by the inspector", 403, code, out)
	a.call(200, "POST", "/v1/fleet-policies/"+polID+"/approve", managerMFA, nil)
	if pol := a.call(200, "GET", "/v1/fleet/policy", pm, nil); pol["source"] != "APPROVED_POLICY" || pol["policy_id"] != polID || pol["fuel_variance_tolerance"] != "0.05" || pol["due_soon_days"] != float64(14) {
		t.Fatalf("policy in force: %v", pol)
	}
	if pol := a.call(200, "GET", "/v1/fleet/policy?on="+day(-30), pm, nil); pol["source"] != "ENGINEERING_DEFAULT" {
		t.Fatalf("policy a month ago: %v", pol)
	}
	code, out = a.do("GET", "/v1/fleet/policy?on=someday", pm, nil)
	want("bad date", 422, code, out)

	// The work order list is the generic document list.
	if list := a.call(200, "GET", "/v1/p2p/maintenance_work_order", pm, nil)["items"].([]any); len(list) != 1 {
		t.Fatalf("work order list: %v", list)
	}
}
