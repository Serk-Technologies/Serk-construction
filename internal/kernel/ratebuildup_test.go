package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestRateBuildup_GoldenCaseFreezeAndRevision (EST-06, 0094). The Bible's
// estimation example: 5,800 materials + 950 labour + 650 equipment = 7,400
// direct; 10% overhead → 8,140 full cost; 15% margin → 9,576.47 selling rate
// (markup 17.6471%, not 15%). Rates carry their provenance and are approved by
// a second person (attested); a build-up uses only approved rates of its own
// currency valid on its price date; its figures are the database's; on
// approval it is frozen, and repricing is a new revision that supersedes it.
func TestRateBuildup_GoldenCaseFreezeAndRevision(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }
	rate := func(code, kind, unit, amount, from, to string) ResourceRateInput {
		return ResourceRateInput{LegalEntityID: f.entity, Code: code, Description: code + " supply", Kind: kind, Unit: unit, Currency: "ETB", Rate: dec(amount),
			Source: "Supplier quotation SQ-2026-114", Region: "Addis Ababa", Confidence: "HIGH", ValidFrom: from, ValidTo: to}
	}

	// Provenance and maker-checker on the rate library.
	if _, err := f.svc.RecordResourceRate(f.ctx, c.siteEng, rate("C25-MIX", "MATERIAL", "m3", "5800", day(-10), day(30))); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer recorded a rate: %v", err)
	}
	usd := rate("REBAR-IMP", "MATERIAL", "t", "1450", day(-10), day(200))
	usd.Currency = "USD"
	if _, err := f.svc.RecordResourceRate(f.ctx, c.estimator, usd); err == nil {
		t.Fatal("a rate in a foreign currency was recorded without its exchange-rate date")
	}
	usd.FXDate = day(-10)
	usdRate, err := f.svc.RecordResourceRate(f.ctx, c.estimator, usd)
	must(t, "foreign-currency rate with its FX date", err)
	mat, err := f.svc.RecordResourceRate(f.ctx, c.estimator, rate("C25-MIX", "MATERIAL", "m3", "5800", day(-10), day(30)))
	must(t, "material rate", err)
	lab, err := f.svc.RecordResourceRate(f.ctx, c.estimator, rate("CREW-CONC", "LABOUR", "hr", "95", day(-10), day(400)))
	must(t, "labour rate", err)
	eqp, err := f.svc.RecordResourceRate(f.ctx, c.estimator, rate("MIXER-0.5", "EQUIPMENT", "hr", "325", day(-10), day(400)))
	must(t, "equipment rate", err)
	rawRefusedWith(t, f, "approve a rate by a forged update", "approval attestation",
		`UPDATE resource_rates SET status = 'APPROVED', approved_by = 'u-commercial' WHERE id = $1::uuid`, mat)
	rawRefusedWith(t, f, "the recorder approves by a direct update", "segregation of duties",
		`UPDATE resource_rates SET status = 'APPROVED', approved_by = 'u-estimator' WHERE id = $1::uuid`, mat)
	if err := f.svc.DecideResourceRate(f.ctx, c.estimator, mat, true, ""); err == nil {
		t.Fatal("a rate was approved by its recorder")
	}

	// A build-up cannot use a rate that is not yet approved.
	golden := RateBuildupInput{ProjectID: f.project, Code: "C25-SLAB", Description: "C25 concrete in suspended slabs", Unit: "m3", Currency: "ETB", PriceDate: day(0),
		OverheadPct: dec("10"), MarginPct: dec("15"), Lines: []RateBuildupLineInput{
			{ResourceRateID: mat, Quantity: dec("1")}, {ResourceRateID: lab, Quantity: dec("10")}, {ResourceRateID: eqp, Quantity: dec("2")}}}
	if _, err := f.svc.CreateRateBuildup(f.ctx, c.estimator, golden); err == nil {
		t.Fatal("a build-up used rates that were not approved")
	}
	for _, id := range []string{mat, lab, eqp, usdRate} {
		must(t, "approve rate", f.svc.DecideResourceRate(f.ctx, c.commercial, id, true, ""))
	}
	if err := f.svc.DecideResourceRate(f.ctx, c.commercial, mat, false, "changed my mind"); err == nil {
		t.Fatal("a decided rate was decided again")
	}
	// Two approved prices for one code on one day are refused.
	overlap, err := f.svc.RecordResourceRate(f.ctx, c.estimator, rate("C25-MIX", "MATERIAL", "m3", "5900", day(20), day(60)))
	must(t, "overlapping rate recorded", err)
	if err := f.svc.DecideResourceRate(f.ctx, c.commercial, overlap, true, ""); err == nil {
		t.Fatal("an overlapping approved rate was accepted for the same code")
	}
	must(t, "reject the overlapping rate", f.svc.DecideResourceRate(f.ctx, c.commercial, overlap, false, "Overlaps the current price"))

	// The golden case, computed by the database; markup is reported, never used as margin.
	bu, err := f.svc.CreateRateBuildup(f.ctx, c.estimator, golden)
	must(t, "build-up", err)
	fig := func(want map[string]string) {
		t.Helper()
		got, err := f.svc.BuildupFigures(f.ctx, c.estimator, bu)
		must(t, "figures", err)
		have := map[string]string{"materials": got.Materials.String(), "labour": got.Labour.String(), "equipment": got.Equipment.String(), "direct": got.DirectCost.String(),
			"overhead": got.Overhead.String(), "full": got.FullCost.String(), "selling": got.SellingRate.StringFixed(2)}
		if got.MarkupPct != nil {
			have["markup"] = got.MarkupPct.String()
		}
		for k, v := range want {
			if !dec(have[k]).Equal(dec(v)) {
				t.Fatalf("%s = %s, want %s (all: %v)", k, have[k], v, have)
			}
		}
	}
	golds := map[string]string{"materials": "5800", "labour": "950", "equipment": "650", "direct": "7400", "overhead": "740", "full": "8140", "selling": "9576.47", "markup": "17.6471"}
	fig(golds)
	fig(golds) // identical on repeated calculation
	// Wastage: 2.5% on the material → 5,945; direct 7,545; full 8,299.5; selling 8,299.5 / 0.85 = 9,764.12.
	must(t, "wastage", f.svc.CorrectRateBuildupLine(f.ctx, c.estimator, bu, 1, dec("1"), dec("2.5")))
	fig(map[string]string{"materials": "5945", "direct": "7545", "full": "8299.5", "selling": "9764.12"})
	must(t, "wastage back", f.svc.CorrectRateBuildupLine(f.ctx, c.estimator, bu, 1, dec("1"), dec("0")))
	if err := f.svc.SetRateBuildupPercentages(f.ctx, c.estimator, bu, dec("10"), dec("0"), dec("0"), dec("100")); err == nil {
		t.Fatal("a margin of 100% was accepted (division by zero)")
	}
	// Rates in another currency are refused: conversion is not built.
	if err := f.svc.AddRateBuildupLines(f.ctx, c.estimator, bu, []RateBuildupLineInput{{ResourceRateID: usdRate, Quantity: dec("0.01")}}); err == nil {
		t.Fatal("a USD rate was mixed into an ETB build-up")
	}
	// The line keeps the rate's price, whatever a client sends.
	rawRefusedWith(t, f, "a line priced by the client", "permission denied",
		`UPDATE rate_buildup_lines SET rate = 1 WHERE buildup_id = $1::uuid`, bu)
	fig(golds)
	// Another project's estimator does not see it.
	other := auth.Principal{Kind: auth.KindHuman, UserID: "u-est-other", TenantID: f.tenant, Roles: []string{"estimator"}, ProjectIDs: []string{uuid.NewString()}}
	if _, err := f.svc.BuildupFigures(f.ctx, other, bu); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project's estimator read the build-up: %v", err)
	}

	// Submission fixes the figures; the author cannot approve; approval freezes it.
	must(t, "submit", f.svc.SubmitRateBuildup(f.ctx, c.estimator, bu))
	rawRefusedWith(t, f, "a fixed selling rate rewritten", "permission denied",
		`UPDATE rate_buildups SET selling_rate = 1 WHERE id = $1::uuid`, bu)
	rawRefusedWith(t, f, "a line changed after submission", "only while it is a draft",
		`UPDATE rate_buildup_lines SET quantity = 2 WHERE buildup_id = $1::uuid AND line_no = 1`, bu)
	rawRefusedWith(t, f, "a forged approval", "approval attestation",
		`UPDATE rate_buildups SET status = 'APPROVED', approved_by = 'u-commercial' WHERE id = $1::uuid`, bu)
	rawRefusedWith(t, f, "the author approves by a direct update", "segregation of duties",
		`UPDATE rate_buildups SET status = 'APPROVED', approved_by = 'u-estimator' WHERE id = $1::uuid`, bu)
	if err := f.svc.DecideRateBuildup(f.ctx, c.estimator, bu, true, ""); err == nil {
		t.Fatal("the author approved the build-up")
	}
	must(t, "approve", f.svc.DecideRateBuildup(f.ctx, c.commercial, bu, true, ""))
	var stored, status string
	queryOne(t, f, `SELECT selling_rate::text, status FROM rate_buildups WHERE id = $1::uuid`, []any{bu}, &stored, &status)
	if stored != "9576.47" || status != "APPROVED" {
		t.Fatalf("approved build-up: %s %s", stored, status)
	}
	rawRefusedWith(t, f, "an approved build-up reopened", "changes only by being superseded",
		`UPDATE rate_buildups SET status = 'DRAFT' WHERE id = $1::uuid`, bu)
	rawRefusedWith(t, f, "an approved build-up's margin changed", "changes only by being superseded",
		`UPDATE rate_buildups SET margin_pct = 20 WHERE id = $1::uuid`, bu)
	if _, err := f.svc.CreateRateBuildup(f.ctx, c.estimator, golden); err == nil {
		t.Fatal("a second build-up of the same code was started instead of a revision")
	}

	// Repricing: a new material price from day 31; the approved build-up does not move.
	newMat, err := f.svc.RecordResourceRate(f.ctx, c.estimator, rate("C25-MIX", "MATERIAL", "m3", "6000", day(31), day(400)))
	must(t, "new material price", err)
	must(t, "approve new price", f.svc.DecideResourceRate(f.ctx, c.commercial, newMat, true, ""))
	fig(golds)
	if _, err := f.svc.ReviseRateBuildup(f.ctx, c.estimator, bu, day(500)); err == nil {
		t.Fatal("a revision was priced on a day no approved rate covers")
	}
	rev, err := f.svc.ReviseRateBuildup(f.ctx, c.estimator, bu, day(31))
	must(t, "revision", err)
	got, err := f.svc.BuildupFigures(f.ctx, c.estimator, rev)
	must(t, "revision figures", err)
	// 6,000 + 950 + 650 = 7,600; 8,360 full; 8,360 / 0.85 = 9,835.29.
	if !got.DirectCost.Equal(dec("7600")) || !got.FullCost.Equal(dec("8360")) || got.SellingRate.StringFixed(2) != "9835.29" {
		t.Fatalf("revision figures: %+v", got)
	}
	must(t, "submit revision", f.svc.SubmitRateBuildup(f.ctx, c.estimator, rev))
	must(t, "approve revision", f.svc.DecideRateBuildup(f.ctx, c.commercial, rev, true, ""))
	queryOne(t, f, `SELECT status FROM rate_buildups WHERE id = $1::uuid`, []any{bu}, &status)
	if status != "SUPERSEDED" {
		t.Fatalf("revision 1 after revision 2 was approved: %s", status)
	}
	queryOne(t, f, `SELECT selling_rate::text FROM rate_buildups WHERE id = $1::uuid`, []any{bu}, &stored)
	if stored != "9576.47" {
		t.Fatalf("the superseded build-up's price moved: %s", stored)
	}
	rawRefusedWith(t, f, "a superseded build-up revived", "changes only by being superseded",
		`UPDATE rate_buildups SET status = 'APPROVED' WHERE id = $1::uuid`, bu)

	// A rejected build-up is followed by a revision too.
	rej, err := f.svc.CreateRateBuildup(f.ctx, c.estimator, RateBuildupInput{ProjectID: f.project, Code: "C25-COL", Description: "C25 concrete in columns", Unit: "m3", Currency: "ETB",
		PriceDate: day(0), MarginPct: dec("12"), Lines: []RateBuildupLineInput{{ResourceRateID: mat, Quantity: dec("1")}}})
	must(t, "second build-up", err)
	must(t, "submit second", f.svc.SubmitRateBuildup(f.ctx, c.estimator, rej))
	if err := f.svc.DecideRateBuildup(f.ctx, c.commercial, rej, false, ""); err == nil {
		t.Fatal("a build-up was rejected without a reason")
	}
	must(t, "reject", f.svc.DecideRateBuildup(f.ctx, c.commercial, rej, false, "Labour is missing from the build-up"))
	if _, err := f.svc.ReviseRateBuildup(f.ctx, c.estimator, rej, day(0)); err != nil {
		t.Fatalf("revising a rejected build-up: %v", err)
	}
}

