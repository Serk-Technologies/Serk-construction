package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestTasks_AssignCompleteCancel (CNX-07.2, 0087): a task is assigned to an
// active person of the directory with a due date and notifies them; only the
// assignee completes it, only its creator cancels or reassigns it; the
// database enforces both names and keeps decided tasks unchanged; nobody else
// sees or acts on it; AI principals take no part.
func TestTasks_AssignCompleteCancel(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	pm, eng, buyer := c.pm, c.siteEng, c.buyer
	for _, p := range []auth.Principal{pm, eng, buyer} {
		must(t, "directory", f.svc.Touch(f.ctx, p))
	}
	day := func(d int) string { return time.Now().AddDate(0, 0, d).Format("2006-01-02") }
	in := TaskInput{ProjectID: f.project, Title: "Book the 50 t crane for the bridge beams", Detail: "Two days, week 46", AssignedTo: eng.UserID, DueOn: day(3), Priority: "high"}
	if _, err := f.svc.CreateTask(f.ctx, pm, TaskInput{ProjectID: f.project, Title: "Somebody unknown", AssignedTo: "u-nobody", DueOn: day(1)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a task was assigned to someone outside the directory: %v", err)
	}
	if _, err := f.svc.CreateTask(f.ctx, pm, TaskInput{ProjectID: f.project, Title: "Yesterday", AssignedTo: eng.UserID, DueOn: day(-1)}); err == nil {
		t.Fatal("a task due yesterday was created")
	}
	outside := eng
	outside.UserID, outside.ProjectIDs = "u-eng-elsewhere", []string{"00000000-0000-0000-0000-000000000001"}
	if _, err := f.svc.CreateTask(f.ctx, outside, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("someone outside the project created a task on it: %v", err)
	}
	ai := pm
	ai.UserID, ai.Kind = "u-assistant", auth.KindAI
	if _, err := f.svc.CreateTask(f.ctx, ai, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an AI principal created a task: %v", err)
	}
	crane, err := f.svc.CreateTask(f.ctx, pm, in)
	must(t, "task", err)
	other, err := f.svc.CreateTask(f.ctx, pm, TaskInput{ProjectID: f.project, Title: "Chase the rebar supplier for the mill certificates", AssignedTo: eng.UserID, DueOn: day(0)})
	must(t, "task 2", err)

	var notes int
	must(t, "notification", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_user = $1 AND kind = 'task.assigned'`, eng.UserID).Scan(&notes)
	}))
	if notes != 2 {
		t.Fatalf("the assignee got %d notifications", notes)
	}
	mine, err := f.svc.Tasks(f.ctx, eng, "mine")
	must(t, "mine", err)
	if len(mine) != 2 || mine[0]["status"] != "OPEN" {
		t.Fatalf("the assignee's tasks: %v", mine)
	}
	if theirs, _ := f.svc.Tasks(f.ctx, buyer, "mine"); len(theirs) != 0 {
		t.Fatalf("someone else sees the tasks: %v", theirs)
	}

	// Only the assignee completes; only the creator cancels or reassigns; strangers see nothing.
	if err := f.svc.CompleteTask(f.ctx, pm, crane, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the creator completed the assignee's task: %v", err)
	}
	if err := f.svc.CompleteTask(f.ctx, buyer, crane, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stranger reached the task: %v", err)
	}
	if err := f.svc.CancelTask(f.ctx, eng, crane, "not my job really"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the assignee cancelled the task: %v", err)
	}
	rawRefusedWith(t, f, "complete a task in another person's name", "completed by the person it is assigned to",
		`UPDATE work_tasks SET status = 'DONE', completed_by = 'u-pm' WHERE id = $1::uuid`, crane)
	rawRefusedWith(t, f, "reassign to someone outside the directory", "active person of the directory",
		`UPDATE work_tasks SET assigned_to = 'u-nobody' WHERE id = $1::uuid`, crane)
	must(t, "reassign", f.svc.ReassignTask(f.ctx, pm, other, buyer.UserID, day(5)))
	must(t, "complete", f.svc.CompleteTask(f.ctx, eng, crane, "Booked with Abay Cranes for 12–13 November"))
	if err := f.svc.CompleteTask(f.ctx, eng, crane, ""); !errors.Is(err, ErrWorkflowState) {
		t.Fatalf("a task was completed twice: %v", err)
	}
	if err := f.svc.CancelTask(f.ctx, pm, other, "no"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a cancellation without a reason: %v", err)
	}
	must(t, "cancel", f.svc.CancelTask(f.ctx, pm, other, "Supplier sent the certificates unprompted"))
	rawRefusedWith(t, f, "reopen a done task", "never changes", `UPDATE work_tasks SET due_on = current_date + 9 WHERE id = $1::uuid`, crane)
	rawFails(t, f, "delete a task", `DELETE FROM work_tasks WHERE id = '`+crane+`'`)

	created, err := f.svc.Tasks(f.ctx, pm, "created")
	must(t, "created", err)
	got := map[string]string{}
	for _, x := range created {
		got[x["id"].(string)] = x["status"].(string)
	}
	if got[crane] != "DONE" || got[other] != "CANCELLED" {
		t.Fatalf("the creator's view: %v", got)
	}
}
