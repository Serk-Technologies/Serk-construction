// Fleet fuel (FLT-02; migration 0055, ADR 0022).
package kernel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

type fuelWorld struct {
	*stockWorld
	fw     *fleetWorld
	diesel string
}

// newFuelWorld is a project with a site store holding 3,000 L of diesel
// bought through the ordinary purchase chain at 95.00 a litre, a plant team,
// and nothing designated as fuel yet.
func newFuelWorld(t *testing.T) *fuelWorld {
	t.Helper()
	sw := newStockWorld(t)
	f, c := sw.f, sw.cast
	now := time.Now()
	wide := func(id, role string, fresh bool) auth.Principal {
		p := auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
		if fresh {
			p.MFA, p.AuthTime = true, now
		}
		return p
	}
	fw := &fleetWorld{docWorld: &docWorld{p2pWorld: sw.p2pWorld, eng: c.siteEng, pm: c.pm}}
	fw.officer, fw.manager, fw.inspector = wide("u-plant-officer", "plant_officer", false), wide("u-plant-manager", "plant_manager", true), wide("u-plant-inspector", "plant_inspector", true)
	w := &fuelWorld{stockWorld: sw, fw: fw}
	var err error
	w.diesel, err = f.svc.CreateStockItem(f.ctx, c.buyer, StockItemInput{LegalEntityID: f.entity, Code: "DIESEL", Name: "Diesel fuel", Unit: "L", ValuationMethod: "WAC", InventoryAccountID: sw.acct["1200"]})
	must(t, "diesel item", err)
	must(t, "diesel item approve", f.svc.ApproveStockItem(f.ctx, c.finMgr, w.diesel))
	pr, err := f.svc.CreatePR(f.ctx, c.siteEng, f.project, "Diesel for the site tank", nil, []PRLine{{LineNo: 1, CostCodeID: sw.costCode, Description: "Diesel fuel", Unit: "L", Quantity: dec("5000"), EstUnitPrice: dec("95")}})
	must(t, "pr", err)
	must(t, "pr submit", f.svc.SubmitPR(f.ctx, c.siteEng, pr))
	must(t, "pr approve", f.svc.DecidePR(f.ctx, c.pm, pr, true, ""))
	rfq, err := f.svc.CreateRFQ(f.ctx, c.buyer, pr, now.Add(72*time.Hour), 3, "", sw.sup[:])
	must(t, "rfq", err)
	must(t, "rfq issue", f.svc.IssueRFQ(f.ctx, c.buyer, rfq))
	var first string
	for i, price := range []string{"95", "96", "97"} {
		q, err := f.svc.RecordQuote(f.ctx, c.buyer, rfq, sw.sup[i], now.AddDate(0, 1, 0), "SQ-"+key("q"), []QuoteLine{{1, dec(price)}})
		must(t, "quote", err)
		must(t, "eval", f.svc.EvaluateQuote(f.ctx, c.siteEng, q, true, "Depot certificate provided"))
		if i == 0 {
			first = q
		}
	}
	must(t, "award", f.svc.AwardRFQ(f.ctx, c.procMgr, rfq, first, ""))
	po, err := f.svc.CreatePOFromAwardItems(f.ctx, c.buyer, rfq, map[int]string{1: sw.acct["1200"]}, map[int]string{1: w.diesel})
	must(t, "po", err)
	must(t, "po approve", f.svc.ApprovePO(f.ctx, c.procMgr, po, ""))
	grn, jg := sw.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("3000")})
	sw.financePost(t, jg)
	_, err = f.svc.PutAway(f.ctx, c.store, grn, 1, w.diesel, sw.store, dec("3000"))
	must(t, "put-away", err)
	return w
}

