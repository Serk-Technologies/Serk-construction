package kernel

// Tests for the authority matrix, delegation of authority and duty-conflict
// exceptions (WP-1.2, migration 0040, kernel/authority.go).
//
// What is proven, in order:
//  1. the matrix is a governed document (maker/checker, MFA, one in force) and
//     limits approvals: by the kernel (purchase order) and by the database
//     itself (budget documents, even with raw SQL);
//  2. delegation: limits of what can be lent, a third-person approver, the
//     delegate acts with the lent authority, never on the delegator's own
//     documents, and revocation ends it at once;
//  3. duty conflicts: a delegation that would create a conflicting set is
//     dropped, unless an approved, expiring exception covers that person.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// authorityWorld is the budget world plus the people governance needs.
type authorityWorld struct {
	*budgetWorld
	owner, secAdmin, cfo, commercial, planner auth.Principal
}

func newAuthorityWorld(t *testing.T) *authorityWorld {
	t.Helper()
	b := newBudgetWorld(t)
	now := time.Now()
	mk := func(id string, mfa bool, roles ...string) auth.Principal {
		p := auth.Principal{Kind: auth.KindHuman, UserID: id, TenantID: b.f.tenant, Roles: roles, ProjectIDs: []string{b.f.project}}
		if mfa {
			p.MFA, p.AuthTime = true, now
		}
		return p
	}
	return &authorityWorld{budgetWorld: b,
		owner: mk("u-process-owner", false, "process_owner"), secAdmin: mk("u-security-admin", true, "security_admin"),
		cfo: mk("u-cfo-2", true, "cfo"), commercial: mk("u-commercial-2", true, "commercial_manager"), planner: mk("u-planner", false, "planner")}
}

// matrixRule: budget transfers from 100,000 ETB need approval.high_value.
func (w *authorityWorld) activateMatrix(t *testing.T, rules ...AuthorityRule) string {
	t.Helper()
	f := w.f
	id, err := f.svc.CreateAuthorityMatrix(f.ctx, w.owner, "Delegation of authority 2026", rules)
	must(t, "create matrix", err)
	must(t, "submit matrix", f.svc.SubmitAuthorityMatrix(f.ctx, w.owner, id))
	must(t, "approve matrix", f.svc.DecideAuthorityMatrix(f.ctx, w.cfo, id, true, ""))
	return id
}

func bigTransferRule() AuthorityRule {
	return AuthorityRule{DocType: "budget_transfer", Min: dec("100000"), Currency: "ETB", RequiredPerm: string(auth.PermApprovalHighValue), RequiresMFA: true}
}

