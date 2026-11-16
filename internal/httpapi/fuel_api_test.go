package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

// TestAPIFuel drives fleet fuel (0055, ADR 0022) over HTTP: designation,
// standards, dispensing as a stock issue approved by a second person, and the
// reconciliation, with the status codes a client relies on.
// buyDiesel buys 300 L of diesel at 940 through the ordinary chain (request,
// three quotations, award, order naming the stock item, receipt) and returns
// the confirmed goods receipt.
func (a *p2pAPI) buyDiesel(t *testing.T, diesel string) string {
	t.Helper()
	ctx, svc := a.db.Ctx, a.svc
	mk := func(id string, roles ...string) auth.Principal {
		return auth.Principal{Kind: auth.KindHuman, UserID: id, TenantID: a.tenant, Roles: roles, ProjectIDs: []string{a.project}, IssuedAt: time.Now().Add(-time.Minute), MFA: true, AuthTime: time.Now()}
	}
	buyer, pmgr, se, pm, store := mk("s-buyer", "procurement_officer"), mk("s-pmgr", "procurement_manager"), mk("s-se", "site_engineer"), mk("s-pm", "project_manager"), mk("s-store", "storekeeper")
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	var sup []string
	must("suppliers", a.db.AdminStore.WithTenant(ctx, a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM suppliers WHERE status = 'ACTIVE' AND legal_entity_id = $1::uuid ORDER BY code LIMIT 3`, a.entity)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			sup = append(sup, id)
		}
		return rows.Err()
	}))
	d := decimal.RequireFromString
	pr, err := svc.CreatePR(ctx, se, a.project, "Diesel for the site tank", nil, []kernel.PRLine{{LineNo: 1, CostCodeID: a.costCode, Description: "Diesel fuel", Unit: "L", Quantity: d("300"), EstUnitPrice: d("950")}})
	must("pr", err)
	must("pr submit", svc.SubmitPR(ctx, se, pr))
	must("pr approve", svc.DecidePR(ctx, pm, pr, true, ""))
	rfq, err := svc.CreateRFQ(ctx, buyer, pr, time.Now().Add(72*time.Hour), 3, "", sup)
	must("rfq", err)
	must("rfq issue", svc.IssueRFQ(ctx, buyer, rfq))
	var q0 string
	for i, price := range []string{"940", "950", "960"} {
		q, err := svc.RecordQuote(ctx, buyer, rfq, sup[i], time.Now().AddDate(0, 1, 0), "DQ-"+uuid.NewString()[:6], []kernel.QuoteLine{{PRLineNo: 1, UnitPrice: d(price)}})
		must("quote", err)
		must("eval", svc.EvaluateQuote(ctx, se, q, true, "Depot certificate provided"))
		if i == 0 {
			q0 = q
		}
	}
	must("award", svc.AwardRFQ(ctx, pmgr, rfq, q0, ""))
	po, err := svc.CreatePOFromAwardItems(ctx, buyer, rfq, map[int]string{1: a.accounts["1200"]}, map[int]string{1: diesel})
	must("po", err)
	must("po approve", svc.ApprovePO(ctx, pmgr, po, ""))
	grn, err := svc.CreateGRN(ctx, store, po, time.Now().UTC(), "DN-D-1", []kernel.GRNLine{{POLineNo: 1, QtyReceived: d("300")}})
	must("grn", err)
	must("grn submit", svc.SubmitGRN(ctx, store, grn))
	_, err = svc.ConfirmGRN(ctx, se, grn)
	must("grn confirm", err)
	return grn
}

func TestAPIFuel(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	if err := a.db.AdminStore.WithTenant(a.db.Ctx, a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: a.tenant, LegalEntityID: a.entity, Code: "1200", Name: "Inventory", Type: "ASSET", Active: true})
		a.accounts["1200"] = id
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a.seedInventoryGRN(t) // posting policy, approved suppliers and the project budget
	buyer, finMgr := a.tok("u-buyer", P, false, "procurement_officer"), a.tok("u-finance-manager", nil, true, "finance_manager")
	procMgr, store := a.tok("u-proc-manager", P, false, "procurement_manager"), a.tok("u-storekeeper", P, false, "storekeeper")
	eng, pm := a.tok("u-fu-eng", P, false, "site_engineer"), a.tok("u-fu-pm", P, false, "project_manager")
	officer, managerMFA, manager := a.tok("u-fu-officer", nil, false, "plant_officer"), a.tok("u-fu-manager", nil, true, "plant_manager"), a.tok("u-fu-manager", nil, false, "plant_manager")
	elsewhere := a.tok("u-fu-else", []string{uuid.NewString()}, false, "site_engineer", "project_manager", "storekeeper")
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }

	diesel := a.id("POST", "/v1/stock-items", buyer, map[string]any{"legal_entity_id": a.entity, "code": "DIESEL", "name": "Diesel fuel", "unit": "L", "valuation_method": "WAC", "inventory_account_id": a.accounts["1200"]})
	a.call(200, "POST", "/v1/stock-items/"+diesel+"/approve", finMgr, nil)
	tank := a.id("POST", "/v1/warehouses", procMgr, map[string]any{"legal_entity_id": a.entity, "project_id": a.project, "code": "P-TANK", "name": "Site fuel tank"})
	grn := a.buyDiesel(t, diesel)
	a.call(201, "POST", "/v1/goods-receipts/"+grn+"/put-away", store, map[string]any{"po_line_no": 1, "stock_item_id": diesel, "warehouse_id": tank, "quantity": "300"})

	// A machine working at the project.
	eq := a.id("POST", "/v1/equipment", officer, map[string]any{"legal_entity_id": a.entity, "name": "Loader 07", "class": "LOADER", "ownership": "OWNED", "manufacturer": "CAT", "model": "950H", "serial_number": "CAT950H-7007", "opening_meter": "2000"})
	move := func(tok, to, project string) {
		t.Helper()
		a.call(200, "POST", "/v1/equipment/"+eq+"/move", tok, map[string]any{"to": to, "reason": "Fuel test set-up", "project_id": project})
	}
	move(managerMFA, "AVAILABLE", "")
	res := a.id("POST", "/v1/equipment-reservations", eng, map[string]any{"equipment_id": eq, "project_id": a.project, "purpose": "Loading", "starts_on": day(0), "ends_on": day(10)})
	a.call(200, "POST", "/v1/equipment-reservations/"+res+"/submit", eng, nil)
	a.call(200, "POST", "/v1/equipment-reservations/"+res+"/approve", managerMFA, nil)
	move(officer, "MOBILIZING", a.project)
	move(eng, "ASSIGNED", "")
	move(eng, "OPERATING", "")

	fill := func(litres, meter string) map[string]any {
		return map[string]any{"equipment_id": eq, "warehouse_id": tank, "stock_item_id": diesel, "cost_code_id": a.costCode, "expense_account_id": a.accounts["5000"],
			"litres": litres, "meter_reading": meter, "operator": "op-kebede", "dispensed_on": day(0)}
	}
	want := func(what string, want, code int, out map[string]any) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d %v, want %d", what, code, out, want)
		}
	}

	// Not fuel until designated; designation needs fleet.approve.
	code, out := a.do("POST", "/v1/fleet/fuel-dispenses", store, fill("60", "2000"))
	want("dispense of an undesignated item", 422, code, out)
	code, out = a.do("POST", "/v1/fleet/fuel-items", officer, map[string]any{"stock_item_id": diesel})
	want("designation by the plant officer", 403, code, out)
	code, out = a.do("POST", "/v1/fleet/fuel-items", store, map[string]any{"stock_item_id": "nope"})
	want("malformed id", 422, code, out)
	a.call(200, "POST", "/v1/fleet/fuel-items", manager, map[string]any{"stock_item_id": diesel})
	if items := a.call(200, "GET", "/v1/fleet/fuel-items", store, nil)["items"].([]any); len(items) != 1 {
		t.Fatalf("fuel items: %v", items)
	}

	// Dispense: who, what, and once per offline reference.
	code, out = a.do("POST", "/v1/fleet/fuel-dispenses", manager, fill("60", "2000"))
	want("dispense without stock.issue", 403, code, out)
	code, out = a.do("POST", "/v1/fleet/fuel-dispenses", elsewhere, fill("60", "2000"))
	want("dispense by a storekeeper of another project", 404, code, out)
	code, out = a.do("POST", "/v1/fleet/fuel-dispenses", store, fill("0", "2000"))
	want("zero litres", 422, code, out)
	code, out = a.do("POST", "/v1/fleet/fuel-dispenses", store, fill("60", "1500"))
	want("meter behind (refused by the database)", 409, code, out)
	body := fill("60", "2000")
	body["client_ref"] = "bowser1-0000000009"
	code, out = a.do("POST", "/v1/fleet/fuel-dispenses", store, body)
	want("dispense", 201, code, out)
	dispense, issue := out["id"].(string), out["stock_issue_id"].(string)
	if out["issue_status"] != "DRAFT" || out["amount"] != nil {
		t.Fatalf("prepared dispense: %v", out)
	}
	code, out = a.do("POST", "/v1/fleet/fuel-dispenses", store, body)
	if code != 200 || out["id"] != dispense || out["already_recorded"] != true {
		t.Fatalf("repeat of an offline reference: %d %v", code, out)
	}

	// Approval is the stock issue's: not its preparer, and the tank must hold it.
	code, out = a.do("POST", "/v1/stock-issues/"+issue+"/approve", store, nil)
	if code < 400 || code >= 500 {
		t.Fatalf("the preparer approved the fuel issue: %d %v", code, out)
	}
	a.call(200, "POST", "/v1/stock-issues/"+issue+"/approve", eng, nil)
	list := a.call(200, "GET", "/v1/fleet/fuel-dispenses?equipment="+eq, pm, nil)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["issue_status"] != "APPROVED" || list[0].(map[string]any)["amount"] != "56400" {
		t.Fatalf("approved dispense (60 L at 940): %v", list)
	}
	if l := a.call(200, "GET", "/v1/fleet/fuel-dispenses", elsewhere, nil)["items"].([]any); len(l) != 0 {
		t.Fatalf("another project lists %d dispenses", len(l))
	}

	// Standards and reconciliation.
	std := map[string]any{"equipment_id": eq, "litres_per_unit": "12.5", "effective_from": day(-30), "basis": "Maker's data sheet"}
	code, out = a.do("POST", "/v1/fleet/fuel-standards", officer, std)
	want("standard by the plant officer", 403, code, out)
	a.call(201, "POST", "/v1/fleet/fuel-standards", manager, std)
	code, out = a.do("POST", "/v1/fleet/fuel-standards", manager, std)
	if code < 400 || code >= 500 {
		t.Fatalf("a second standard for the same day: %d %v", code, out)
	}
	rec := a.call(200, "GET", "/v1/fleet/fuel-reconciliation?from="+day(-7)+"&to="+day(0), pm, nil)
	lines := rec["lines"].([]any)
	if pol := rec["policy"].(map[string]any); len(lines) != 1 || pol["source"] != "ENGINEERING_DEFAULT" || pol["fuel_variance_tolerance"] != "0.1" || rec["basis"] == "" {
		t.Fatalf("reconciliation: %v", rec)
	}
	l := lines[0].(map[string]any)
	// 60 L dispensed, nothing logged yet: one question (dispensed, not logged) and one about meter movement.
	if l["dispensed_litres"] != "60" || l["logged_litres"] != "0" || l["fuel_cost"] != "56400" || l["standard_litres_per_unit"] != "12.5" || len(l["flags"].([]any)) != 2 {
		t.Fatalf("line: %v", l)
	}
	code, out = a.do("GET", "/v1/fleet/fuel-reconciliation?from="+day(-7)+"&to="+day(0)+"&project="+a.project, elsewhere, nil)
	want("reconciliation of a project out of scope", 404, code, out)
	code, out = a.do("GET", "/v1/fleet/fuel-reconciliation?from=x&to=y", pm, nil)
	want("bad dates", 422, code, out)
	code, out = a.do("GET", "/v1/fleet/fuel-reconciliation?from="+day(-7)+"&to="+day(0), "", nil)
	want("no token", 401, code, out)
}
