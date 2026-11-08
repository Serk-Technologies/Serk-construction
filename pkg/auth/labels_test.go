package auth

import "testing"

// Every permission a role can hold has a plain-words label, an area and a
// stage, so the people and access pages never show a bare code.
func TestEveryPermissionIsLabelled(t *testing.T) {
	a := NewAuthorizer(nil, false)
	for p := range a.KnownPermissions() {
		if _, ok := permissionLabels[p]; !ok {
			t.Errorf("permission %s has no label in labels.go", p)
		}
	}
	for p, l := range permissionLabels {
		if _, ok := stageRank[l.stage]; !ok || l.area == "" || l.label == "" {
			t.Errorf("label of %s is incomplete: %+v", p, l)
		}
	}
	if got := HighestStage([]Permission{PermP2PRead, PermPOManage, PermPOApprove}); got != StageApprove {
		t.Fatalf("highest stage = %q, want approve", got)
	}
	if got := HighestStage(nil); got != "" {
		t.Fatalf("no permissions = %q, want empty", got)
	}
}

// Custom-role permissions count like role permissions, but never the
// reserved principal.* family.
func TestCustomPermissionsCount(t *testing.T) {
	a := NewAuthorizer(nil, false)
	p := Principal{UserID: "u", TenantID: "t", Custom: map[Permission][]string{PermPOApprove: {"site_buyer"}, "principal.human": {"x"}}}
	if !a.Has(p, PermPOApprove) || a.Has(p, "principal.human") {
		t.Fatal("custom permissions not applied correctly")
	}
	got := a.Permissions(p)
	if len(got) != 1 || got[0] != PermPOApprove {
		t.Fatalf("permissions = %v", got)
	}
}