// working returns a machine operating at the first project with its meter at 1000.
func (w *fuelWorld) working(t *testing.T, serial string) string {
	t.Helper()
	f := w.f
	eq := w.fw.machine(t, serial)
	w.fw.reserve(t, eq, f.project, w.cast.siteEng, 0, 30)
	for _, step := range [][2]string{{"MOBILIZING", f.project}, {"ASSIGNED", ""}, {"OPERATING", ""}} {
		by := w.cast.siteEng
		if step[0] == "MOBILIZING" {
			by = w.fw.officer
		}
		if _, err := f.svc.MoveEquipment(f.ctx, by, eq, step[0], "Start of work", step[1]); err != nil {
			t.Fatalf("%s: %v", step[0], err)
		}
	}
	return eq
}

func (w *fuelWorld) fill(eq, litres, meter string) FuelDispenseInput {
	m := dec(meter)
	return FuelDispenseInput{EquipmentID: eq, WarehouseID: w.store, StockItemID: w.diesel, CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"],
		Litres: dec(litres), MeterReading: &m, Operator: "op-abebe", DispensedOn: today(), Note: "Morning fill"}
}

// TestFuel_DispenseIsAStockIssue: filling a machine is prepared as a stock
// issue of a designated fuel item and takes effect (stock, value, journal,
// project cost) only when a second person approves that issue. There is no
// second ledger: the tank is the warehouse balance.
func TestFuel_DispenseIsAStockIssue(t *testing.T) {
	w := newFuelWorld(t)
	f, c := w.f, w.cast
	eq := w.working(t, "F-100")

	// Nothing is fuel until a person with fleet.approve says so.
	if _, err := f.svc.DispenseFuel(f.ctx, c.store, w.fill(eq, "120", "1000")); err == nil {
		t.Fatal("an item that is not designated as fuel was dispensed into a machine")
	}
	if err := f.svc.DesignateFuelItem(f.ctx, w.fw.officer, w.diesel); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("fuel item designated without fleet.approve: %v", err)
	}
	pending, err := f.svc.CreateStockItem(f.ctx, c.buyer, StockItemInput{LegalEntityID: f.entity, Code: "PETROL", Name: "Petrol", Unit: "L", ValuationMethod: "WAC", InventoryAccountID: w.acct["1200"]})
	must(t, "pending item", err)
	if err := f.svc.DesignateFuelItem(f.ctx, w.fw.manager, pending); err == nil {
		t.Fatal("a stock item that is not yet approved was designated as fuel")
	}
	must(t, "designate diesel", f.svc.DesignateFuelItem(f.ctx, w.fw.manager, w.diesel))
	if err := f.svc.DesignateFuelItem(f.ctx, w.fw.manager, w.diesel); err == nil {
		t.Fatal("the same item was designated twice")
	}
	if items, err := f.svc.ListFuelItems(f.ctx, c.store); err != nil || len(items) != 1 || items[0].Code != "DIESEL" {
		t.Fatalf("fuel items: %+v %v", items, err)
	}

	// What a dispense must say, and who may prepare it.
	yard := w.fw.machine(t, "F-YARD")
	for name, mut := range map[string]func(*FuelDispenseInput){
		"no litres":                     func(d *FuelDispenseInput) { d.Litres = dec("0") },
		"negative litres":               func(d *FuelDispenseInput) { d.Litres = dec("-5") },
		"no operator":                   func(d *FuelDispenseInput) { d.Operator = " " },
		"no meter reading":              func(d *FuelDispenseInput) { d.MeterReading = nil },
		"a meter reading behind":        func(d *FuelDispenseInput) { m := dec("900"); d.MeterReading = &m },
		"a future date":                 func(d *FuelDispenseInput) { d.DispensedOn = today().AddDate(0, 0, 2) },
		"a machine that is in the yard": func(d *FuelDispenseInput) { d.EquipmentID = yard },
		"an item that is not fuel":      func(d *FuelDispenseInput) { d.StockItemID = w.itemSteel },
		"a machine that does not exist": func(d *FuelDispenseInput) { d.EquipmentID = "00000000-0000-4000-8000-00000000dead" },
		"a tank that does not exist":    func(d *FuelDispenseInput) { d.WarehouseID = "00000000-0000-4000-8000-00000000dead" },
	} {
		d := w.fill(eq, "120", "1000")
		mut(&d)
		if _, err := f.svc.DispenseFuel(f.ctx, c.store, d); err == nil {
			t.Fatalf("a dispense with %s was accepted", name)
		}
	}
	if _, err := f.svc.DispenseFuel(f.ctx, w.fw.manager, w.fill(eq, "120", "1000")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a dispense by someone without stock.issue: %v", err)
	}
	if n := w.stockValue(t, `SELECT count(*) FROM stock_issues WHERE stock_item_id = $1::uuid`, w.diesel); !n.IsZero() {
		t.Fatalf("refused dispenses left %s stock issues behind", n)
	}

	// Prepared: a draft issue and the machine it is for. Nothing has moved.
	d, err := f.svc.DispenseFuel(f.ctx, c.store, w.fill(eq, "120", "1000"))
	must(t, "dispense", err)
	if d.IssueStatus != "DRAFT" || d.Amount != nil || !d.Litres.Equal(dec("120")) || d.EquipmentID != eq || d.ProjectID != f.project {
		t.Fatalf("prepared dispense: %+v", d)
	}
	if q, _ := w.balanceOf(t, w.diesel); !q.Equal(dec("3000")) {
		t.Fatalf("the tank changed before approval: %s L", q)
	}
	rec, err := f.svc.FuelReconciliationOf(f.ctx, c.pm, dayOff(-7), dayOff(0), "")
	must(t, "reconciliation", err)
	if len(rec.Lines) != 1 || !rec.Lines[0].Pending.Equal(dec("120")) || !rec.Lines[0].Dispensed.IsZero() || !rec.Lines[0].Cost.IsZero() {
		t.Fatalf("before approval: %+v", rec.Lines)
	}

	// Approval is the existing stock-issue rule: another person, and then the ledger does the rest.
	if _, err := f.svc.ApproveStockIssue(f.ctx, auth.Principal{Kind: auth.KindHuman, UserID: c.store.UserID, TenantID: f.tenant, Roles: []string{"storekeeper", "site_engineer"}, ProjectIDs: []string{f.project}, IssuedAt: time.Now().Add(-time.Minute)}, d.StockIssueID); err == nil {
		t.Fatal("the person who prepared a fuel dispense approved it")
	}
	journal, err := f.svc.ApproveStockIssue(f.ctx, c.siteEng, d.StockIssueID)
	must(t, "approve the fuel issue", err)
	w.financePost(t, journal)
	if q, v := w.balanceOf(t, w.diesel); !q.Equal(dec("2880")) || !v.Equal(dec("273600")) {
		t.Fatalf("after approval the tank holds %s L worth %s, want 2880 / 273600", q, v)
	}
	list, err := f.svc.ListFuelDispenses(f.ctx, c.pm, eq)
	must(t, "list", err)
	if len(list) != 1 || list[0].IssueStatus != "APPROVED" || list[0].Amount == nil || !list[0].Amount.Equal(dec("11400")) {
		t.Fatalf("approved dispense: %+v", list)
	}
	// The cost is on the project through the ordinary issue journal, posted and balanced.
	var debit, lines string
	must(t, "journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(sum(amount) FILTER (WHERE l.side = 'DEBIT' AND l.project_id = $2::uuid), 0)::text, count(*)::text
			FROM journal_lines l JOIN journals j ON j.id = l.journal_id WHERE j.id = $1::uuid AND j.status = 'POSTED'`, journal, f.project).Scan(&debit, &lines)
	}))
	if !dec(debit).Equal(dec("11400")) || lines != "2" {
		t.Fatalf("fuel journal: %s debit to the project in %s lines, want 11400 in 2", debit, lines)
	}

	// More than the tank holds: prepared, but approval is refused by the stock ledger and nothing moves.
	big, err := f.svc.DispenseFuel(f.ctx, c.store, w.fill(eq, "50000", "1001"))
	must(t, "oversized dispense", err)
	if _, err := f.svc.ApproveStockIssue(f.ctx, c.siteEng, big.StockIssueID); err == nil {
		t.Fatal("more fuel than the tank holds was issued")
	}
	if q, _ := w.balanceOf(t, w.diesel); !q.Equal(dec("2880")) {
		t.Fatalf("a refused approval moved stock: %s L", q)
	}
	must(t, "cancel oversized", f.svc.CancelStockIssue(f.ctx, c.store, big.StockIssueID, "Entered in error"))

	// Offline: the same reference records once, however many times it arrives.
	off := w.fill(eq, "40", "1002")
	off.ClientRef = "bowser3-0000000117"
	one, err := f.svc.DispenseFuel(f.ctx, c.store, off)
	must(t, "offline dispense", err)
	var wg sync.WaitGroup
	got := make([]string, 6)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if r, err := f.svc.DispenseFuel(f.ctx, c.store, off); err == nil {
				got[i] = r.ID
			}
		}(i)
	}
	wg.Wait()
	for _, id := range got {
		if id != "" && id != one.ID {
			t.Fatalf("an offline repeat produced another dispense %s (first %s)", id, one.ID)
		}
	}
	if n := w.stockValue(t, `SELECT count(*) FROM fuel_dispenses WHERE client_ref = 'bowser3-0000000117'`); !n.Equal(dec("1")) {
		t.Fatalf("%s dispenses for one offline reference", n)
	}
	if n := w.stockValue(t, `SELECT count(*) FROM stock_issues WHERE stock_item_id = $1::uuid AND status = 'DRAFT'`, w.diesel); !n.Equal(dec("1")) {
		t.Fatalf("%s draft issues after the offline repeats, want 1", n)
	}

	// A tenant database login cannot write around the kernel.
	for name, sql := range map[string]string{
		"a dispense for an approved issue":   `INSERT INTO fuel_dispenses (stock_issue_id, equipment_id, project_id, dispensed_on, litres, meter_serial, meter_reading, operator, created_by) SELECT i.id, '` + eq + `', i.project_id, i.issue_date, i.quantity, 1, 1005, 'op-x', i.created_by FROM stock_issues i WHERE i.id = '` + d.StockIssueID + `'`,
		"a second dispense for a draft":      `INSERT INTO fuel_dispenses (stock_issue_id, equipment_id, project_id, dispensed_on, litres, meter_serial, meter_reading, operator, created_by) SELECT i.id, '` + eq + `', i.project_id, i.issue_date, i.quantity, 1, 1005, 'op-x', i.created_by FROM stock_issues i WHERE i.id = '` + one.StockIssueID + `'`,
		"more litres on a dispense":          `UPDATE fuel_dispenses SET litres = 1 WHERE id = '` + d.ID + `'`,
		"another machine on a dispense":      `UPDATE fuel_dispenses SET equipment_id = '` + yard + `' WHERE id = '` + d.ID + `'`,
		"removing a dispense":                `DELETE FROM fuel_dispenses WHERE id = '` + d.ID + `'`,
		"a fuel item without an attestation": `INSERT INTO fuel_items (stock_item_id, designated_by) VALUES ('` + w.itemSteel + `', 'u-plant-manager')`,
		"removing a fuel item":               `DELETE FROM fuel_items WHERE stock_item_id = '` + w.diesel + `'`,
		"a standard without an attestation":  `INSERT INTO equipment_fuel_standards (equipment_id, litres_per_unit, effective_from, basis, set_by) VALUES ('` + eq + `', 1, current_date, 'made up figure', 'u-plant-manager')`,
	} {
		if err := w.rawExec(sql); err == nil {
			t.Errorf("OPEN: raw SQL as the tenant login: %s", name)
		}
	}
	// Another tenant sees nothing.
	if l, err := f.svc.ListFuelDispenses(f.ctx, f.outsider, ""); err != nil || len(l) != 0 {
		t.Fatalf("another tenant lists %d dispenses: %v", len(l), err)
	}
	if r, err := f.svc.FuelReconciliationOf(f.ctx, f.outsider, dayOff(-7), dayOff(0), ""); err != nil || len(r.Lines) != 0 {
		t.Fatalf("another tenant reconciles %d machines: %v", len(r.Lines), err)
	}
	if _, err := f.svc.DispenseFuel(f.ctx, f.outsider, w.fill(eq, "10", "1003")); err == nil {
		t.Fatal("another tenant dispensed into this tenant's machine")
	}
}

// TestFuel_StandardsAndReconciliation: dispensed, logged and expected fuel are
// compared per machine; a difference is listed with both figures for a person
// to explain. Standards are set by fleet.approve, effective-dated, never edited.
func TestFuel_StandardsAndReconciliation(t *testing.T) {
	w := newFuelWorld(t)
	f, c := w.f, w.cast
	eq := w.working(t, "F-200")
	must(t, "designate diesel", f.svc.DesignateFuelItem(f.ctx, w.fw.manager, w.diesel))
	w.fw.authorise(t, "op-abebe")

	std := FuelStandardInput{EquipmentID: eq, LitresPerUnit: dec("14"), EffectiveFrom: dayOff(-60), Basis: "Maker's data sheet, medium duty"}
	if _, err := f.svc.SetFuelStandard(f.ctx, w.fw.officer, std); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a standard set without fleet.approve: %v", err)
	}
	for name, mut := range map[string]func(*FuelStandardInput){
		"a zero rate":     func(s *FuelStandardInput) { s.LitresPerUnit = dec("0") },
		"no basis":        func(s *FuelStandardInput) { s.Basis = "x" },
		"a bad date":      func(s *FuelStandardInput) { s.EffectiveFrom = "yesterday" },
		"no such machine": func(s *FuelStandardInput) { s.EquipmentID = "00000000-0000-4000-8000-00000000dead" },
	} {
		s := std
		mut(&s)
		if _, err := f.svc.SetFuelStandard(f.ctx, w.fw.manager, s); err == nil {
			t.Fatalf("a standard with %s was accepted", name)
		}
	}
	_, err := f.svc.SetFuelStandard(f.ctx, w.fw.manager, std)
	must(t, "standard", err)
	if _, err := f.svc.SetFuelStandard(f.ctx, w.fw.manager, std); err == nil {
		t.Fatal("a second standard for the same day replaced the first")
	}
	if err := w.rawExec(`UPDATE equipment_fuel_standards SET litres_per_unit = 99 WHERE equipment_id = $1::uuid`, eq); err == nil {
		t.Error("OPEN: a recorded standard was edited by raw SQL")
	}

	// 120 L into the machine (approved); its approved log says 96 L over 8 hours.
	d, err := f.svc.DispenseFuel(f.ctx, c.store, w.fill(eq, "120", "1000"))
	must(t, "dispense", err)
	_, err = f.svc.ApproveStockIssue(f.ctx, c.siteEng, d.StockIssueID)
	must(t, "approve issue", err)
	log, err := f.svc.CreateEquipmentLog(f.ctx, c.siteEng, EquipmentLogInput{EquipmentID: eq, ProjectID: f.project, LogDate: dayOff(0), Operator: "op-abebe", MeterOpen: dec("1000"), MeterClose: dec("1008"),
		ProductiveHours: dec("6.5"), IdleHours: dec("1.5"), FuelLitres: dec("96"), CostCodeID: w.costCode})
	must(t, "log", err)
	must(t, "submit log", f.svc.SubmitEquipmentLog(f.ctx, c.siteEng, log))
	must(t, "approve log", f.svc.DecideEquipmentLog(f.ctx, c.pm, log, true, ""))

	rec, err := f.svc.FuelReconciliationOf(f.ctx, c.pm, dayOff(-7), dayOff(0), f.project)
	must(t, "reconciliation", err)
	if len(rec.Lines) != 1 {
		t.Fatalf("lines: %+v", rec.Lines)
	}
	l := rec.Lines[0]
	if !l.Dispensed.Equal(dec("120")) || !l.Logged.Equal(dec("96")) || !l.MeteredUnits.Equal(dec("8")) || l.Expected == nil || !l.Expected.Equal(dec("112")) ||
		!l.Cost.Equal(dec("11400")) || l.ActualPerUnit == nil || !l.ActualPerUnit.Equal(dec("15")) || !l.DispensedLogged.Equal(dec("24")) {
		t.Fatalf("figures: %+v", l)
	}
	// 24 L dispensed and not logged is over the tolerance; 8 L over the standard is inside it.
	if len(l.Flags) != 1 || l.Flags[0].Code != "DISPENSED_NOT_LOGGED" || rec.Flagged != 1 || rec.Policy.Source != "ENGINEERING_DEFAULT" || rec.Policy.FuelVarianceTolerance != "0.1" || rec.Policy.Note == "" || rec.Basis == "" {
		t.Fatalf("flags: %+v (flagged %d, policy %+v)", l.Flags, rec.Flagged, rec.Policy)
	}
	// A later standard applies from its date; the earlier one is kept.
	_, err = f.svc.SetFuelStandard(f.ctx, w.fw.manager, FuelStandardInput{EquipmentID: eq, LitresPerUnit: dec("10"), EffectiveFrom: dayOff(-1), Basis: "Measured trial on site, 3 shifts"})
	must(t, "second standard", err)
	rec, err = f.svc.FuelReconciliationOf(f.ctx, c.pm, dayOff(-7), dayOff(0), "")
	must(t, "reconciliation 2", err)
	if l := rec.Lines[0]; l.Expected == nil || !l.Expected.Equal(dec("80")) || len(l.Flags) != 2 {
		t.Fatalf("with the new standard: expected %v, flags %+v", l.Expected, l.Flags)
	}
	if n := w.stockValue(t, `SELECT count(*) FROM equipment_fuel_standards WHERE equipment_id = $1::uuid`, eq); !n.Equal(dec("2")) {
		t.Fatalf("%s standards kept, want 2", n)
	}

	// Scope and input.
	elsewhere := auth.Principal{UserID: "u-eng-2", TenantID: f.tenant, Roles: []string{"site_engineer", "project_manager"}, ProjectIDs: []string{"00000000-0000-4000-8000-000000000002"}, IssuedAt: time.Now().Add(-time.Minute), Kind: auth.KindHuman}
	if r, err := f.svc.FuelReconciliationOf(f.ctx, elsewhere, dayOff(-7), dayOff(0), ""); err != nil || len(r.Lines) != 0 {
		t.Fatalf("someone on another project sees %d machines: %v", len(r.Lines), err)
	}
	if _, err := f.svc.FuelReconciliationOf(f.ctx, elsewhere, dayOff(-7), dayOff(0), f.project); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone on another project asked for this project: %v", err)
	}
	if l, err := f.svc.ListFuelDispenses(f.ctx, elsewhere, eq); err != nil || len(l) != 0 {
		t.Fatalf("someone on another project lists %d dispenses: %v", len(l), err)
	}
	if _, err := f.svc.FuelReconciliationOf(f.ctx, c.pm, dayOff(0), dayOff(-7), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a period that ends before it starts: %v", err)
	}
	if _, err := f.svc.FuelReconciliationOf(f.ctx, c.pm, dayOff(-500), dayOff(0), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a period of more than a year: %v", err)
	}
}
