package kernel

// Tests for fixed assets (WP-2.9, migration 0057, kernel/fixedassets.go).
// Fixture (hand-checked):
//   Excavator: cost 1,200,000, residual 120,000, 60 months, straight line -> 18,000 a month.
//   Survey kit: cost 600,000, residual 0, 48 months, double declining -> 25,000 in October
//     (600,000 x 2/48), 23,958.33 in November (575,000 x 2/48).
//   October run 43,000. Disposal of the excavator for 1,000,000 after two months:
//     accumulated 36,000, net book value 1,164,000, loss 164,000.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

func TestFixedAssets_CapitaliseDepreciateDispose(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	acc, cfo := c.accountant, fresh(c.cfo)
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"1500", "Plant and equipment", "ASSET"}, {"1590", "Accumulated depreciation", "ASSET"},
			{"1550", "Assets under construction", "ASSET"}, {"6100", "Depreciation", "EXPENSE"}, {"4900", "Gain or loss on disposal", "REVENUE"}, {"1100", "Accounts receivable", "ASSET"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			w.acct[a.code] = id
		}
		return nil
	}))
	journal := func(id string) string {
		var s string
		must(t, "journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT string_agg(a.code || ' ' || l.side || ' ' || l.amount::numeric(24,2), ', ' ORDER BY a.code, l.side) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = $1::uuid`, id).Scan(&s)
		}))
		return s
	}

	if _, err := f.svc.CreateAssetClass(f.ctx, acc, AssetClassInput{LegalEntityID: f.entity, Code: "PLANT", Name: "Plant", CostAccount: w.acct["1500"], AccumAccount: w.acct["6100"],
		ExpenseAcct: w.acct["1590"], GainLossAcct: w.acct["4900"], LifeMonths: 60}); err == nil {
		t.Fatal("an expense account was accepted as accumulated depreciation")
	}
	class, err := f.svc.CreateAssetClass(f.ctx, acc, AssetClassInput{LegalEntityID: f.entity, Code: "plant", Name: "Plant", CostAccount: w.acct["1500"], AccumAccount: w.acct["1590"],
		ExpenseAcct: w.acct["6100"], GainLossAcct: w.acct["4900"], LifeMonths: 60})
	must(t, "class", err)
	if _, err := f.svc.RegisterAsset(f.ctx, c.pm, AssetInput{ClassID: class, Description: "x", InServiceOn: "2026-10-05", Cost: dec("1"), CapitalisedFrom: w.acct["1550"]}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager registered an asset: %v", err)
	}
	register := func(desc, cost, residual, method string, life int) string {
		id, err := f.svc.RegisterAsset(f.ctx, acc, AssetInput{ClassID: class, Description: desc, InServiceOn: "2026-10-05", Cost: dec(cost), Residual: dec(residual),
			LifeMonths: life, Method: method, CapitalisedFrom: w.acct["1550"]})
		must(t, "asset "+desc, err)
		must(t, "submit "+desc, f.svc.SubmitAsset(f.ctx, acc, id))
		if _, err := f.svc.DecideAsset(f.ctx, fresh(acc), id, true, ""); err == nil {
			t.Fatal("the accountant activated their own asset")
		}
		jid, err := f.svc.DecideAsset(f.ctx, cfo, id, true, "")
		must(t, "activate "+desc, err)
		w.financePost(t, jid)
		return id
	}
	exc := register("Excavator CAT 320", "1200000", "120000", "SL", 60)
	register("Survey kit", "600000", "0", "DB", 48)
	rawFails(t, f, "dispose without approval", `UPDATE fixed_assets SET status = 'DISPOSED' WHERE id = '`+exc+`'`)

	// August.
	pv, err := f.svc.PreviewDepreciation(f.ctx, acc, f.entity, "2026-10")
	must(t, "preview", err)
	if len(pv) != 2 || !pv[0].Amount.Equal(dec("18000")) || !pv[1].Amount.Equal(dec("25000")) {
		t.Fatalf("August charges: %+v", pv)
	}
	aug, err := f.svc.PrepareDepreciation(f.ctx, acc, f.entity, "2026-10")
	must(t, "run August", err)
	if _, err := f.svc.PrepareDepreciation(f.ctx, acc, f.entity, "2026-11"); err == nil {
		t.Fatal("a run while the previous journal is a draft")
	}
	var augJ string
	must(t, "aug journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT journal_id::text FROM depreciation_runs WHERE id = $1::uuid`, aug).Scan(&augJ)
	}))
	if got := journal(augJ); got != "1590 CREDIT 43000.00, 6100 DEBIT 43000.00" {
		t.Fatalf("August journal: %s", got)
	}
	rawFails(t, f, "forge a depreciation line", `INSERT INTO depreciation_lines (run_id, asset_id, nbv_before, amount) VALUES ('`+aug+`', '`+exc+`', 1, 1)`)
	w.financePost(t, augJ)
	if _, err := f.svc.PrepareDepreciation(f.ctx, acc, f.entity, "2026-10"); err == nil {
		t.Fatal("August was depreciated twice")
	}

	// September: run, cancel by finance, run again, post.
	pv, err = f.svc.PreviewDepreciation(f.ctx, acc, f.entity, "2026-11")
	must(t, "preview Sep", err)
	if len(pv) != 2 || !pv[1].Amount.Equal(dec("23958.33")) || !pv[1].NBVBefore.Equal(dec("575000")) {
		t.Fatalf("September charges: %+v", pv)
	}
	sep, err := f.svc.PrepareDepreciation(f.ctx, acc, f.entity, "2026-11")
	must(t, "run Sep", err)
	if err := f.svc.CancelDepreciation(f.ctx, acc, sep, "wrong month"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the accountant cancelled the run: %v", err)
	}
	must(t, "cancel Sep", f.svc.CancelDepreciation(f.ctx, c.finMgr, sep, "Rerun after the asset review"))
	sep, err = f.svc.PrepareDepreciation(f.ctx, acc, f.entity, "2026-11")
	must(t, "rerun Sep", err)
	var sepJ string
	must(t, "sep journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT journal_id::text FROM depreciation_runs WHERE id = $1::uuid`, sep).Scan(&sepJ)
	}))
	w.financePost(t, sepJ)

	// Disposal of the excavator: loss 164,000.
	disp, err := f.svc.ProposeDisposal(f.ctx, acc, DisposalInput{AssetID: exc, DisposedOn: today().Format("2006-01-02"), Proceeds: dec("1000000"), ProceedsAccount: w.acct["1100"], Reason: "Sold to a plant hire company"})
	must(t, "disposal", err)
	must(t, "submit disposal", f.svc.SubmitDisposal(f.ctx, acc, disp))
	dj, err := f.svc.DecideDisposal(f.ctx, cfo, disp, true, "")
	must(t, "approve disposal", err)
	if got := journal(dj); got != "1100 DEBIT 1000000.00, 1500 CREDIT 1200000.00, 1590 DEBIT 36000.00, 4900 DEBIT 164000.00" {
		t.Fatalf("disposal journal: %s", got)
	}
	w.financePost(t, dj)
	pv, err = f.svc.PreviewDepreciation(f.ctx, acc, f.entity, "2026-12")
	must(t, "preview Dec", err)
	if len(pv) != 1 {
		t.Fatalf("a disposed asset is still depreciated: %+v", pv)
	}
	reg, err := f.svc.AssetRegister(f.ctx, c.finMgr)
	must(t, "register", err)
	if len(reg["assets"].([]map[string]any)) != 2 || len(reg["runs"].([]map[string]any)) != 3 {
		t.Fatalf("register: %v", reg)
	}
	if _, err := f.svc.AssetRegister(f.ctx, c.pm); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager read the asset register: %v", err)
	}
}