func TestAuthority_MatrixGovernanceAndLimits(t *testing.T) {
	w := newAuthorityWorld(t)
	f, c := w.f, w.cast
	w.approvedOriginal(t)

	// Preparing needs authority.manage: the CFO and the project manager cannot.
	if _, err := f.svc.CreateAuthorityMatrix(f.ctx, w.cfo, "no", []AuthorityRule{bigTransferRule()}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the CFO prepared a matrix: %v", err)
	}
	// A rule must name a permission some role grants, with a real band.
	bad := bigTransferRule()
	bad.RequiredPerm = "nonsense.perm"
	if _, err := f.svc.CreateAuthorityMatrix(f.ctx, w.owner, "typo", []AuthorityRule{bad}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a rule naming an unknown permission was accepted: %v", err)
	}
	// Overlapping bands are refused by the database.
	if _, err := f.svc.CreateAuthorityMatrix(f.ctx, w.owner, "overlap", []AuthorityRule{bigTransferRule(), bigTransferRule()}); err == nil {
		t.Fatal("overlapping bands were accepted")
	}

	id, err := f.svc.CreateAuthorityMatrix(f.ctx, w.owner, "Delegation of authority 2026", []AuthorityRule{bigTransferRule()})
	must(t, "create matrix", err)
	// The people who prepare a matrix can read it (with its rules) without
	// being able to read procurement documents; project managers cannot read it.
	d, err := f.svc.GetP2PDoc(f.ctx, w.owner, "authority_matrix", id)
	must(t, "owner reads matrix", err)
	if !strings.Contains(string(d.Detail), "approval.high_value") {
		t.Fatalf("the matrix document does not show its rules: %s", d.Detail)
	}
	if _, err := f.svc.ListP2PDocs(f.ctx, w.owner, "purchase_order", "", 10); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the process owner can list purchase orders: %v", err)
	}
	if _, err := f.svc.GetP2PDoc(f.ctx, c.pm, "authority_matrix", id); err == nil {
		t.Fatal("a project manager read the authority matrix")
	}
	must(t, "submit matrix", f.svc.SubmitAuthorityMatrix(f.ctx, w.owner, id))
	if err := f.svc.DecideAuthorityMatrix(f.ctx, w.owner, id, true, ""); err == nil {
		t.Fatal("the author approved their own matrix")
	}
	stale := w.cfo
	stale.AuthTime = time.Now().Add(-time.Hour) // approval needs a fresh multi-factor sign-in
	if err := f.svc.DecideAuthorityMatrix(f.ctx, stale, id, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("approval without fresh MFA: %v", err)
	}
	must(t, "approve matrix", f.svc.DecideAuthorityMatrix(f.ctx, w.cfo, id, true, ""))

	// A new version retires the old one; exactly one is in force.
	id2 := w.activateMatrix(t, bigTransferRule())
	cur, err := f.svc.CurrentAuthorityMatrix(f.ctx, c.pm)
	must(t, "current matrix", err)
	if cur == nil || cur["id"] != id2 {
		t.Fatalf("matrix in force = %v, want %s", cur, id2)
	}
	var approved int
	must(t, "count", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM authority_matrices WHERE status = 'APPROVED'`).Scan(&approved)
	}))
	if approved != 1 {
		t.Fatalf("%d matrices approved at once", approved)
	}

	// Budget transfer of 150,000 needs approval.high_value: a commercial manager
	// is refused by the DATABASE; the CFO (who holds it) approves.
	trf, err := f.svc.RequestBudgetTransfer(f.ctx, c.pm, BudgetTransferRequest{ProjectID: f.project, FromCostCode: w.mat, ToCostCode: w.fin,
		Amount: dec("150000"), Reason: "Large reallocation"})
	must(t, "request", err)
	must(t, "submit", f.svc.SubmitBudgetTransfer(f.ctx, c.pm, trf))
	err = f.svc.DecideBudgetTransfer(f.ctx, w.commercial, trf, true, "ok")
	if err == nil {
		t.Fatal("a commercial manager approved a transfer above the matrix limit")
	}
	t.Logf("held: above-limit approval refused: %v", err)
	// Small transfers are not limited.
	small, err := f.svc.RequestBudgetTransfer(f.ctx, c.pm, BudgetTransferRequest{ProjectID: f.project, FromCostCode: w.mat, ToCostCode: w.fin,
		Amount: dec("99999.99"), Reason: "Below the band"})
	must(t, "request small", err)
	must(t, "submit small", f.svc.SubmitBudgetTransfer(f.ctx, c.pm, small))
	must(t, "approve small", f.svc.DecideBudgetTransfer(f.ctx, w.commercial, small, true, "ok"))
	// The CFO holds budget.approve and approval.high_value; with fresh MFA the large one passes.
	must(t, "CFO approves large", f.svc.DecideBudgetTransfer(f.ctx, w.cfo, trf, true, "within my authority"))
	// ... but a stale sign-in is refused where the band requires MFA.
	trf2, err := f.svc.RequestBudgetTransfer(f.ctx, c.pm, BudgetTransferRequest{ProjectID: f.project, FromCostCode: w.mat, ToCostCode: w.fin,
		Amount: dec("120000"), Reason: "Another large one"})
	must(t, "request 2", err)
	must(t, "submit 2", f.svc.SubmitBudgetTransfer(f.ctx, c.pm, trf2))
	if err := f.svc.DecideBudgetTransfer(f.ctx, stale, trf2, true, "stale"); err == nil {
		t.Fatal("a band that requires fresh MFA accepted a stale sign-in")
	}
}

// The database applies the matrix itself: a tenant login with raw SQL cannot
// activate a matrix, retire the one in force, or edit its rules.
func TestAuthority_DatabaseRefusesBypass(t *testing.T) {
	w := newAuthorityWorld(t)
	f := w.f
	id := w.activateMatrix(t, bigTransferRule())
	draft, err := f.svc.CreateAuthorityMatrix(f.ctx, w.owner, "Draft", []AuthorityRule{bigTransferRule()})
	must(t, "draft", err)
	must(t, "submit draft", f.svc.SubmitAuthorityMatrix(f.ctx, w.owner, draft))

	for name, q := range map[string]string{
		"approve a matrix without an attestation":   `UPDATE authority_matrices SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '` + draft + `'`,
		"retire the matrix in force":                `UPDATE authority_matrices SET status = 'RETIRED' WHERE id = '` + id + `'`,
		"raise a rule's limit":                      `UPDATE authority_rules SET min_amount = 1e12 WHERE matrix_id = '` + id + `'`,
		"delete a rule":                             `DELETE FROM authority_rules WHERE matrix_id = '` + id + `'`,
		"add a rule to an approved matrix":          `INSERT INTO authority_rules (matrix_id, doc_type, min_amount, currency, required_perm) VALUES ('` + id + `', 'purchase_order', 1, 'ETB', 'po.approve')`,
		"insert an already-approved matrix":         `INSERT INTO authority_matrices (version, reason, status, created_by, approved_by) VALUES (99, 'forged', 'APPROVED', 'a', 'b')`,
		"insert an approved delegation":             `INSERT INTO delegations (delegator, delegate, perms, valid_from, valid_to, reason, status, created_by, approved_by) VALUES ('a', 'b', ARRAY['po.approve'], now(), now() + interval '1 day', 'forged!!', 'APPROVED', 'a', 'c')`,
		"delegate the right to approve delegations": `INSERT INTO delegations (delegator, delegate, perms, valid_from, valid_to, reason, created_by) VALUES ('a', 'b', ARRAY['delegation.approve'], now(), now() + interval '1 day', 'escalate', 'a')`,
		"delegate for longer than 90 days":          `INSERT INTO delegations (delegator, delegate, perms, valid_from, valid_to, reason, created_by) VALUES ('a', 'b', ARRAY['po.approve'], now(), now() + interval '91 days', 'forever', 'a')`,
		"delegate to yourself":                      `INSERT INTO delegations (delegator, delegate, perms, valid_from, valid_to, reason, created_by) VALUES ('a', 'a', ARRAY['po.approve'], now(), now() + interval '1 day', 'myself', 'a')`,
		"lend a platform marker":                    `INSERT INTO delegations (delegator, delegate, perms, valid_from, valid_to, reason, created_by) VALUES ('a', 'b', ARRAY['principal.human'], now(), now() + interval '1 day', 'marker', 'a')`,
		"insert an approved SoD exception":          `INSERT INTO sod_exceptions (user_id, perm_a, perm_b, mitigating_control, expires_at, status, created_by, approved_by) VALUES ('u', 'a', 'b', 'a long enough written control', now() + interval '1 day', 'APPROVED', 'x', 'y')`,
		"SoD exception for longer than a year":      `INSERT INTO sod_exceptions (user_id, perm_a, perm_b, mitigating_control, expires_at, created_by) VALUES ('u', 'a', 'b', 'a long enough written control', now() + interval '400 days', 'x')`,
	} {
		err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, q)
			if err == nil && ct.RowsAffected() == 0 {
				return errors.New("no rows (refused by visibility)")
			}
			return err
		})
		if err == nil {
			t.Fatalf("database accepted: %s", name)
		}
		t.Logf("held: %s: %v", name, err)
	}
	// The other company sees nothing.
	var n int
	must(t, "tenant2", f.app.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM authority_matrices) + (SELECT count(*) FROM delegations) + (SELECT count(*) FROM sod_exceptions)`).Scan(&n)
	}))
	if n != 0 {
		t.Fatalf("tenant2 reads %d governance rows of tenant1", n)
	}
}

