package kernel

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestSupplierGovernance_QualificationConflictsScores (SUP-03.3, 0093). With
// the entity's approved P2P policy requiring them: an evaluator or awarder
// declares on the RFQ first and, having declared a conflict with a supplier,
// neither evaluates its quote nor awards to it; an RFQ is awarded only to a
// supplier qualified today (recorded by one person, approved by another,
// attested). An order is scored once, after receipt, not by its approver.
func TestSupplierGovernance_QualificationConflictsScores(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.approvedBOQ(t)

	// Policy version 2 switches both rules on (maker-checker, as any policy).
	pol, err := f.svc.ProposeP2PPolicy(f.ctx, c.controller, postgres.P2PPolicy{LegalEntityID: f.entity, PriceTolerancePct: dec("0.02"),
		OverReceiptPct: dec("0.05"), VATRate: dec("0.15"), HighValuePOThreshold: dec("5000000"), DualPaymentThreshold: dec("500000"),
		GRNIAccountID: w.acct["2100"], APAccountID: w.acct["2000"], VATInputAccountID: w.acct["1300"], PPVAccountID: w.acct["5100"],
		RequireConflictDeclarations: true, RequireSupplierQualification: true})
	must(t, "policy v2", err)
	must(t, "approve policy v2", f.svc.DecideP2PPolicy(f.ctx, c.cfo, pol, true))

	// Qualification of the first supplier: recorded by the buyer, decided by the procurement manager.
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }
	qin := SupplierQualificationInput{SupplierID: w.sup[0], Category: "Reinforcement steel", Reference: "MoTI licence 4471/2026", ValidFrom: day(-30), ValidTo: day(335)}
	if _, err := f.svc.RecordSupplierQualification(f.ctx, c.siteEng, qin); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer recorded a qualification: %v", err)
	}
	q1, err := f.svc.RecordSupplierQualification(f.ctx, c.buyer, qin)
	must(t, "qualification", err)
	rawRefusedWith(t, f, "approve a qualification by a forged update", "approval attestation",
		`UPDATE supplier_qualifications SET status = 'APPROVED', approved_by = 'u-proc-manager' WHERE id = $1::uuid`, q1)
	rawRefusedWith(t, f, "the recorder approves by a direct update", "segregation of duties",
		`UPDATE supplier_qualifications SET status = 'APPROVED', approved_by = 'u-buyer' WHERE id = $1::uuid`, q1)
	expired, err := f.svc.RecordSupplierQualification(f.ctx, c.buyer, SupplierQualificationInput{SupplierID: w.sup[1], Category: "Cement", Reference: "Old licence 12/2024", ValidFrom: day(-400), ValidTo: day(-35)})
	must(t, "expired qualification", err)
	must(t, "approve expired", f.svc.DecideSupplierQualification(f.ctx, c.procMgr, expired, true, ""))

	// PR -> RFQ -> quotes.
	pr, err := f.svc.CreatePR(f.ctx, c.siteEng, f.project, "Level 4 slab reinforcement", nil, []PRLine{
		{LineNo: 1, CostCodeID: w.costCode, Description: "Reinforcement steel Grade 60", Unit: "t", Quantity: dec("10"), EstUnitPrice: dec("86000")}})
	must(t, "pr", err)
	must(t, "pr submit", f.svc.SubmitPR(f.ctx, c.siteEng, pr))
	must(t, "pr approve", f.svc.DecidePR(f.ctx, c.pm, pr, true, ""))
	rfq, err := f.svc.CreateRFQ(f.ctx, c.buyer, pr, time.Now().Add(72*time.Hour), 2, "", w.sup[:2])
	must(t, "rfq", err)
	must(t, "rfq issue", f.svc.IssueRFQ(f.ctx, c.buyer, rfq))
	qa, err := f.svc.RecordQuote(f.ctx, c.buyer, rfq, w.sup[0], time.Now().AddDate(0, 1, 0), "Q-A", []QuoteLine{{1, dec("85000")}})
	must(t, "quote A", err)
	qb, err := f.svc.RecordQuote(f.ctx, c.buyer, rfq, w.sup[1], time.Now().AddDate(0, 1, 0), "Q-B", []QuoteLine{{1, dec("84000")}})
	must(t, "quote B", err)

	// Conflict declarations.
	if err := f.svc.EvaluateQuote(f.ctx, c.siteEng, qa, true, ""); err == nil || !strings.Contains(err.Error(), "declares on this RFQ") {
		t.Fatalf("a quote was evaluated without a declaration: %v", err)
	}
	if err := f.svc.DeclareRFQConflict(f.ctx, c.siteEng, rfq, w.sup[1], ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a conflict without the relationship: %v", err)
	}
	must(t, "declare conflict", f.svc.DeclareRFQConflict(f.ctx, c.siteEng, rfq, w.sup[1], "Brother-in-law is a director of the supplier"))
	must(t, "evaluate A", f.svc.EvaluateQuote(f.ctx, c.siteEng, qa, true, ""))
	if err := f.svc.EvaluateQuote(f.ctx, c.siteEng, qb, true, ""); err == nil || !strings.Contains(err.Error(), "conflict of interest") {
		t.Fatalf("a conflicted evaluator evaluated the supplier's quote: %v", err)
	}
	eng2 := c.siteEng
	eng2.UserID = "u-site-engineer-2"
	must(t, "declare none", f.svc.DeclareRFQConflict(f.ctx, eng2, rfq, "", ""))
	must(t, "evaluate B", f.svc.EvaluateQuote(f.ctx, eng2, qb, true, ""))
	rawFails(t, f, "withdraw a declaration", `DELETE FROM rfq_conflict_declarations WHERE rfq_id = '`+rfq+`'`)

	// Award: declaration and qualification.
	if err := f.svc.AwardRFQ(f.ctx, c.procMgr, rfq, qa, "Better delivery terms"); err == nil || !strings.Contains(err.Error(), "declares on this RFQ") {
		t.Fatalf("an RFQ was awarded without a declaration: %v", err)
	}
	must(t, "manager declares none", f.svc.DeclareRFQConflict(f.ctx, c.procMgr, rfq, "", ""))
	if err := f.svc.AwardRFQ(f.ctx, c.procMgr, rfq, qb, ""); err == nil || !strings.Contains(err.Error(), "qualification") {
		t.Fatalf("an RFQ was awarded to a supplier whose qualification expired: %v", err)
	}
	if err := f.svc.AwardRFQ(f.ctx, c.procMgr, rfq, qa, "Better delivery terms"); err == nil || !strings.Contains(err.Error(), "qualification") {
		t.Fatalf("an RFQ was awarded to a supplier whose qualification is still pending: %v", err)
	}
	must(t, "approve qualification", f.svc.DecideSupplierQualification(f.ctx, c.procMgr, q1, true, ""))
	must(t, "award A", f.svc.AwardRFQ(f.ctx, c.procMgr, rfq, qa, "Better delivery terms"))
	decl, err := f.svc.RFQDeclarations(f.ctx, c.pm, rfq)
	must(t, "declarations", err)
	if len(decl) != 3 {
		t.Fatalf("declarations: %v", decl)
	}

	// Score: after receipt, once, not by the approver.
	po, err := f.svc.CreatePOFromAward(f.ctx, c.buyer, rfq, map[int]string{1: w.acct["5000"]})
	must(t, "po", err)
	must(t, "po approve", f.svc.ApprovePO(f.ctx, c.procMgr, po, ""))
	if err := f.svc.ScoreSupplier(f.ctx, c.siteEng, po, 4, 3, 5, ""); err == nil || !strings.Contains(err.Error(), "after goods were received") {
		t.Fatalf("an order was scored before receipt: %v", err)
	}
	w.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("10")})
	if err := f.svc.ScoreSupplier(f.ctx, c.procMgr, po, 4, 3, 5, ""); err == nil {
		t.Fatal("the order's approver scored its supplier")
	}
	if err := f.svc.ScoreSupplier(f.ctx, c.siteEng, po, 6, 3, 5, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a score of 6: %v", err)
	}
	must(t, "score", f.svc.ScoreSupplier(f.ctx, c.siteEng, po, 4, 3, 5, "Two days late on the second load"))
	if err := f.svc.ScoreSupplier(f.ctx, c.siteEng, po, 5, 5, 5, ""); err == nil {
		t.Fatal("an order was scored twice")
	}
	st, err := f.svc.SupplierStandingOf(f.ctx, c.pm, w.sup[0])
	must(t, "standing", err)
	if !st.QualifiedToday || st.Scores != 1 || st.Quality == nil || *st.Quality != 4 || *st.Delivery != 3 || *st.Safety != 5 {
		t.Fatalf("supplier standing: %+v", st)
	}
	if st2, _ := f.svc.SupplierStandingOf(f.ctx, c.pm, w.sup[1]); st2.QualifiedToday {
		t.Fatal("a supplier with only an expired qualification is shown qualified")
	}
}
