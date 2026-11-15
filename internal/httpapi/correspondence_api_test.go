package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestAPICorrespondence (CNX-05.1, 0085): an engineer of the project records
// an incoming letter needing an answer and the outgoing reply; the register
// shows it awaiting, then answered; the generic list carries it; another
// project's engineer neither records nor reads it.
func TestAPICorrespondence(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	eng := a.tok("u-cor-eng", P, false, "site_engineer")
	elsewhere := a.tok("u-cor-else", []string{uuid.NewString()}, false, "site_engineer")
	day := func(d int) string { return time.Now().AddDate(0, 0, d).Format("2006-01-02") }

	letter := map[string]any{"project_id": a.project, "direction": "IN", "counterparty": "Addis Ababa City Roads Authority",
		"subject": "Traffic management plan for the night pour", "reference": "AACRA/TM/118", "dated_on": day(-2), "reply_due_on": day(5)}
	if code, out := a.do("POST", "/v1/correspondence", elsewhere, letter); code != 403 {
		t.Fatalf("an engineer of another project recorded correspondence: %d %v", code, out)
	}
	code, out := a.do("POST", "/v1/correspondence", eng, letter)
	if code != 201 {
		t.Fatalf("letter: %d %v", code, out)
	}
	in := out["id"].(string)
	register := func(tok string) (int, []any) {
		code, out := a.do("GET", "/v1/projects/"+a.project+"/correspondence", tok, nil)
		items, _ := out["items"].([]any)
		return code, items
	}
	if code, items := register(eng); code != 200 || len(items) != 1 || items[0].(map[string]any)["status"] != "AWAITING_REPLY" {
		t.Fatalf("register: %d %v", code, items)
	}
	if code, _ := register(elsewhere); code != 403 {
		t.Fatalf("another project's engineer read the register: %d", code)
	}
	reply := map[string]any{"project_id": a.project, "direction": "OUT", "counterparty": "Addis Ababa City Roads Authority",
		"subject": "Re: traffic management plan — revised plan enclosed", "dated_on": day(0), "in_reply_to": in}
	if code, out := a.do("POST", "/v1/correspondence", eng, reply); code != 201 {
		t.Fatalf("reply: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/correspondence", eng, reply); code != 409 {
		t.Fatalf("a second reply: %d %v", code, out)
	}
	_, items := register(eng)
	for _, x := range items {
		m := x.(map[string]any)
		if m["id"] == in && m["status"] != "ANSWERED" {
			t.Fatalf("the letter after its reply: %v", m)
		}
	}
	if code, out := a.do("GET", "/v1/p2p/correspondence?status=ANSWERED", eng, nil); code != 200 || len(out["items"].([]any)) != 1 {
		t.Fatalf("generic list: %d %v", code, out)
	}
}
