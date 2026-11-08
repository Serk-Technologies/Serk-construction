package kernel

// Tests for people and access (migration 0048, kernel/people.go). Proven:
//  1. a signed-in person is recorded in the directory with their token's roles;
//     while not managed, the token stays the authority;
//  2. only user/role administrators (or auditors, read only) see the directory;
//  3. taking a person under management copies their sign-in roles and projects,
//     then the directory replaces the token on the next request;
//  4. nobody changes their own access (kernel and database); changes need a
//     fresh MFA sign-in and a reason;
//  5. a role is never given if it creates a duty conflict, and a company role
//     can neither hold a conflict itself nor be changed into one for a holder;
//  6. removal keeps history; a held role cannot be retired;
//  7. disabling refuses every request and revokes sessions; enabling restores;
//  8. a person sees and edits their own profile; companies stay isolated.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestPeople_DirectoryRolesAndProfile(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	fresh := func(p auth.Principal) auth.Principal {
		p.Kind, p.MFA, p.AuthTime, p.IssuedAt = auth.KindHuman, true, now, now.Add(-time.Minute)
		return p
	}
	admin := fresh(auth.Principal{UserID: "u-secadmin", TenantID: f.tenant, Roles: []string{"security_admin"}})
	admin2 := fresh(auth.Principal{UserID: "u-secadmin-2", TenantID: f.tenant, Roles: []string{"security_admin"}})
	auditor := fresh(auth.Principal{UserID: "u-auditor", TenantID: f.tenant, Roles: []string{"auditor"}})
	pm := fresh(auth.Principal{UserID: "u-pm", TenantID: f.tenant, Roles: []string{"project_manager"}, ProjectIDs: []string{f.project}})
	authz := auth.NewAuthorizer(nil, true)

	// 1. Sign-in bookkeeping; the token stays the authority.
	for _, p := range []auth.Principal{admin, admin2, pm} {
		must(t, "touch", f.svc.Touch(f.ctx, p))
	}
	got, err := f.svc.ApplyDirectory(f.ctx, pm)
	must(t, "apply unmanaged", err)
	if len(got.Roles) != 1 || got.Roles[0] != "project_manager" || len(got.ProjectIDs) != 1 {
		t.Fatalf("unmanaged person changed: %+v", got)
	}

	// 2. Who may see the directory.
	if _, err := f.svc.People(f.ctx, pm); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager listed people: %v", err)
	}
	v, err := f.svc.People(f.ctx, auditor)
	must(t, "auditor lists", err)
	if v.Me.CanManageUsers || v.Me.CanManageRoles {
		t.Fatal("the auditor may manage")
	}
	var seen *Person
	for i := range v.People {
		if v.People[i].UserID == "u-pm" {
			seen = &v.People[i]
		}
	}
	if seen == nil || seen.RoleSource != "sign-in" || len(seen.Roles) != 1 || seen.Stage != auth.StageApprove || len(seen.Conflicts) != 0 {
		t.Fatalf("pm in directory: %+v", seen)
	}
	if len(v.Permissions) < 100 || len(v.Roles) < 20 {
		t.Fatalf("catalogue: %d permissions, %d roles", len(v.Permissions), len(v.Roles))
	}

	// 3. Take the PM under management: nothing changes at the switch.
	if _, err := f.svc.ManagePerson(f.ctx, admin, "u-pm", "no"); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("manage without reason: %v", err)
	}
	stale := admin
	stale.AuthTime = now.Add(-time.Hour)
	if _, err := f.svc.ManagePerson(f.ctx, stale, "u-pm", "joining the HQ directory"); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("manage without fresh MFA: %v", err)
	}
	x, err := f.svc.ManagePerson(f.ctx, admin, "u-pm", "joining the HQ directory")
	must(t, "manage", err)
	if !x.Managed || x.RoleSource != "directory" || len(x.Grants) != 1 || len(x.ProjectIDs) != 1 {
		t.Fatalf("managed pm: %+v", x)
	}
	// The token now claims more than the directory gives: the directory wins.
	greedy := pm
	greedy.Roles = []string{"project_manager", "cfo"}
	greedy.ProjectIDs = nil
	got, err = f.svc.ApplyDirectory(f.ctx, greedy)
	must(t, "apply managed", err)
	if len(got.Roles) != 1 || got.Roles[0] != "project_manager" || len(got.ProjectIDs) != 1 || got.ProjectIDs[0] != f.project {
		t.Fatalf("directory did not replace the token: %+v", got)
	}

	// 4. Nobody changes their own access.
	must(t, "manage admin", errOnly(f.svc.ManagePerson(f.ctx, admin2, "u-secadmin", "administrators are managed too")))
	if _, err := f.svc.GrantRole(f.ctx, admin, "u-secadmin", "cfo", "I would like more power"); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("self grant: %v", err)
	}
	if _, err := f.svc.GrantRole(f.ctx, pm, "u-secadmin", "auditor", "not an administrator"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("non-admin grant: %v", err)
	}
	dbRefuses(t, f, "self-granted role", `INSERT INTO role_grants (user_id, role, reason, granted_by) VALUES ('u-pm', 'cfo', 'raw insert', 'u-pm')`)
	dbRefuses(t, f, "self-disable via SQL", `UPDATE directory_users SET status = 'DISABLED', status_reason = 'raw sql', updated_by = 'u-pm', updated_at = now() WHERE user_id = 'u-pm'`)
	dbRefuses(t, f, "deleting a person", `DELETE FROM directory_users WHERE user_id = 'u-pm'`)
	dbRefuses(t, f, "unmanaging", `UPDATE directory_users SET managed = false, updated_by = 'u-secadmin', updated_at = now() WHERE user_id = 'u-pm'`)

	// 5. Duty conflicts: commercial manager approves what a PM prepares.
	if _, err := f.svc.GrantRole(f.ctx, admin, "u-pm", "commercial_manager", "covering the commercial desk"); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("conflicting grant: %v", err)
	}
	x, err = f.svc.GrantRole(f.ctx, admin, "u-pm", "auditor", "quarterly review support")
	must(t, "grant auditor", err)
	if len(x.Roles) != 2 {
		t.Fatalf("roles after grant: %v", x.Roles)
	}
	// Company roles.
	if _, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "buyer_all", Name: "Buyer", Permissions: []auth.Permission{auth.PermPOManage, auth.PermPOApprove}, Reason: "one person buys"}); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("conflicting company role: %v", err)
	}
	if _, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "x_role", Name: "X", Permissions: []auth.Permission{"po.everything"}, Reason: "unknown permission"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown permission: %v", err)
	}
	if _, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "auditor", Name: "Auditor", Permissions: []auth.Permission{auth.PermP2PRead}, Reason: "shadow a product role"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("seed code reused: %v", err)
	}
	if _, err := f.svc.SaveRole(f.ctx, pm, RoleInput{Code: "store_helper", Name: "Store helper", Permissions: []auth.Permission{auth.PermWarehouseManage}, Reason: "not a role admin"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("non-admin role: %v", err)
	}
	r, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "store_helper", Name: "Store helper", Description: "Keeps the site store tidy",
		Permissions: []auth.Permission{auth.PermWarehouseManage, auth.PermP2PRead}, Reason: "site stores need a helper"})
	must(t, "company role", err)
	if r.Code != "custom:store_helper" || r.Kind != "company" || r.Version != 1 {
		t.Fatalf("company role: %+v", r)
	}
	if authz.Has(got, auth.PermWarehouseManage) {
		t.Fatal("pm already maintains warehouses; pick another permission")
	}
	must(t, "grant company role", errOnly(f.svc.GrantRole(f.ctx, admin, "u-pm", "custom:store_helper", "helps the store on site")))
	got, err = f.svc.ApplyDirectory(f.ctx, pm)
	must(t, "apply with company role", err)
	if !authz.Has(got, auth.PermWarehouseManage) || !authz.Has(got, auth.PermAuditRead) {
		t.Fatalf("company role or auditor not applied: roles %v custom %v", got.Roles, got.Custom)
	}
	// Changing the role so a holder would both raise and approve requisitions.
	if _, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "store_helper", Name: "Store helper", Version: 1,
		Permissions: []auth.Permission{auth.PermWarehouseManage, auth.PermPRCreate}, Reason: "add requisitions"}); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("role change creating a holder conflict: %v", err)
	}
	if _, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "store_helper", Name: "Store helper", Version: 7,
		Permissions: []auth.Permission{auth.PermWarehouseManage}, Reason: "stale version"}); err == nil {
		t.Fatal("stale role version accepted")
	}
	r, err = f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "store_helper", Name: "Store keeper's helper", Version: 1,
		Permissions: []auth.Permission{auth.PermWarehouseManage, auth.PermStockCount}, Reason: "helpers count stock too"})
	must(t, "change company role", err)
	if r.Version != 2 || len(r.Permissions) != 2 {
		t.Fatalf("changed role: %+v", r)
	}

	// 6. Removal keeps history; a held role cannot be retired.
	if err := f.svc.RetireRole(f.ctx, admin, "store_helper", "no longer needed"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("retired a held role: %v", err)
	}
	x, err = f.svc.PersonByID(f.ctx, admin, "u-pm")
	must(t, "person", err)
	for _, g := range x.Grants {
		if g.Role == "custom:store_helper" || g.Role == "auditor" {
			must(t, "revoke", errOnly(f.svc.RevokeRole(f.ctx, admin, g.ID, "review support finished")))
		}
	}
	must(t, "retire", f.svc.RetireRole(f.ctx, admin, "store_helper", "no longer needed"))
	if _, err := f.svc.GrantRole(f.ctx, admin, "u-pm", "custom:store_helper", "retired role"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("granted a retired role: %v", err)
	}
	_, hist, events, err := f.svc.GetPerson(f.ctx, auditor, "u-pm")
	must(t, "history", err)
	if len(hist) != 3 || len(events) < 5 {
		t.Fatalf("grant history %d, events %d", len(hist), len(events))
	}
	dbRefuses(t, f, "reviving a removed grant", `UPDATE role_grants SET revoked_at = NULL, revoked_by = NULL WHERE user_id = 'u-pm' AND role = 'auditor'`)

	// 7. Disable: every request refused, sessions revoked; enable restores.
	if _, err := f.svc.SetPersonStatus(f.ctx, admin, "u-pm", false, "left the company"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := f.svc.ApplyDirectory(f.ctx, pm); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled person accepted: %v", err)
	}
	if err := f.svc.CheckSession(f.ctx, pm); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("session of a disabled person still valid: %v", err)
	}
	if _, err := f.svc.GrantRole(f.ctx, admin, "u-pm", "auditor", "disabled people get nothing"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("granted to a disabled person: %v", err)
	}
	must(t, "enable", errOnly(f.svc.SetPersonStatus(f.ctx, admin, "u-pm", true, "rehired on contract")))
	if _, err := f.svc.ApplyDirectory(f.ctx, pm); err != nil {
		t.Fatalf("enabled person refused: %v", err)
	}

	// 8. Profile, pre-registration and isolation.
	pmNow := fresh(pm)
	pmNow.IssuedAt = time.Now().Add(time.Second) // a sign-in after the revocation
	pmNow, err = f.svc.ApplyDirectory(f.ctx, pmNow)
	must(t, "apply", err)
	if _, err := f.svc.UpdateMyProfile(f.ctx, pmNow, ProfileInput{DisplayName: ""}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank name: %v", err)
	}
	prof, err := f.svc.UpdateMyProfile(f.ctx, pmNow, ProfileInput{DisplayName: "Hagos Tesfay", JobTitle: "Project manager, Mekelle", Phone: "+251 911 000 000"})
	must(t, "profile", err)
	if prof.DisplayName != "Hagos Tesfay" || prof.RoleSource != "directory" || len(prof.Roles) != 1 || len(prof.Projects) != 1 || len(prof.Permissions) < 10 || len(prof.Activity) == 0 {
		t.Fatalf("profile: %+v", prof)
	}
	if _, err := f.svc.AddPerson(f.ctx, admin, PersonInput{UserID: "u-pm", DisplayName: "Again", Reason: "duplicate entry"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate person: %v", err)
	}
	np, err := f.svc.AddPerson(f.ctx, admin, PersonInput{UserID: "u-new-planner", DisplayName: "Selam Gebru", Email: "selam@example.com",
		JobTitle: "Planner", Roles: []string{"planner"}, ProjectIDs: &[]string{f.project}, Reason: "new hire, HR ticket 41"})
	must(t, "add person", err)
	if !np.Managed || len(np.Roles) != 1 || np.Roles[0] != "planner" || len(np.ProjectIDs) != 1 {
		t.Fatalf("new person: %+v", np)
	}
	if np.FirstSeen != nil {
		t.Fatal("a person added before signing in has a first-seen date")
	}
	planner := fresh(auth.Principal{UserID: "u-new-planner", TenantID: f.tenant, Roles: []string{"planner"}})
	must(t, "first sign-in", f.svc.Touch(f.ctx, planner))
	np, err = f.svc.PersonByID(f.ctx, admin, "u-new-planner")
	must(t, "after sign-in", err)
	if np.FirstSeen == nil || np.LastSeen == nil || !np.Managed || len(np.Roles) != 1 {
		t.Fatalf("first sign-in of a pre-registered person: %+v", np)
	}
	dbRefuses(t, f, "rewriting first seen", `UPDATE directory_users SET first_seen = now() - interval '1 year' WHERE user_id = 'u-new-planner'`)
	other := fresh(auth.Principal{UserID: "u-secadmin-t2", TenantID: f.tenant2, Roles: []string{"security_admin"}})
	ov, err := f.svc.People(f.ctx, other)
	must(t, "other company", err)
	if len(ov.People) != 0 {
		t.Fatalf("another company sees %d people", len(ov.People))
	}
}

// PersonByID is a test helper: one person as an administrator sees them.
func (s *Service) PersonByID(ctx context.Context, p auth.Principal, id string) (Person, error) {
	x, _, _, err := s.GetPerson(ctx, p, id)
	return x, err
}

func errOnly(_ Person, err error) error { return err }

func dbRefuses(t *testing.T, f *fixture, what, sql string) {
	t.Helper()
	err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql)
		return err
	})
	if err == nil {
		t.Fatalf("the database allowed %s", what)
	}
}
