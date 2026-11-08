package auth

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestDefaultRolesSeparateDuties(t *testing.T) {
	a := NewAuthorizer(nil, true)
	for name := range DefaultRoles {
		p := Principal{UserID: "u", TenantID: "t", Roles: []string{name}}
		for _, c := range DefaultSoDConflicts {
			set := c.Perms
			n := 0
			for _, perm := range set {
				if a.Has(p, perm) {
					n++
				}
			}
			if n > 1 {
				t.Fatalf("role %s holds %d of %v", name, n, set)
			}
		}
	}
}

func TestRequire(t *testing.T) {
	a := NewAuthorizer(nil, true)
	acc := Principal{UserID: "u1", TenantID: "t1", Roles: []string{"accountant"}}
	if err := a.Require(acc, PermJournalCreate, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Require(acc, PermJournalPost, "t1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want forbidden got %v", err)
	}
	if err := a.Require(acc, PermJournalCreate, "t2"); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("want tenant mismatch got %v", err)
	}
	if err := a.Require(Principal{}, PermJournalRead, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("want unauthenticated got %v", err)
	}
	unknownRole := Principal{UserID: "u", TenantID: "t", Roles: []string{"ghost"}}
	if len(a.Permissions(unknownRole)) != 0 {
		t.Fatal("unknown role granted permissions")
	}
}

// SC-05: tenant-wide access is an explicit entitlement, never implied by an
// empty project list, and a project-limited session never reaches
// unassigned (tenant-wide) records.
func TestScopeSC05(t *testing.T) {
	a := NewAuthorizer(nil, true)
	cases := []struct {
		name    string
		p       Principal
		project string
		allowed bool
	}{
		{"tenant-wide accountant, unassigned", Principal{Roles: []string{"accountant"}}, "", true},
		{"tenant-wide accountant, any project", Principal{Roles: []string{"accountant"}}, "p9", true},
		{"accountant limited to p1, unassigned", Principal{Roles: []string{"accountant"}, ProjectIDs: []string{"p1"}}, "", false},
		{"accountant limited to p1, p1", Principal{Roles: []string{"accountant"}, ProjectIDs: []string{"p1"}}, "p1", true},
		{"accountant limited to p1, p2", Principal{Roles: []string{"accountant"}, ProjectIDs: []string{"p1"}}, "p2", false},
		{"project accountant, no projects, unassigned", Principal{Roles: []string{"project_accountant"}}, "", false},
		{"project accountant, no projects, p1", Principal{Roles: []string{"project_accountant"}}, "p1", false},
		{"project accountant, p1+p2 mixed, p2", Principal{Roles: []string{"project_accountant"}, ProjectIDs: []string{"p1", "p2"}}, "p2", true},
		{"project accountant, p1+p2 mixed, unassigned", Principal{Roles: []string{"project_accountant"}, ProjectIDs: []string{"p1", "p2"}}, "", false},
		{"inherited roles: PM + accountant limited to p1, unassigned", Principal{Roles: []string{"project_manager", "accountant"}, ProjectIDs: []string{"p1"}}, "", false},
	}
	for _, c := range cases {
		c.p.UserID, c.p.TenantID = "u", "t"
		perm := PermJournalCreate
		err := a.RequireProject(c.p, perm, "t", c.project)
		if c.allowed && err != nil {
			t.Errorf("%s: denied: %v", c.name, err)
		}
		if !c.allowed && !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: allowed (err=%v)", c.name, err)
		}
	}
	// Read access follows the same scope rule.
	pm := Principal{UserID: "u", TenantID: "t", Roles: []string{"project_manager"}, ProjectIDs: []string{"p1"}}
	if err := a.RequireProject(pm, PermJournalRead, "t", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("scoped PM read an unassigned journal: %v", err)
	}
}

