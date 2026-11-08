package kernel

// Tests for liquidated damages, extension of time and obligations (WP-2.6
// completion, migration 0051, kernel/contracttime.go). Proven: damages terms
// are prepared and approved by two people (fresh MFA), once per contract,
// never after the contract value or before signing; the revised completion
// carries the days the client agreed on claims; damages accrue per day late
// and stop at the cap; obligations are closed once, a waiver needs a reason;
// the deadline alerts find late and near items and re-key at 14, 7, 2 days.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/automation"
)

func TestContractTime_DamagesExtensionsObligations(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	w.activeContract(t) // signed 2026-10-01, value 23,060,000
	qs, commercial := w.ar.qs, c.commercial
	day := func(offset int) string { return time.Now().AddDate(0, 0, offset).Format("2006-01-02") }

	if got := LDAmount(100, dec("50000"), dec("2306000")); !got.Equal(dec("2306000")) {
		t.Fatalf("cap not applied: %s", got)
	}
	if got := LDAmount(-3, dec("50000"), dec("2306000")); !got.IsZero() {
		t.Fatalf("damages before completion: %s", got)
	}

	terms := LDTermsInput{ContractID: w.contract, ClauseRef: "8.8", CompletionDate: "2026-10-20", RatePerDay: dec("50000"), CapAmount: dec("2306000")}
	if _, err := f.svc.CreateLDTerms(f.ctx, w.ar.arClerk, terms); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an AR clerk set damages terms: %v", err)
	}
	bad := terms
	bad.CapAmount = dec("30000000")
	if _, err := f.svc.CreateLDTerms(f.ctx, qs, bad); err == nil {
		t.Fatal("a cap above the contract value was accepted")
	}
	bad = terms
	bad.CompletionDate = "2026-09-01"
	if _, err := f.svc.CreateLDTerms(f.ctx, qs, bad); err == nil {
		t.Fatal("a completion date before signing was accepted")
	}
	ld, err := f.svc.CreateLDTerms(f.ctx, qs, terms)
	must(t, "terms", err)
	must(t, "submit terms", f.svc.SubmitLDTerms(f.ctx, qs, ld))
	if err := f.svc.DecideLDTerms(f.ctx, w.mfa(qs), ld, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the preparer approved the terms: %v", err)
	}
	if err := f.svc.DecideLDTerms(f.ctx, commercial, ld, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("terms approved without fresh MFA: %v", err)
	}
	must(t, "approve terms", f.svc.DecideLDTerms(f.ctx, w.mfa(commercial), ld, true, ""))
	if _, err := f.svc.CreateLDTerms(f.ctx, qs, terms); err == nil {
		t.Fatal("a second set of terms was accepted")
	}
	rawFails(t, f, "change approved terms", `UPDATE contract_ld_terms SET status = 'REJECTED', decision_reason = 'raw change' WHERE id = '`+ld+`'`)

	today := time.Now().UTC().Truncate(24 * time.Hour)
	late := func(rev string) int {
		d, _ := time.Parse("2006-01-02", rev)
		n := int(today.Sub(d).Hours() / 24)
		if n < 0 {
			return 0
		}
		return n
	}
	pos, err := f.svc.ContractTimePosition(f.ctx, commercial, w.contract)
	must(t, "position", err)
	if pos.CompletionDate != "2026-10-20" || pos.ExtensionDays != 0 || pos.DaysLate != late("2026-10-20") ||
		!pos.AccruedToDate.Equal(LDAmount(late("2026-10-20"), dec("50000"), dec("2306000"))) || pos.ScheduleFinish != "" {
		t.Fatalf("position: %+v", pos)
	}

	// An extension agreed on a claim moves the completion date.
	claim, err := f.svc.CreateClaim(f.ctx, qs, ClaimInput{ContractID: w.contract, Kind: "TIME", Title: "Late access to Km 12", Description: "The client handed over the site at Km 12 late.",
		Days: 10, WaiverReason: "Engineer instructed the claim in writing"})
	must(t, "claim", err)
	must(t, "claim submit", f.svc.SubmitClaim(f.ctx, qs, claim))
	must(t, "claim approve", f.svc.DecideClaim(f.ctx, w.mfa(commercial), claim, true, ""))
	must(t, "claim agreed", f.svc.RecordClaimOutcome(f.ctx, qs, claim, ClaimOutcome{Outcome: "AGREED", Reference: "ERA/EOT/01", AgreedDays: 8, On: day(0)}))
	pos, err = f.svc.ContractTimePosition(f.ctx, commercial, w.contract)
	must(t, "position after extension", err)
	if pos.ExtensionDays != 8 || pos.RevisedCompletion != "2026-10-28" || len(pos.Extensions) != 1 || pos.DaysLate != late("2026-10-28") ||
		!pos.AccruedToDate.Equal(LDAmount(late("2026-10-28"), dec("50000"), dec("2306000"))) {
		t.Fatalf("after extension: %+v", pos)
	}

	// Obligations.
	if _, err := f.svc.AddObligation(f.ctx, w.ar.arClerk, ObligationInput{ContractID: w.contract, ClauseRef: "4.2", Description: "Performance security", Party: "CONTRACTOR", DueOn: day(5)}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an AR clerk added an obligation: %v", err)
	}
	soon, err := f.svc.AddObligation(f.ctx, qs, ObligationInput{ContractID: w.contract, ClauseRef: "8.3", Description: "Submit the revised programme", Party: "contractor", DueOn: day(5)})
	must(t, "obligation soon", err)
	overdue, err := f.svc.AddObligation(f.ctx, qs, ObligationInput{ContractID: w.contract, ClauseRef: "2.1", Description: "Give access to the borrow pit", Party: "CLIENT", Owner: "u-pm", DueOn: day(-2)})
	must(t, "obligation overdue", err)
	list, err := f.svc.Obligations(f.ctx, commercial)
	must(t, "obligations", err)
	stand := map[string]Obligation{}
	for _, o := range list {
		stand[o.ID] = o
	}
	if stand[soon].Standing != "DUE_SOON" || stand[soon].Owner != "u-qs" || stand[overdue].Standing != "OVERDUE" || stand[overdue].DaysLeft != -2 {
		t.Fatalf("standing: %+v %+v", stand[soon], stand[overdue])
	}
	must(t, "deadline alerts", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		got, err := deadlineFindings(ctx, tx, automation.ObligationDue, time.Now(), 14)
		if err != nil {
			return err
		}
		keys := map[string]string{}
		for _, g := range got {
			keys[g.SubjectID] = g.Key
		}
		if keys[soon] != "7d" || keys[overdue] != "overdue" {
			t.Fatalf("obligation alerts: %+v", got)
		}
		return nil
	}))
	if err := f.svc.CloseObligation(f.ctx, qs, soon, "WAIVED", day(0), ""); err == nil {
		t.Fatal("a waiver without a reason was accepted")
	}
	if err := f.svc.CloseObligation(f.ctx, qs, soon, "DONE", day(1), "Letter CT/114"); err == nil {
		t.Fatal("an obligation was closed in the future")
	}
	must(t, "close", f.svc.CloseObligation(f.ctx, qs, soon, "DONE", day(0), "Letter CT/114"))
	if err := f.svc.CloseObligation(f.ctx, qs, soon, "WAIVED", day(0), "changed my mind"); err == nil {
		t.Fatal("a closed obligation was closed again")
	}
	rawFails(t, f, "reopen an obligation", `UPDATE contract_obligations SET status = 'OPEN', closed_on = NULL, closed_by = NULL, close_note = NULL WHERE id = '`+soon+`'`)
	pos, err = f.svc.ContractTimePosition(f.ctx, commercial, w.contract)
	must(t, "position obligations", err)
	if len(pos.Obligations) != 2 || pos.Obligations[0].ID != overdue {
		t.Fatalf("obligations on the contract: %+v", pos.Obligations)
	}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-qs-out", TenantID: f.tenant, Roles: []string{"quantity_surveyor"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if o, err := f.svc.Obligations(f.ctx, outsider); err != nil || len(o) != 0 {
		t.Fatalf("an outsider saw obligations: %v %d", err, len(o))
	}
}

func TestDeadlineBucket(t *testing.T) {
	for days, want := range map[int]string{-1: "overdue", 0: "2d", 2: "2d", 3: "7d", 7: "7d", 8: "14d", 14: "14d", 15: "early"} {
		if got := automation.DeadlineBucket(days); got != want {
			t.Fatalf("%d days: %s, want %s", days, got, want)
		}
	}
}
