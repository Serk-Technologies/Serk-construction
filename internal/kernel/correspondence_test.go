package kernel

import (
	"errors"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestCorrespondence_RegisterRepliesAndOverdue (CNX-05.1, 0085): incoming and
// outgoing items are recorded against the project and optionally one of its
// contracts; an item that needs an answer awaits it until a reply in the other
// direction is recorded, which marks it answered in the same transaction; a
// recorded item never changes otherwise, is never deleted, and the register
// shows what is overdue.
func TestCorrespondence_RegisterRepliesAndOverdue(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	contract, err := f.svc.CreateContract(f.ctx, c.commercial, w.contractInput())
	must(t, "contract", err)
	eng := c.siteEng
	day := func(d int) string { return time.Now().AddDate(0, 0, d).Format("2006-01-02") }

	letter := CorrespondenceInput{ProjectID: f.project, ContractID: contract, Direction: "in", Counterparty: "Ethiopian Roads Administration",
		Subject: "Notice of delayed access to chainage 12+400", Reference: "ERA/RE/2026/233", DatedOn: day(-20), ReplyDueOn: day(-6)}
	if _, err := f.svc.RecordCorrespondence(f.ctx, c.buyer, letter); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer recorded correspondence: %v", err)
	}
	bad := letter
	bad.ReplyDueOn = day(-30)
	if _, err := f.svc.RecordCorrespondence(f.ctx, eng, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an answer due before the letter was accepted: %v", err)
	}
	in, err := f.svc.RecordCorrespondence(f.ctx, eng, letter)
	must(t, "incoming letter", err)
	note, err := f.svc.RecordCorrespondence(f.ctx, eng, CorrespondenceInput{ProjectID: f.project, Direction: "OUT", Counterparty: "Abay Piling PLC",
		Subject: "Site induction schedule for October", DatedOn: day(-3)})
	must(t, "outgoing note", err)

	reg, err := f.svc.CorrespondenceRegister(f.ctx, eng, f.project)
	must(t, "register", err)
	byID := map[string]map[string]any{}
	for _, r := range reg {
		byID[r["id"].(string)] = r
	}
	if r := byID[in]; r["status"] != "AWAITING_REPLY" || r["overdue"] != true || r["contract"] == "" {
		t.Fatalf("incoming letter in the register: %v", r)
	}
	if r := byID[note]; r["status"] != "RECORDED" || r["overdue"] != false {
		t.Fatalf("outgoing note in the register: %v", r)
	}

	// The database keeps the register honest.
	rawRefusedWith(t, f, "mark an item answered without a reply", "only by recording its reply",
		`UPDATE correspondence SET status = 'ANSWERED' WHERE id = $1::uuid`, in)
	rawRefusedWith(t, f, "rewrite a recorded item", "permission denied",
		`UPDATE correspondence SET subject = 'Something else entirely' WHERE id = $1::uuid`, note)
	rawFails(t, f, "delete an item", `DELETE FROM correspondence WHERE id = '`+note+`'`)
	rawRefusedWith(t, f, "reply in the same direction", "other direction",
		`INSERT INTO correspondence (project_id, number, direction, counterparty, subject, dated_on, in_reply_to, status, created_by)
		 VALUES ($1::uuid, 'COR-X1', 'IN', 'Ethiopian Roads Administration', 'Reminder', current_date, $2::uuid, 'RECORDED', 'u-site-engineer')`, f.project, in)
	if _, err := f.svc.RecordCorrespondence(f.ctx, eng, CorrespondenceInput{ProjectID: f.project, Direction: "IN", Counterparty: "Abay Piling PLC",
		Subject: "Re: induction", DatedOn: day(0), InReplyTo: note}); !errors.Is(err, ErrWorkflowState) {
		t.Fatalf("a reply to an item needing none was accepted: %v", err)
	}

	// The reply answers the letter; a second reply is refused.
	reply := CorrespondenceInput{ProjectID: f.project, ContractID: contract, Direction: "OUT", Counterparty: "Ethiopian Roads Administration",
		Subject: "Re: delayed access — revised sequence enclosed", Reference: "SERK/ERA/2026/091", DatedOn: day(-1), InReplyTo: in}
	ans, err := f.svc.RecordCorrespondence(f.ctx, eng, reply)
	must(t, "reply", err)
	if _, err := f.svc.RecordCorrespondence(f.ctx, eng, reply); !errors.Is(err, ErrWorkflowState) {
		t.Fatalf("a second reply was accepted: %v", err)
	}
	reg, err = f.svc.CorrespondenceRegister(f.ctx, eng, f.project)
	must(t, "register 2", err)
	for _, r := range reg {
		if r["id"] == in && (r["status"] != "ANSWERED" || r["overdue"] != false || r["answered_by"] == "") {
			t.Fatalf("answered letter: %v", r)
		}
		if r["id"] == ans && r["replies_to"] == "" {
			t.Fatalf("reply: %v", r)
		}
	}
	elsewhere := eng
	elsewhere.UserID, elsewhere.ProjectIDs = "u-eng-elsewhere", []string{"00000000-0000-0000-0000-000000000001"}
	if _, err := f.svc.CorrespondenceRegister(f.ctx, elsewhere, f.project); err == nil {
		t.Fatal("someone outside the project read its correspondence")
	}
}