func TestStepUp(t *testing.T) {
	a := NewAuthorizer(nil, true)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	if err := a.RequireStepUp(Principal{MFA: false, AuthTime: now}); !errors.Is(err, ErrStepUpRequired) {
		t.Fatal("no-MFA session passed step-up")
	}
	if err := a.RequireStepUp(Principal{MFA: true, AuthTime: now.Add(-11 * time.Minute)}); !errors.Is(err, ErrStepUpRequired) {
		t.Fatal("stale MFA passed step-up")
	}
	if err := a.RequireStepUp(Principal{MFA: true}); !errors.Is(err, ErrStepUpRequired) {
		t.Fatal("MFA without auth_time passed step-up")
	}
	if err := a.RequireStepUp(Principal{MFA: true, AuthTime: now.Add(-2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
}

func TestSoD(t *testing.T) {
	strict := NewAuthorizer(nil, true)
	prep := Principal{UserID: "prep", TenantID: "t"}
	appr := Principal{UserID: "appr", TenantID: "t"}
	post := Principal{UserID: "post", TenantID: "t"}

	if err := strict.CheckSoDApprove(prep, JournalActors{CreatedBy: "prep"}); !errors.Is(err, ErrSoD) {
		t.Fatal("self-approval allowed")
	}
	if err := strict.CheckSoDApprove(appr, JournalActors{CreatedBy: "prep"}); err != nil {
		t.Fatal(err)
	}
	if err := strict.CheckSoDPost(appr, JournalActors{CreatedBy: "prep", ApprovedBy: "appr"}); !errors.Is(err, ErrSoD) {
		t.Fatal("approver posting allowed under strict mode")
	}
	if err := strict.CheckSoDPost(prep, JournalActors{CreatedBy: "prep", ApprovedBy: "appr"}); !errors.Is(err, ErrSoD) {
		t.Fatal("preparer posting allowed under strict mode")
	}
	if err := strict.CheckSoDPost(post, JournalActors{CreatedBy: "prep", ApprovedBy: "appr"}); err != nil {
		t.Fatal(err)
	}
	if err := strict.CheckSoDPost(post, JournalActors{CreatedBy: "prep"}); !errors.Is(err, ErrSoD) {
		t.Fatal("posting without approver allowed")
	}
	relaxed := NewAuthorizer(nil, false)
	if err := relaxed.CheckSoDPost(appr, JournalActors{CreatedBy: "prep", ApprovedBy: "appr"}); err != nil {
		t.Fatal("two-person rule should permit approver to post")
	}
	if err := CheckMakerChecker(Principal{UserID: "m"}, "m", "rate"); !errors.Is(err, ErrSoD) {
		t.Fatal("maker approved own record")
	}
}

// A tenant-defined role can never grant a platform-derived permission.
func TestReservedPermissionsNotGrantableByRoles(t *testing.T) {
	a := NewAuthorizer(map[string]Role{"bot": {Name: "bot", Permissions: []Permission{"principal.human", PermPaymentApprove}}}, true)
	p := Principal{UserID: "svc-x", TenantID: "t", Roles: []string{"bot"}}
	for _, perm := range a.Permissions(p) {
		if perm == "principal.human" {
			t.Fatal("role granted principal.human")
		}
	}
	if !a.Has(p, PermPaymentApprove) {
		t.Fatal("ordinary permission lost")
	}
}

// Delegated authority (0040): counts as the permission, but a delegate never
// decides what their delegator prepared, and some permissions are never lent.
func TestDelegatedAuthority(t *testing.T) {
	a := NewAuthorizer(nil, true)
	deleg := Principal{UserID: "u-delegate", TenantID: "t", Roles: []string{"project_manager"},
		Delegated: map[Permission][]string{PermBudgetApprove: {"u-boss"}}}
	if !a.Has(deleg, PermBudgetApprove) {
		t.Fatal("a delegated permission was not honoured")
	}
	if a.Has(Principal{UserID: "u", TenantID: "t", Roles: []string{"project_manager"}}, PermBudgetApprove) {
		t.Fatal("the project manager role must not hold budget.approve by itself")
	}
	found := false
	for _, p := range a.Permissions(deleg) {
		if p == PermBudgetApprove {
			found = true
		}
	}
	if !found {
		t.Fatal("delegated permission missing from Permissions (it would not be attested)")
	}
	if err := CheckMakerChecker(deleg, "u-boss", "budget revision"); !errors.Is(err, ErrSoD) {
		t.Fatalf("a delegate approved their delegator's document: %v", err)
	}
	if err := CheckMakerChecker(deleg, "u-someone-else", "budget revision"); err != nil {
		t.Fatalf("unrelated document refused: %v", err)
	}
	// Reserved markers can never be delegated into existence.
	bad := Principal{UserID: "u", TenantID: "t", Delegated: map[Permission][]string{ReservedPermissionPrefix + "human": {"x"}}}
	if a.Has(bad, Permission(ReservedPermissionPrefix+"human")) {
		t.Fatal("a reserved permission was delegated")
	}
}

func TestDelegationAllowed(t *testing.T) {
	a := NewAuthorizer(nil, true)
	pm := Principal{UserID: "u-pm", TenantID: "t", Roles: []string{"project_manager"}}
	if err := a.DelegationAllowed(pm, PermPRApprove); err != nil {
		t.Fatalf("a held, delegable permission was refused: %v", err)
	}
	if err := a.DelegationAllowed(pm, PermBudgetApprove); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delegated a permission the delegator does not hold: %v", err)
	}
	cfo := Principal{UserID: "u-cfo", TenantID: "t", Roles: []string{"cfo"}}
	for _, p := range []Permission{PermScopeTenantWide, PermAuthorityApprove, PermBudgetOverride, PermIdentityRevoke} {
		if err := a.DelegationAllowed(cfo, p); !errors.Is(err, ErrForbidden) {
			t.Fatalf("non-delegable %s was delegable: %v", p, err)
		}
	}
	// Re-delegation: what was lent cannot be lent on.
	lent := Principal{UserID: "u-x", TenantID: "t", Roles: []string{"estimator"}, Delegated: map[Permission][]string{PermPRApprove: {"u-pm"}}}
	if err := a.DelegationAllowed(lent, PermPRApprove); !errors.Is(err, ErrForbidden) {
		t.Fatalf("re-delegation allowed: %v", err)
	}
}

func TestSoDViolations(t *testing.T) {
	held := map[Permission]bool{PermPRCreate: true, PermPRApprove: true, PermJournalCreate: true}
	v := SoDViolations(held, nil)
	if len(v) != 1 || v[0].Control != "Requisitions" {
		t.Fatalf("violations = %+v", v)
	}
	accepted := map[[2]Permission]bool{{PermPRApprove, PermPRCreate}: true} // stored sorted: "pr.approve" < "pr.create"
	if got := SoDViolations(held, accepted); len(got) != 0 {
		t.Fatalf("an accepted exception still reported: %+v", got)
	}
}

// The role matrix document is generated from the catalogue
// (UPDATE_ROLE_DOC=1 rewrites docs/security/ROLE_PERMISSION_MATRIX.md).
func TestRoleMatrixDocumentIsCurrent(t *testing.T) {
	const path = "../../docs/security/ROLE_PERMISSION_MATRIX.md"
	want := RoleMatrixMarkdown()
	if os.Getenv("UPDATE_ROLE_DOC") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (generate it with UPDATE_ROLE_DOC=1)", err)
	}
	if string(got) != want {
		t.Fatal("docs/security/ROLE_PERMISSION_MATRIX.md is out of date; regenerate with UPDATE_ROLE_DOC=1")
	}
}
