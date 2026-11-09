package kernel

// Tests for guarantees and price adjustment (WP-2.7, migration 0050,
// kernel/bonds.go). Proven: who may register, change, release and call a
// guarantee; its value only goes down and its expiry only later, each with a
// reason; release and call are attested decisions by another person with a
// fresh MFA, and a closed guarantee never changes; the register's standing
// and advance cover. Index values are approved by a second person, never in
// the future, once per month, and never change; a formula adds up to
// exactly 1 and needs approved base values; the database computes the factor
// Pn = a + Σ b·(Ln/Lo) and the adjustment from approved values only, for a
// certified certificate, once, and recomputes it at approval.

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestBonds_GuaranteeRegister(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	w.activeContract(t)
	day := func(offset int) string { return time.Now().AddDate(0, 0, offset).Format("2006-01-02") }
	treasury, commercial, qs := c.treasury, c.commercial, w.ar.qs

	adv := GuaranteeInput{ContractID: w.contract, Kind: "advance", Issuer: "Commercial Bank of Ethiopia", Reference: "CBE/APG/2026/0091",
		Amount: dec("1000000"), IssuedOn: day(-10), ExpiresOn: day(20)}
	if _, err := f.svc.RegisterGuarantee(f.ctx, qs, adv); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a quantity surveyor registered a guarantee: %v", err)
	}
	bad := adv
	bad.IssuedOn = day(2)
	bad.ExpiresOn = day(40)
	if _, err := f.svc.RegisterGuarantee(f.ctx, treasury, bad); err == nil {
		t.Fatal("a guarantee issued in the future was accepted")
	}
	bad = adv
	bad.ExpiresOn = day(-11)
	if _, err := f.svc.RegisterGuarantee(f.ctx, treasury, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an expiry before issue was accepted: %v", err)
	}
	advID, err := f.svc.RegisterGuarantee(f.ctx, treasury, adv)
	must(t, "advance guarantee", err)
	if _, err := f.svc.RegisterGuarantee(f.ctx, treasury, adv); err == nil {
		t.Fatal("the same bank reference was registered twice")
	}
	perf, err := f.svc.RegisterGuarantee(f.ctx, treasury, GuaranteeInput{ContractID: w.contract, Kind: "PERFORMANCE", Issuer: "Awash Bank", Reference: "AB/PB/77120",
		Amount: dec("2306000"), IssuedOn: day(-10), ExpiresOn: day(365)})
	must(t, "performance guarantee", err)

	reg, err := f.svc.Guarantees(f.ctx, commercial, w.contract)
	must(t, "register", err)
	byID := map[string]Guarantee{}
	for _, g := range reg.Guarantees {
		byID[g.ID] = g
	}
	if byID[advID].Standing != "EXPIRING" || byID[advID].DaysLeft != 20 || byID[perf].Standing != "ACTIVE" || byID[advID].Currency != "ETB" {
		t.Fatalf("standing: %+v %+v", byID[advID], byID[perf])
	}
	if reg.Totals["expiring"] != 1 || reg.Totals["active"] != 2 {
		t.Fatalf("totals: %v", reg.Totals)
	}
	if len(reg.Cover) != 1 || !reg.Cover[0].Cover.Equal(dec("1000000")) || !reg.Cover[0].Shortfall.IsZero() {
		t.Fatalf("advance cover: %+v", reg.Cover)
	}

	// Reduce and extend: down and later only, with a reason.
	less, more := dec("800000"), dec("1200000")
	if err := f.svc.ChangeGuarantee(f.ctx, treasury, advID, GuaranteeChange{Amount: &less}); err == nil {
		t.Fatal("a reduction without a reason was accepted")
	}
	if err := f.svc.ChangeGuarantee(f.ctx, treasury, advID, GuaranteeChange{Amount: &more, Reason: "bank increased it"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an increase was accepted: %v", err)
	}
	if err := f.svc.ChangeGuarantee(f.ctx, treasury, advID, GuaranteeChange{ExpiresOn: day(10), Reason: "shorter cover"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a shorter expiry was accepted: %v", err)
	}
	must(t, "reduce", f.svc.ChangeGuarantee(f.ctx, treasury, advID, GuaranteeChange{Amount: &less, Reason: "Advance partly recovered in IPC 1"}))
	must(t, "extend", f.svc.ChangeGuarantee(f.ctx, treasury, advID, GuaranteeChange{ExpiresOn: day(200), Reason: "Bank amendment no. 1"}))
	rawFails(t, f, "raise a guarantee's value", `UPDATE guarantees SET amount = 900000, change_reason = 'raw raise' WHERE id = '`+advID+`'`)
	rawFails(t, f, "shorten a guarantee", `UPDATE guarantees SET expires_on = current_date + 5, change_reason = 'raw shorten' WHERE id = '`+advID+`'`)
	rawFails(t, f, "change the issuer", `UPDATE guarantees SET reference = 'OTHER', change_reason = 'raw edit' WHERE id = '`+advID+`'`)
	rawFails(t, f, "release without an attestation", `UPDATE guarantees SET status = 'RELEASED', closed_by = 'u-commercial', closed_at = now(), close_reference = 'x1', close_reason = 'raw release' WHERE id = '`+perf+`'`)

	// Release and call: bond.approve, another person, fresh MFA.
	rel := GuaranteeClose{Outcome: "released", Reference: "CTR/REL/01", Reason: "Taking-over certificate issued"}
	if err := f.svc.CloseGuarantee(f.ctx, w.mfa(treasury), perf, rel); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("treasury released a guarantee: %v", err)
	}
	if err := f.svc.CloseGuarantee(f.ctx, commercial, perf, rel); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("a release without fresh MFA: %v", err)
	}
	both := auth.Principal{Kind: auth.KindHuman, UserID: "u-treasury", TenantID: f.tenant, Roles: []string{"commercial_manager"}, ProjectIDs: []string{f.project}}
	if err := f.svc.CloseGuarantee(f.ctx, w.mfa(both), perf, rel); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the registrar released their own guarantee: %v", err)
	}
	must(t, "release", f.svc.CloseGuarantee(f.ctx, w.mfa(commercial), perf, rel))
	if err := f.svc.ChangeGuarantee(f.ctx, treasury, perf, GuaranteeChange{ExpiresOn: day(400), Reason: "after release"}); err == nil {
		t.Fatal("a released guarantee was extended")
	}
	over := dec("900000")
	if err := f.svc.CloseGuarantee(f.ctx, w.mfa(commercial), advID, GuaranteeClose{Outcome: "CALLED", Reference: "ERA/CALL/3", Reason: "Contractor default", CalledAmount: &over}); err == nil {
		t.Fatal("a call above the guarantee's value was accepted")
	}
	part := dec("500000")
	must(t, "call", f.svc.CloseGuarantee(f.ctx, w.mfa(commercial), advID, GuaranteeClose{Outcome: "CALLED", Reference: "ERA/CALL/3", Reason: "Contractor default", CalledAmount: &part}))
	rawFails(t, f, "reopen a called guarantee", `UPDATE guarantees SET status = 'ACTIVE', closed_by = NULL, closed_at = NULL, close_reference = NULL, close_reason = NULL, called_amount = NULL WHERE id = '`+advID+`'`)

	reg, err = f.svc.Guarantees(f.ctx, treasury, "")
	must(t, "register after", err)
	for _, g := range reg.Guarantees {
		if g.ID == perf && (g.Standing != "RELEASED" || g.ClosedBy != "u-commercial") || g.ID == advID && (g.Standing != "CALLED" || !g.CalledAmount.Equal(part) || g.Version != 4) {
			t.Fatalf("after closing: %+v", g)
		}
	}
	if reg.Totals["active"] != 0 || !reg.Cover[0].Cover.IsZero() {
		t.Fatalf("after closing: %v %+v", reg.Totals, reg.Cover)
	}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-qs-out", TenantID: f.tenant, Roles: []string{"quantity_surveyor"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if r, err := f.svc.Guarantees(f.ctx, outsider, ""); err != nil || len(r.Guarantees) != 0 {
		t.Fatalf("an outsider saw guarantees: %v %d", err, len(r.Guarantees))
	}
}

