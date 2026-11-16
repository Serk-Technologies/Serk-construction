package kernel

import (
	"strings"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestLeadershipRoles_SeparatedPowers: the system administrator runs the
// platform and holds no financial or approval power; the board chairman
// reads everything and acts on nothing; the CEO approves operations but
// never prepares, so every CEO approval is a second person's check.
func TestLeadershipRoles_SeparatedPowers(t *testing.T) {
	cat := auth.NewAuthorizer(nil, true).Catalogue()
	// Permissions that move money, create or decide business records.
	acting := func(p auth.Permission) bool {
		s := string(p)
		for _, w := range []string{"journal.", "payment.", "bank.", "ap.", "ar.", "approve", "certify", "award", "release", "post", "confirm", ".create",
			".manage", ".prepare", ".issue", ".receive", ".record", "decide", "evaluate"} {
			if strings.Contains(s, w) {
				return true
			}
		}
		return false
	}
	for _, p := range cat["system_admin"] {
		if acting(p) && p != auth.PermUserManage && p != auth.PermRoleManage && p != auth.PermTenantSettingsManage && p != auth.PermSiteDeviceManage && p != auth.PermDelegationApprove {
			t.Errorf("system_admin holds %s", p)
		}
	}
	for _, p := range cat["board_chairman"] {
		if !strings.HasSuffix(string(p), ".read") && p != auth.PermScopeTenantWide {
			t.Errorf("board_chairman holds a non-read permission %s", p)
		}
	}
	for _, p := range cat["ceo"] {
		if p == auth.PermDelegationCreate { // lends the CEO's own approvals while away; a system administrator approves the delegation
			continue
		}
		if strings.Contains(string(p), ".create") || strings.Contains(string(p), ".manage") || strings.Contains(string(p), ".prepare") ||
			strings.Contains(string(p), "post") || strings.Contains(string(p), "release") || strings.HasPrefix(string(p), "payment.") || strings.HasPrefix(string(p), "user.") || strings.HasPrefix(string(p), "role.") {
			t.Errorf("ceo holds %s", p)
		}
	}
	for _, r := range []string{"system_admin", "board_chairman", "ceo"} {
		if auth.RoleProfiles[r] == "" {
			t.Errorf("%s has no written responsibilities", r)
		}
	}

	// Behaviour on real documents.
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.approvedBOQ(t)
	now := time.Now()
	lead := func(id, role string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, Kind: auth.KindHuman, IssuedAt: now.Add(-time.Minute), MFA: true, AuthTime: now}
	}
	admin, chair, ceo := lead("u-sysadmin", "system_admin"), lead("u-chairman", "board_chairman"), lead("u-ceo", "ceo")
	pr, err := f.svc.CreatePR(f.ctx, c.siteEng, f.project, "Formwork plywood for level 2", nil, []PRLine{{LineNo: 1, CostCodeID: w.costCode, Description: "Plywood 18 mm", Unit: "sheet", Quantity: dec("120"), EstUnitPrice: dec("2100")}})
	must(t, "pr", err)
	must(t, "submit", f.svc.SubmitPR(f.ctx, c.siteEng, pr))
	for name, p := range map[string]auth.Principal{"system admin": admin, "board chairman": chair} {
		if err := f.svc.DecidePR(f.ctx, p, pr, true, ""); err == nil {
			t.Fatalf("the %s approved a requisition", name)
		}
		if _, err := f.svc.CreatePR(f.ctx, p, f.project, "Not allowed to prepare", nil, []PRLine{{LineNo: 1, CostCodeID: w.costCode, Description: "x", Unit: "ea", Quantity: dec("1"), EstUnitPrice: dec("1")}}); err == nil {
			t.Fatalf("the %s prepared a requisition", name)
		}
	}
	if _, err := f.svc.CreatePR(f.ctx, ceo, f.project, "The CEO does not prepare", nil, []PRLine{{LineNo: 1, CostCodeID: w.costCode, Description: "x", Unit: "ea", Quantity: dec("1"), EstUnitPrice: dec("1")}}); err == nil {
		t.Fatal("the CEO prepared a requisition")
	}
	must(t, "the CEO approves the site engineer's requisition", f.svc.DecidePR(f.ctx, ceo, pr, true, ""))
	// The chairman and the CEO see the company's journals; the system administrator does not.
	if _, err := f.svc.ListJournals(f.ctx, chair, "", "", 10); err != nil {
		t.Fatalf("the chairman cannot read journals: %v", err)
	}
	if _, err := f.svc.ListJournals(f.ctx, ceo, "", "", 10); err != nil {
		t.Fatalf("the CEO cannot read journals: %v", err)
	}
	if _, err := f.svc.ListJournals(f.ctx, admin, "", "", 10); err == nil {
		t.Fatal("the system administrator read the journals")
	}
	if _, err := f.svc.GetP2PDoc(f.ctx, chair, "purchase_requisition", pr); err != nil {
		t.Fatalf("the chairman cannot open a requisition: %v", err)
	}
}

// TestRoleResponsibilities (0096): a company role states its responsibilities
// beside its permissions; product leadership roles carry theirs.
func TestRoleResponsibilities(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	admin := auth.Principal{UserID: "u-sysadmin", TenantID: f.tenant, Roles: []string{"system_admin"}, Kind: auth.KindHuman, IssuedAt: now.Add(-time.Minute), MFA: true, AuthTime: now}
	duty := "Leads site safety walks every morning; stops work on an unsafe scaffold; reports to the HSE manager."
	if _, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "site_safety_lead", Name: "Site safety lead", Permissions: []auth.Permission{auth.PermHSEReport, auth.PermP2PRead},
		Responsibilities: strings.Repeat("x", 4001), Reason: "new site role"}); err == nil {
		t.Fatal("responsibilities over 4,000 characters were accepted")
	}
	_, err := f.svc.SaveRole(f.ctx, admin, RoleInput{Code: "site_safety_lead", Name: "Site safety lead", Permissions: []auth.Permission{auth.PermHSEReport, auth.PermP2PRead},
		Responsibilities: duty, Reason: "new site role"})
	must(t, "save role", err)
	v, err := f.svc.People(f.ctx, admin)
	must(t, "people", err)
	found := map[string]string{}
	for _, r := range v.Roles {
		found[r.Code] = r.Responsibilities
	}
	if found["custom:site_safety_lead"] != duty {
		t.Fatalf("company role responsibilities: %q", found["custom:site_safety_lead"])
	}
	for _, r := range []string{"system_admin", "board_chairman", "ceo"} {
		if found[r] == "" {
			t.Errorf("product role %s shows no responsibilities", r)
		}
	}
}
