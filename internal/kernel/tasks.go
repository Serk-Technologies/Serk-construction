// Assignable tasks with due dates, outside document approvals (CNX-07.2; migration 0087).
package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// TaskInput asks a person to do something by a date, optionally on a project.
type TaskInput struct {
	ProjectID  string `json:"project_id,omitempty"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	AssignedTo string `json:"assigned_to"`
	DueOn      string `json:"due_on"`
	Priority   string `json:"priority,omitempty"`
}

// A task on a project is created by someone who works on it; a task without
// a project by anyone who reads the company's documents (p2p.read).
func (s *Service) taskScope(p auth.Principal, project string) error {
	if p.Kind != auth.KindHuman {
		return auth.ErrForbidden
	}
	if project == "" {
		return s.authz.Require(p, auth.PermP2PRead, p.TenantID)
	}
	return s.authz.RequireProject(p, auth.PermP2PRead, p.TenantID, project)
}

// CreateTask records an OPEN task and notifies the person it is assigned to.
func (s *Service) CreateTask(ctx context.Context, p auth.Principal, in TaskInput) (string, error) {
	due, err := parseDay("due_on", in.DueOn)
	if err != nil {
		return "", err
	}
	prio := strings.ToUpper(strings.TrimSpace(in.Priority))
	if prio == "" {
		prio = "NORMAL"
	}
	if !oneOf(prio, "LOW", "NORMAL", "HIGH") {
		return "", fmt.Errorf("%w: priority is LOW, NORMAL or HIGH", ErrInvalidInput)
	}
	to := strings.TrimSpace(in.AssignedTo)
	if len(strings.TrimSpace(in.Title)) < 3 || to == "" {
		return "", fmt.Errorf("%w: give the task a title and a person", ErrInvalidInput)
	}
	if err := s.taskScope(p, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM directory_users WHERE user_id = $1 AND status = 'ACTIVE')`, to).Scan(&active); err != nil {
			return err
		}
		if !active {
			return fmt.Errorf("%w: %s is not an active person of the directory", ErrInvalidInput, to)
		}
		num, err := postgres.NextNumber(ctx, tx, "TSK")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO work_tasks (project_id, number, title, detail, priority, assigned_to, due_on, created_by)
			VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Title), strings.TrimSpace(in.Detail), prio, to, due, p.UserID)
		if err != nil {
			return err
		}
		if err := s.taskNotice(ctx, tx, p, id, num, in.ProjectID, to, strings.TrimSpace(in.Title), due.Format("2006-01-02"), "assigned"); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "task.create", "work_task", id, "task.created", map[string]any{"id": id, "number": num, "assigned_to": to})
	})
	return id, err
}

func (s *Service) taskNotice(ctx context.Context, tx pgx.Tx, p auth.Principal, id, num, project, to, title, due, what string) error {
	if to == p.UserID {
		return nil
	}
	return s.notify(ctx, tx, postgres.Notification{RecipientUser: to, Kind: "task." + what, Severity: "info",
		Title: fmt.Sprintf("Task %s %s to you: %s", num, what, title), Body: "Due " + due + ". From " + p.UserID + ".",
		SubjectType: "work_task", SubjectID: id, ProjectID: project, Href: "#/tasks",
		DedupeKey: "task:" + id + ":" + what + ":" + to + ":" + due, CreatedBy: p.UserID})
}

type taskRow struct{ assignedTo, createdBy, status, number, project, title string }

func (s *Service) taskTx(ctx context.Context, p auth.Principal, id string, fn func(context.Context, pgx.Tx, taskRow) error) error {
	if p.Kind != auth.KindHuman {
		return auth.ErrForbidden
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var t taskRow
		if err := mapNoRows(tx.QueryRow(ctx, `SELECT assigned_to, created_by, status, number, COALESCE(project_id::text, ''), title FROM work_tasks WHERE id = $1::uuid FOR UPDATE`, id).
			Scan(&t.assignedTo, &t.createdBy, &t.status, &t.number, &t.project, &t.title)); err != nil {
			return err
		}
		// Only the two people concerned see a task through these actions.
		if p.UserID != t.assignedTo && p.UserID != t.createdBy {
			return ErrNotFound
		}
		if t.status != "OPEN" {
			return fmt.Errorf("%w: the task is %s", ErrWorkflowState, strings.ToLower(t.status))
		}
		return fn(ctx, tx, t)
	})
}