func TestBonds_PriceAdjustment(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	w.activeContract(t)
	qs, commercial := w.ar.qs, c.commercial
	approve := func(id string) {
		must(t, "approve "+id, f.svc.DecideIndexValue(f.ctx, w.mfa(commercial), id, true, ""))
	}
	value := func(code, period, v string) string {
		id, err := f.svc.ProposeIndexValue(f.ctx, qs, IndexValueInput{IndexCode: code, Period: period, Value: dec(v), SourceRef: "CSA bulletin " + period})
		must(t, "value "+code+" "+period, err)
		must(t, "submit "+code+" "+period, f.svc.SubmitIndexValue(f.ctx, qs, id))
		return id
	}

	// Index series and values: maker/checker, never in the future, once a month.
	if err := f.svc.CreatePriceIndex(f.ctx, commercial, "CEM", "Cement price index", "Central Statistics Agency"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the approver created an index: %v", err)
	}
	must(t, "index CEM", f.svc.CreatePriceIndex(f.ctx, qs, "cem", "Cement price index", "Central Statistics Agency"))
	must(t, "index FUEL", f.svc.CreatePriceIndex(f.ctx, qs, "FUEL", "Diesel price index", "Ministry of Trade"))
	if _, err := f.svc.ProposeIndexValue(f.ctx, qs, IndexValueInput{IndexCode: "CEM", Period: time.Now().AddDate(0, 2, 0).Format("2006-01"), Value: dec("1"), SourceRef: "forecast"}); err == nil {
		t.Fatal("an index value for a future month was accepted")
	}
	cemBase := value("CEM", "2026-01", "120")
	if err := f.svc.DecideIndexValue(f.ctx, w.mfa(qs), cemBase, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the proposer approved their own value: %v", err)
	}
	if err := f.svc.DecideIndexValue(f.ctx, commercial, cemBase, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("an approval without fresh MFA: %v", err)
	}
	approve(cemBase)
	if _, err := f.svc.ProposeIndexValue(f.ctx, qs, IndexValueInput{IndexCode: "CEM", Period: "2026-01", Value: dec("125"), SourceRef: "revised"}); err == nil {
		t.Fatal("a second value for an approved month was accepted")
	}
	rawFails(t, f, "change an approved index value", `UPDATE price_index_values SET status = 'REJECTED', decision_reason = 'raw change' WHERE id = '`+cemBase+`'`)
	approve(value("CEM", "2026-09", "150"))
	fuelBase := value("FUEL", "2026-01", "80")
	approve(value("FUEL", "2026-09", "100"))
	approve(value("CEM", "2026-08", "140")) // FUEL has no August value

	// The formula: a + Σb = 1, base values approved, one per contract.
	terms := []FormulaTerm{{"CEM", dec("0.40")}, {"FUEL", dec("0.20")}}
	if _, err := f.svc.CreateFormula(f.ctx, qs, FormulaInput{ContractID: w.contract, ClauseRef: "13.8", FixedPortion: dec("0.35"), BasePeriod: "2026-01", Terms: terms}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a formula adding up to 0.95 was accepted: %v", err)
	}
	terms[1].Weight = dec("0.25")
	formula, err := f.svc.CreateFormula(f.ctx, qs, FormulaInput{ContractID: w.contract, ClauseRef: "13.8", FixedPortion: dec("0.35"), BasePeriod: "2026-01", Terms: terms})
	must(t, "formula", err)
	if err := f.svc.SubmitFormula(f.ctx, qs, formula); err == nil {
		t.Fatal("a formula was submitted while a base value awaited approval")
	}
	approve(fuelBase)
	must(t, "submit formula", f.svc.SubmitFormula(f.ctx, qs, formula))

	// A certified certificate: 1,000 m3 x 350 = 350,000 of work this period.
	w.measure(t, 10, "1000", "0", "0")
	ipc := w.certifiedIPC(t, 10, "ERA-IPC-01")
	if _, err := f.svc.PrepareAdjustment(f.ctx, qs, ipc, "2026-09"); err == nil {
		t.Fatal("an adjustment was prepared before the formula was approved")
	}
	must(t, "approve formula", f.svc.DecideFormula(f.ctx, w.mfa(commercial), formula, true, ""))
	if _, err := f.svc.CreateFormula(f.ctx, qs, FormulaInput{ContractID: w.contract, ClauseRef: "13.8", FixedPortion: dec("0.5"), BasePeriod: "2026-01", Terms: []FormulaTerm{{"CEM", dec("0.5")}}}); err == nil {
		t.Fatal("a second formula was accepted for the contract")
	}
	rawFails(t, f, "add an index to an approved formula", `INSERT INTO escalation_terms (formula_id, index_code, weight) VALUES ('`+formula+`', 'CEM', 0.1)`)

	// Pn = 0.35 + 0.40 x 150/120 + 0.25 x 100/80 = 1.1625; 350,000 x 0.1625 = 56,875.
	pv, err := f.svc.PreviewAdjustment(f.ctx, qs, ipc, "2026-09")
	must(t, "preview", err)
	if !pv.Factor.Equal(dec("1.1625")) || !pv.Amount.Equal(dec("56875")) || !pv.BaseValue.Equal(dec("350000")) || len(pv.Terms) != 2 || !pv.Terms[0].Ratio.Equal(dec("1.25")) {
		t.Fatalf("preview: %+v", pv)
	}
	for _, m := range []string{"2026-11", "2025-12", "2026-08"} {
		if _, err := f.svc.PrepareAdjustment(f.ctx, qs, ipc, m); err == nil {
			t.Fatalf("an adjustment with indices of %s was accepted", m)
		}
	}
	if _, err := f.svc.PrepareAdjustment(f.ctx, commercial, ipc, "2026-09"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the approver prepared an adjustment: %v", err)
	}
	adj, err := f.svc.PrepareAdjustment(f.ctx, qs, ipc, "2026-09")
	must(t, "prepare adjustment", err)
	if _, err := f.svc.PrepareAdjustment(f.ctx, qs, ipc, "2026-09"); err == nil {
		t.Fatal("a second adjustment for the same certificate was accepted")
	}
	rawFails(t, f, "edit the computed amount", `UPDATE price_adjustments SET amount = 99999 WHERE id = '`+adj+`'`)
	must(t, "submit adjustment", f.svc.SubmitAdjustment(f.ctx, qs, adj))
	if err := f.svc.DecideAdjustment(f.ctx, w.mfa(qs), adj, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the preparer approved the adjustment: %v", err)
	}
	if err := f.svc.DecideAdjustment(f.ctx, w.mfa(commercial), adj, false, ""); err == nil {
		t.Fatal("a rejection without a reason was accepted")
	}
	must(t, "approve adjustment", f.svc.DecideAdjustment(f.ctx, w.mfa(commercial), adj, true, ""))

	esc, err := f.svc.Escalation(f.ctx, commercial, w.contract)
	must(t, "escalation", err)
	if esc.Formula["status"] != "APPROVED" || len(esc.Adjustments) != 1 || esc.Adjustments[0].Status != "APPROVED" || !esc.ApprovedTotal.Equal(dec("56875")) ||
		len(esc.Certificates) != 1 || esc.Certificates[0]["adjusted"] != true {
		t.Fatalf("escalation: %+v", esc)
	}
	idx, err := f.svc.PriceIndices(f.ctx, commercial)
	must(t, "indices", err)
	if len(idx) != 2 || idx[0].Code != "CEM" || len(idx[0].Values) != 3 || idx[0].Values[0].Period != "2026-09" {
		t.Fatalf("indices: %+v", idx)
	}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-qs-out", TenantID: f.tenant, Roles: []string{"quantity_surveyor"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if _, err := f.svc.Escalation(f.ctx, outsider, w.contract); !errors.Is(err, auth.ErrForbidden) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("an outsider read the contract's escalation: %v", err)
	}
	_ = decimal.Zero
}