// queryOne reads one row as the application role, inside the tenant.
func queryOne(t *testing.T, f *fixture, q string, args []any, dest ...any) {
	t.Helper()
	must(t, "query", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, args...).Scan(dest...)
	}))
}

// TestRateBuildup_ConcurrentOverlappingApprovals: two overlapping prices of the
// same code approved at the same moment never both stand.
func TestRateBuildup_ConcurrentOverlappingApprovals(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }
	for round := 0; round < 5; round++ {
		code := "RACE-" + string(rune('A'+round))
		var ids [2]string
		for i := range ids {
			id, err := f.svc.RecordResourceRate(f.ctx, c.estimator, ResourceRateInput{LegalEntityID: f.entity, Code: code, Description: "Contested price", Kind: "MATERIAL",
				Unit: "t", Currency: "ETB", Rate: dec("100"), Source: "Price list 2026", Region: "Adama", Confidence: "MEDIUM", ValidFrom: day(i), ValidTo: day(30 + i)})
			must(t, "rate", err)
			ids[i] = id
		}
		errs := make(chan error, 2)
		start := make(chan struct{})
		for _, id := range ids {
			go func(id string) { <-start; errs <- f.svc.DecideResourceRate(f.ctx, c.commercial, id, true, "") }(id)
		}
		close(start)
		ok := 0
		for range ids {
			if <-errs == nil {
				ok++
			}
		}
		var approved int
		queryOne(t, f, `SELECT count(*) FROM resource_rates WHERE code = $1 AND status = 'APPROVED'`, []any{code}, &approved)
		if approved > 1 || approved != ok {
			t.Fatalf("round %d: %d approvals reported, %d approved overlapping prices stand", round, ok, approved)
		}
	}
}
