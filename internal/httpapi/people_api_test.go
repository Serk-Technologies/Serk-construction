package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// People and access through the API (0048): the middleware records each
// signed-in person, applies the directory's roles on the next request,
// refuses a disabled person and revokes their sessions.
func TestAPIPeopleDirectory(t *testing.T) {
	e := newEnv(t)
	mk := func(user string, roles ...string) string {
		now := time.Now()
		tok, err := testVerifier.Sign(Claims{Subject: user, TenantID: e.tenant, Roles: roles, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
			Issuer: "serk-id", Audience: "serk-construction", TokenID: uuid.NewString(), Kind: "human", AMR: []string{"pwd", "otp"}, AuthTime: now.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	admin := mk("u-secadmin", "security_admin")
	pm := mk("u-pm-x", "project_manager", "cfo")

	code, prof := e.do("GET", "/v1/me/profile", pm, nil)
	if code != 200 || prof["role_source"] != "sign-in" || len(prof["roles"].([]any)) != 2 {
		t.Fatalf("profile before management: %d %v", code, prof)
	}
	if code, _ := e.do("GET", "/v1/people", mk("u-pm-plain", "project_manager"), nil); code != 403 {
		t.Fatalf("pm listed people: %d", code)
	}
	code, list := e.do("GET", "/v1/people", admin, nil)
	if code != 200 || len(list["people"].([]any)) < 2 || len(list["permissions"].([]any)) < 100 {
		t.Fatalf("people: %d %v", code, list["people"])
	}
	if code, out := e.do("POST", "/v1/people/u-secadmin/manage", admin, map[string]any{"reason": "manage myself"}); code != 409 {
		t.Fatalf("self management: %d %v", code, out)
	}
	code, x := e.do("POST", "/v1/people/u-pm-x/manage", admin, map[string]any{"reason": "HQ directory rollout"})
	if code != 200 || x["managed"] != true {
		t.Fatalf("manage: %d %v", code, x)
	}
	var cfoGrant string
	for _, g := range x["grants"].([]any) {
		if g.(map[string]any)["role"] == "cfo" {
			cfoGrant = g.(map[string]any)["id"].(string)
		}
	}
	if code, out := e.do("POST", "/v1/role-grants/"+cfoGrant+"/revoke", admin, map[string]any{"reason": "a PM is not the CFO"}); code != 200 {
		t.Fatalf("revoke: %d %v", code, out)
	}
	// The same token, next request: the directory has the last word.
	code, prof = e.do("GET", "/v1/me/profile", pm, nil)
	if code != 200 || prof["role_source"] != "directory" || len(prof["roles"].([]any)) != 1 {
		t.Fatalf("profile after removal: %d %v", code, prof)
	}
	if code, out := e.do("PUT", "/v1/me/profile", pm, map[string]any{"display_name": "Hagos Tesfay", "job_title": "Project manager"}); code != 200 || out["display_name"] != "Hagos Tesfay" {
		t.Fatalf("own profile: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/roles", admin, map[string]any{"code": "store_helper", "name": "Store helper",
		"permissions": []string{"warehouse.manage"}, "reason": "site stores need a helper"}); code != 201 || out["code"] != "custom:store_helper" {
		t.Fatalf("company role: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/roles", admin, map[string]any{"code": "buyer_all", "name": "Buyer",
		"permissions": []string{"po.manage", "po.approve"}, "reason": "one person buys"}); code != 409 {
		t.Fatalf("conflicting company role: %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/people/u-pm-x/disable", admin, map[string]any{"reason": "left the company"}); code != 200 {
		t.Fatalf("disable: %d %v", code, out)
	}
	if code, out := e.do("GET", "/v1/me/profile", pm, nil); code != 401 {
		t.Fatalf("revoked token of a disabled person: %d %v", code, out)
	}
	time.Sleep(1100 * time.Millisecond) // a sign-in strictly after the revocation
	code, out := e.do("GET", "/v1/me/profile", mk("u-pm-x", "project_manager"), nil)
	if errObj, _ := out["error"].(map[string]any); code != 403 || errObj["code"] != "account_disabled" {
		t.Fatalf("new sign-in of a disabled person: %d %v", code, out)
	}
}