// CompleteTask marks a task DONE; only the person it is assigned to does.
func (s *Service) CompleteTask(ctx context.Context, p auth.Principal, id, note string) error {
	return s.taskTx(ctx, p, id, func(ctx context.Context, tx pgx.Tx, t taskRow) error {
		if p.UserID != t.assignedTo {
			return fmt.Errorf("%w: only the person the task is assigned to completes it", auth.ErrForbidden)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE work_tasks SET status = 'DONE', completed_by = $2, completion_note = NULLIF($3, '') WHERE id = $1::uuid`, id, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		if t.createdBy != p.UserID {
			if err := s.notify(ctx, tx, postgres.Notification{RecipientUser: t.createdBy, Kind: "task.done", Severity: "info", Title: "Task " + t.number + " done: " + t.title,
				Body: strings.TrimSpace(note), SubjectType: "work_task", SubjectID: id, ProjectID: t.project, Href: "#/tasks", DedupeKey: "task:" + id + ":done", CreatedBy: p.UserID}); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "task.complete", "work_task", id, "task.completed", map[string]any{"id": id})
	})
}

// CancelTask cancels an open task with a reason; only its creator does.
func (s *Service) CancelTask(ctx context.Context, p auth.Principal, id, reason string) error {
	if len(strings.TrimSpace(reason)) < 5 {
		return fmt.Errorf("%w: cancelling needs a reason", ErrInvalidInput)
	}
	return s.taskTx(ctx, p, id, func(ctx context.Context, tx pgx.Tx, t taskRow) error {
		if p.UserID != t.createdBy {
			return fmt.Errorf("%w: only the person who created the task cancels it", auth.ErrForbidden)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE work_tasks SET status = 'CANCELLED', cancelled_by = $2, cancelled_reason = $3 WHERE id = $1::uuid`, id, p.UserID, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "task.cancel", "work_task", id, "task.cancelled", map[string]any{"id": id, "reason": reason})
	})
}

// ReassignTask gives an open task to another person or a new date; only its creator does.
func (s *Service) ReassignTask(ctx context.Context, p auth.Principal, id, to, dueOn string) error {
	due, err := parseDay("due_on", dueOn)
	if err != nil {
		return err
	}
	to = strings.TrimSpace(to)
	return s.taskTx(ctx, p, id, func(ctx context.Context, tx pgx.Tx, t taskRow) error {
		if p.UserID != t.createdBy {
			return fmt.Errorf("%w: only the person who created the task reassigns it", auth.ErrForbidden)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE work_tasks SET assigned_to = $2, due_on = $3 WHERE id = $1::uuid`, id, to, due); err != nil {
			return err
		}
		if err := s.taskNotice(ctx, tx, p, id, t.number, t.project, to, t.title, due.Format("2006-01-02"), "reassigned"); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "task.reassign", "work_task", id, "task.reassigned", map[string]any{"id": id, "assigned_to": to, "due_on": dueOn})
	})
}

// Tasks lists the tasks assigned to the person ("mine") or created by them
// ("created"), open first and by due date, with what is overdue.
func (s *Service) Tasks(ctx context.Context, p auth.Principal, view string) ([]map[string]any, error) {
	if p.Kind != auth.KindHuman {
		return nil, auth.ErrForbidden
	}
	col := "assigned_to"
	if view == "created" {
		col = "created_by"
	}
	var out []map[string]any
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = rowsAsMaps(ctx, tx, `SELECT t.id::text AS id, t.number, t.title, t.detail, t.priority, t.assigned_to, t.created_by, t.due_on::text AS due_on, t.status,
			COALESCE((SELECT code FROM projects WHERE id = t.project_id), '') AS project, (t.status = 'OPEN' AND t.due_on < current_date) AS overdue,
			COALESCE(t.completion_note, t.cancelled_reason, '') AS note
			FROM work_tasks t WHERE t.`+col+` = $1 ORDER BY (t.status = 'OPEN') DESC, t.due_on, t.number LIMIT 500`, p.UserID)
		return err
	})
	return out, err
}