func (w *authorityWorld) delegation(t *testing.T, from auth.Principal, to string, perms ...string) string {
	t.Helper()
	f := w.f
	id, err := f.svc.CreateDelegation(f.ctx, from, DelegationRequest{Delegate: to, Perms: perms, ValidFrom: time.Now().Add(-time.Minute),
		ValidTo: time.Now().Add(7 * 24 * time.Hour), Reason: "Annual leave cover"})
	must(t, "create delegation", err)
	must(t, "submit delegation", f.svc.SubmitDelegation(f.ctx, from, id))
	return id
}

func TestAuthority_DelegationLifecycle(t *testing.T) {
	w := newAuthorityWorld(t)
	f, c := w.f, w.cast
	w.approvedOriginal(t)

	// What may not be lent.
	if _, err := f.svc.CreateDelegation(f.ctx, w.commercial, DelegationRequest{Delegate: w.planner.UserID, Perms: []string{"budget.override"},
		ValidFrom: time.Now(), ValidTo: time.Now().Add(time.Hour), Reason: "Leave cover"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a non-delegable permission was lent: %v", err)
	}
	if _, err := f.svc.CreateDelegation(f.ctx, c.pm, DelegationRequest{Delegate: w.planner.UserID, Perms: []string{"budget.approve"},
		ValidFrom: time.Now(), ValidTo: time.Now().Add(time.Hour), Reason: "Leave cover"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a permission the delegator does not hold was lent: %v", err)
	}
	if _, err := f.svc.CreateDelegation(f.ctx, w.commercial, DelegationRequest{Delegate: w.commercial.UserID, Perms: []string{"budget.approve"},
		ValidFrom: time.Now(), ValidTo: time.Now().Add(time.Hour), Reason: "Leave cover"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("delegation to self: %v", err)
	}

	// Commercial manager lends budget.approve to the planner.
	id := w.delegation(t, w.commercial, w.planner.UserID, "budget.approve")
	// Approver must be a third person with delegation.approve and fresh MFA.
	if err := f.svc.DecideDelegation(f.ctx, w.commercial, id, true, ""); err == nil {
		t.Fatal("the delegator approved their own delegation")
	}
	if err := f.svc.DecideDelegation(f.ctx, w.cfo, id, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the CFO (no delegation.approve) approved a delegation: %v", err)
	}
	// Not yet in force while only submitted.
	if got := f.svc.ApplyDelegations(f.ctx, w.planner); got.Delegated != nil {
		t.Fatalf("an unapproved delegation was applied: %v", got.Delegated)
	}
	must(t, "approve delegation", f.svc.DecideDelegation(f.ctx, w.secAdmin, id, true, ""))

	// The planner can now decide a budget revision prepared by the project manager.
	rev, err := f.svc.CreateBudgetRevision(f.ctx, c.pm, f.project, "REVISION", "Scope added", []BudgetLine{{CostCodeID: w.mat, Amount: dec("10000")}})
	must(t, "revision", err)
	must(t, "submit revision", f.svc.SubmitBudgetRevision(f.ctx, c.pm, rev))
	if err := f.svc.DecideBudgetRevision(f.ctx, w.planner, rev, true, "x"); err == nil {
		t.Fatal("the planner approved without applying the delegation (permission is not in the token)")
	}
	planner := f.svc.ApplyDelegations(f.ctx, w.planner)
	if len(planner.Delegated[auth.PermBudgetApprove]) != 1 || planner.Delegated[auth.PermBudgetApprove][0] != w.commercial.UserID {
		t.Fatalf("delegated = %v", planner.Delegated)
	}
	must(t, "planner approves by delegation", f.svc.DecideBudgetRevision(f.ctx, planner, rev, true, "approved under delegation from the commercial manager"))

	// A delegate never decides what the delegator prepared: the delegator (also a
	// budget preparer here) prepares, the delegate may not approve.
	dual := auth.Principal{Kind: auth.KindHuman, UserID: w.commercial.UserID, TenantID: f.tenant, Roles: []string{"commercial_manager", "project_manager"}, ProjectIDs: []string{f.project}}
	rev2, err := f.svc.CreateBudgetRevision(f.ctx, dual, f.project, "REVISION", "Prepared by the delegator", []BudgetLine{{CostCodeID: w.fin, Amount: dec("5000")}})
	must(t, "revision 2", err)
	must(t, "submit revision 2", f.svc.SubmitBudgetRevision(f.ctx, dual, rev2))
	if err := f.svc.DecideBudgetRevision(f.ctx, planner, rev2, true, "proxy approval"); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("a delegate approved their delegator's own document: %v", err)
	}

	// Revocation by the delegator ends it at once.
	if err := f.svc.RevokeDelegation(f.ctx, w.planner, id, "not mine to revoke"); err == nil {
		t.Fatal("the delegate revoked a delegation they do not own")
	}
	must(t, "revoke", f.svc.RevokeDelegation(f.ctx, w.commercial, id, "Back from leave early"))
	if got := f.svc.ApplyDelegations(f.ctx, w.planner); got.Delegated != nil {
		t.Fatalf("a revoked delegation still applies: %v", got.Delegated)
	}
	if err := f.svc.RevokeDelegation(f.ctx, w.commercial, id, "again"); err == nil {
		t.Fatal("a revoked delegation was revoked twice")
	}
	// Listing: the planner sees what was lent to them.
	list, err := f.svc.MyDelegations(f.ctx, w.planner)
	must(t, "list", err)
	if len(list) != 1 || list[0]["status"] != "REVOKED" {
		t.Fatalf("planner's delegations = %v", list)
	}
	other := auth.Principal{Kind: auth.KindHuman, UserID: "u-stranger", TenantID: f.tenant, Roles: []string{"estimator"}}
	if l, _ := f.svc.MyDelegations(f.ctx, other); len(l) != 0 {
		t.Fatalf("a stranger sees %d delegations", len(l))
	}
}

func TestAuthority_ConflictingDelegationIsDroppedUnlessExcepted(t *testing.T) {
	w := newAuthorityWorld(t)
	f, c := w.f, w.cast

	// The project manager prepares budgets (budget.revise). Lending them
	// budget.approve would let one person prepare and approve: conflict.
	id := w.delegation(t, w.commercial, c.pm.UserID, "budget.approve")
	must(t, "approve delegation", f.svc.DecideDelegation(f.ctx, w.secAdmin, id, true, ""))
	if got := f.svc.ApplyDelegations(f.ctx, c.pm); got.Delegated != nil {
		t.Fatalf("a delegation creating a duty conflict was applied: %v", got.Delegated)
	}

	// An exception needs a real conflict, a written control, and a third person.
	if _, err := f.svc.CreateSoDException(f.ctx, w.owner, SoDExceptionRequest{UserID: c.pm.UserID, PermA: "po.approve", PermB: "budget.revise",
		MitigatingControl: "Every decision is re-reviewed by the CFO weekly.", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an exception for a pair that is not a conflict: %v", err)
	}
	ex, err := f.svc.CreateSoDException(f.ctx, w.owner, SoDExceptionRequest{UserID: c.pm.UserID, PermA: "budget.revise", PermB: "budget.approve",
		MitigatingControl: "Every budget decision of this person is re-reviewed by the CFO weekly.", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)})
	must(t, "exception", err)
	must(t, "submit exception", f.svc.SubmitSoDException(f.ctx, w.owner, ex))
	if err := f.svc.DecideSoDException(f.ctx, w.owner, ex, true, ""); err == nil {
		t.Fatal("the author approved their own exception")
	}
	// Not in force until approved.
	if got := f.svc.ApplyDelegations(f.ctx, c.pm); got.Delegated != nil {
		t.Fatalf("an unapproved exception was honoured: %v", got.Delegated)
	}
	must(t, "approve exception", f.svc.DecideSoDException(f.ctx, w.cfo, ex, true, ""))
	// budget.approve conflicts with TWO things the project manager holds
	// (budget.revise and budget.transfer): one exception is not enough.
	if got := f.svc.ApplyDelegations(f.ctx, c.pm); got.Delegated != nil {
		t.Fatalf("an exception for one of two conflicts allowed the delegation: %v", got.Delegated)
	}
	ex2, err := f.svc.CreateSoDException(f.ctx, w.owner, SoDExceptionRequest{UserID: c.pm.UserID, PermA: "budget.transfer", PermB: "budget.approve",
		MitigatingControl: "Every budget decision of this person is re-reviewed by the CFO weekly.", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)})
	must(t, "exception 2", err)
	must(t, "submit exception 2", f.svc.SubmitSoDException(f.ctx, w.owner, ex2))
	must(t, "approve exception 2", f.svc.DecideSoDException(f.ctx, w.cfo, ex2, true, ""))
	got := f.svc.ApplyDelegations(f.ctx, c.pm)
	if len(got.Delegated[auth.PermBudgetApprove]) != 1 {
		t.Fatalf("approved exceptions did not allow the delegation: %v", got.Delegated)
	}
	// The maker/checker rule is NOT waived: the delegate still cannot approve what the delegator prepared.
	if err := auth.CheckMakerChecker(got, w.commercial.UserID, "budget revision"); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("exception waived the delegation self-approval rule: %v", err)
	}
	// The conflict report lists the catalogue's own conflicts (none in the seed roles).
	rep, err := f.svc.SoDReport(f.ctx, w.owner)
	must(t, "report", err)
	if len(rep) != 0 {
		t.Fatalf("the seed role catalogue has duty conflicts: %+v", rep)
	}
	if _, err := f.svc.SoDReport(f.ctx, c.estimator); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator read the conflict report: %v", err)
	}
}

