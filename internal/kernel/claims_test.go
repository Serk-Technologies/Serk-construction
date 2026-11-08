package kernel

// Tests for contract notices and claims (WP-2.6, migration 0046, kernel/claims.go).
// Proven: the notice deadline is computed by the database and its standing
// follows the calendar; a notice is sent once and never in the future or
// before its event; a claim has the money or days its kind says; a claim with
// a missing or late notice cannot be submitted without a reason; maker/checker,
// fresh MFA, human-only decision and attestation; the client's answer is
// recorded once, only on an approved claim, and never exceeds what was claimed.

import (
	"errors"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestClaims_NoticesAndClaims(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	w.activeContract(t)
	qs := w.ar.qs
	day := func(offset int) string { return time.Now().AddDate(0, 0, offset).Format("2006-01-02") }

	// Who may raise notices.
	in := NoticeInput{ContractID: w.contract, ClauseRef: "20.1", Subject: "Unforeseen rock in cutting at Km 12", EventDate: day(-40)}
	if _, err := f.svc.CreateNotice(f.ctx, w.ar.arClerk, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an AR clerk raised a notice: %v", err)
	}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-qs-out", TenantID: f.tenant, Roles: []string{"quantity_surveyor"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if _, err := f.svc.CreateNotice(f.ctx, outsider, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a QS outside the project raised a notice: %v", err)
	}
	future := in
	future.EventDate = day(3)
	if _, err := f.svc.CreateNotice(f.ctx, qs, future); err == nil {
		t.Fatal("a notice for a future event was accepted")
	}

	// Three notices: overdue (28 days from 40 days ago), due soon, and open.
	overdue, err := f.svc.CreateNotice(f.ctx, qs, in)
	must(t, "overdue notice", err)
	soon, err := f.svc.CreateNotice(f.ctx, qs, NoticeInput{ContractID: w.contract, ClauseRef: "20.1", Subject: "Late drawings", EventDate: day(-24)})
	must(t, "soon notice", err)
	open, err := f.svc.CreateNotice(f.ctx, qs, NoticeInput{ContractID: w.contract, ClauseRef: "8.4", Subject: "Heavy rain", EventDate: day(-2), DueDays: 14})
	must(t, "open notice", err)
	standing := map[string]Notice{}
	list, err := f.svc.ListNotices(f.ctx, qs, w.contract)
	must(t, "list", err)
	for _, n := range list {
		standing[n.ID] = n
	}
	for id, want := range map[string]string{overdue: "OVERDUE", soon: "DUE_SOON", open: "OPEN"} {
		if standing[id].Standing != want {
			t.Fatalf("notice %s standing %s, want %s", id, standing[id].Standing, want)
		}
	}
	if standing[overdue].DaysLeft != -12 || standing[soon].DaysLeft != 4 || standing[open].DaysLeft != 12 {
		t.Fatalf("days left: %d %d %d", standing[overdue].DaysLeft, standing[soon].DaysLeft, standing[open].DaysLeft)
	}

	// Sending: not in the future, not before the event, once.
	if err := f.svc.SendNotice(f.ctx, qs, soon, day(1)); err == nil {
		t.Fatal("a notice was sent in the future")
	}
	if err := f.svc.SendNotice(f.ctx, qs, soon, day(-30)); err == nil {
		t.Fatal("a notice was sent before its event")
	}
	must(t, "send soon on time", f.svc.SendNotice(f.ctx, qs, soon, day(-1)))
	if err := f.svc.SendNotice(f.ctx, qs, soon, day(0)); err == nil {
		t.Fatal("a notice was sent twice")
	}
	rawFails(t, f, "edit a sent notice", `UPDATE contract_notices SET subject = 'changed' WHERE id = '`+soon+`'`)
	rawFails(t, f, "move a notice's event", `UPDATE contract_notices SET event_date = current_date WHERE id = '`+open+`'`)
	must(t, "send overdue late", f.svc.SendNotice(f.ctx, qs, overdue, day(0)))
	if n, _ := f.svc.ListNotices(f.ctx, qs, w.contract); true {
		for _, x := range n {
			if x.ID == soon && x.Standing != "SENT" || x.ID == overdue && x.Standing != "SENT_LATE" {
				t.Fatalf("after sending: %+v", x)
			}
		}
	}

	// Claim rules by kind.
	base := ClaimInput{ContractID: w.contract, NoticeID: soon, Kind: "BOTH", Title: "Rock excavation", Description: "Unforeseen rock needed breaker plant for 9 days", Amount: dec("450000"), Days: 9}
	for name, mut := range map[string]func(*ClaimInput){
		"time with money":           func(x *ClaimInput) { x.Kind, x.Amount = "TIME", dec("1") },
		"cost with days":            func(x *ClaimInput) { x.Kind, x.Days = "COST", 1 },
		"both without days":         func(x *ClaimInput) { x.Days = 0 },
		"unknown kind":              func(x *ClaimInput) { x.Kind = "WISH" },
		"another contract's notice": func(x *ClaimInput) { x.NoticeID = "00000000-0000-0000-0000-000000000009" },
	} {
		bad := base
		mut(&bad)
		if _, err := f.svc.CreateClaim(f.ctx, qs, bad); err == nil {
			t.Fatalf("claim accepted: %s", name)
		}
	}

	// A late or missing notice needs a stated reason; an on-time notice does not.
	lateClaim := base
	lateClaim.NoticeID = overdue
	late, err := f.svc.CreateClaim(f.ctx, qs, lateClaim)
	must(t, "late claim", err)
	if err := f.svc.SubmitClaim(f.ctx, qs, late); err == nil {
		t.Fatal("a claim on a late notice was submitted without a reason")
	}
	noNotice := base
	noNotice.NoticeID = ""
	none, err := f.svc.CreateClaim(f.ctx, qs, noNotice)
	must(t, "claim with no notice", err)
	if err := f.svc.SubmitClaim(f.ctx, qs, none); err == nil {
		t.Fatal("a claim with no notice was submitted without a reason")
	}
	waived := lateClaim
	waived.WaiverReason = "Engineer waived the time bar in writing, ref ENG/044"
	waivedID, err := f.svc.CreateClaim(f.ctx, qs, waived)
	must(t, "waived claim", err)
	must(t, "submit waived", f.svc.SubmitClaim(f.ctx, qs, waivedID))

	id, err := f.svc.CreateClaim(f.ctx, qs, base)
	must(t, "claim", err)
	must(t, "submit on time", f.svc.SubmitClaim(f.ctx, qs, id))

	// Decision: maker/checker, fresh MFA, human only, attested, reason on rejection.
	if err := f.svc.DecideClaim(f.ctx, qs, id, true, ""); err == nil {
		t.Fatal("the author approved their own claim")
	}
	fresh := c.commercial
	fresh.MFA, fresh.AuthTime = true, time.Now()
	stale := fresh
	stale.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.DecideClaim(f.ctx, stale, id, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("approval without fresh MFA: %v", err)
	}
	ai := fresh
	ai.Kind = auth.KindAI
	if err := f.svc.DecideClaim(f.ctx, ai, id, true, ""); err == nil {
		t.Fatal("an AI principal decided a claim")
	}
	rawFails(t, f, "approve without an attestation", `UPDATE contract_claims SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+id+`'`)
	if err := f.svc.DecideClaim(f.ctx, fresh, waivedID, false, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("rejection without a reason: %v", err)
	}
	must(t, "reject waived", f.svc.DecideClaim(f.ctx, fresh, waivedID, false, "Insufficient evidence of the waiver"))

	// The client's answer: only on an approved claim, bounded by the claim, once.
	answer := ClaimOutcome{Outcome: "AGREED", Reference: "ENG/EOT/07", AgreedAmount: dec("300000"), AgreedDays: 6, On: day(0)}
	if err := f.svc.RecordClaimOutcome(f.ctx, qs, id, answer); err == nil {
		t.Fatal("the client's answer was recorded before the claim was approved")
	}
	must(t, "approve", f.svc.DecideClaim(f.ctx, fresh, id, true, ""))
	over := answer
	over.AgreedAmount = dec("450001")
	if err := f.svc.RecordClaimOutcome(f.ctx, qs, id, over); err == nil {
		t.Fatal("the client agreed more money than was claimed")
	}
	over = answer
	over.AgreedDays = 10
	if err := f.svc.RecordClaimOutcome(f.ctx, qs, id, over); err == nil {
		t.Fatal("the client agreed more days than were claimed")
	}
	dated := answer
	dated.On = day(2)
	if err := f.svc.RecordClaimOutcome(f.ctx, qs, id, dated); err == nil {
		t.Fatal("the client's answer was dated in the future")
	}
	must(t, "outcome", f.svc.RecordClaimOutcome(f.ctx, qs, id, answer))
	if err := f.svc.RecordClaimOutcome(f.ctx, qs, id, answer); err == nil {
		t.Fatal("the client's answer was recorded twice")
	}
	rawFails(t, f, "edit a claim after the client answered", `UPDATE contract_claims SET amount = 1 WHERE id = '`+id+`'`)
	rawFails(t, f, "edit a rejected claim", `UPDATE contract_claims SET title = 'changed' WHERE id = '`+waivedID+`'`)

	// Reading: the claim is visible with its outcome in the document list.
	d, err := f.svc.GetP2PDoc(f.ctx, qs, "contract_claim", id)
	must(t, "get claim", err)
	if d.Status != "APPROVED" {
		t.Fatalf("claim: %+v", d)
	}

	// The registers agree with the documents.
	nr, err := f.svc.RunReport(f.ctx, c.cfo, "notice-register", ReportFilters{EntityID: f.entity, ProjectID: f.project})
	must(t, "notice register", err)
	if len(nr.Rows) != 3 {
		t.Fatalf("notice register: %v", nr.Rows)
	}
	cr, err := f.svc.RunReport(f.ctx, c.cfo, "claims-register", ReportFilters{EntityID: f.entity, ProjectID: f.project})
	must(t, "claims register", err)
	if len(cr.Rows) != 4 || !dec(cr.Totals["claimed_amount"]).Equal(dec("1800000")) || !dec(cr.Totals["agreed_amount"]).Equal(dec("300000")) {
		t.Fatalf("claims register: %d rows, totals %v", len(cr.Rows), cr.Totals)
	}
}