// The kernel applies the matrix to purchase orders (not only budget documents).
func TestAuthority_MatrixLimitsPurchaseOrders(t *testing.T) {
	pw := newP2PWorld(t) // the plain P2P world: its WBS and BOQ cost codes line up
	f, c := pw.f, pw.cast
	pw.approvedBOQ(t)
	_, _, po := pw.draftPO(t, "10", "100")

	now := time.Now()
	owner := auth.Principal{Kind: auth.KindHuman, UserID: "u-process-owner-2", TenantID: f.tenant, Roles: []string{"process_owner"}, ProjectIDs: []string{f.project}}
	rule := AuthorityRule{DocType: "purchase_order", Min: decimal.Zero, Currency: "ETB", RequiredPerm: string(auth.PermApprovalHighValue)}
	mid, err := f.svc.CreateAuthorityMatrix(f.ctx, owner, "Every PO needs high-value authority", []AuthorityRule{rule})
	must(t, "matrix", err)
	must(t, "submit matrix", f.svc.SubmitAuthorityMatrix(f.ctx, owner, mid))
	must(t, "approve matrix", f.svc.DecideAuthorityMatrix(f.ctx, c.cfo, mid, true, ""))

	// The procurement manager holds po.approve but not approval.high_value.
	err = f.svc.ApprovePO(f.ctx, c.procMgr, po, "")
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the procurement manager approved a PO the matrix reserves: %v", err)
	}
	t.Logf("held: %v", err)
	// Someone holding both permissions (here one person with both roles) passes.
	both := auth.Principal{Kind: auth.KindHuman, UserID: "u-proc-cfo", TenantID: f.tenant, Roles: []string{"procurement_manager", "cfo"}, ProjectIDs: []string{f.project}}
	both.MFA, both.AuthTime = true, now
	must(t, "approve with authority", f.svc.ApprovePO(f.ctx, both, po, ""))
}
