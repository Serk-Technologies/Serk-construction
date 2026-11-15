// Quality management and site records (WP-3.6, WP-3.8; SRS QA-01..05, SIT-06,
// SIT-07, COL-04). Migration: 0065_quality_site.sql.
//
// An inspection and test plan (ITP) is prepared by a quality engineer and
// approved by a project manager; a HOLD item stops progress on its activity
// until an inspection of it has passed (the database refuses the progress).
// Punch items, site instructions and meeting minutes are the site's records,
// each with the separation of duties the database enforces.
package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// requireProjectAny passes when the principal holds any of the permissions on the project.
func (s *Service) requireProjectAny(p auth.Principal, project string, perms ...auth.Permission) error {
	var last error
	for _, perm := range perms {
		if err := s.authz.RequireProject(p, perm, p.TenantID, project); err == nil {
			return nil
		} else {
			last = err
		}
	}
	if last == nil {
		last = auth.ErrForbidden
	}
	return last
}

// stepByID applies one update to a project record, after checking the
// principal's permission on the record's project, and audits it.
func (s *Service) stepByID(ctx context.Context, p auth.Principal, table, aggregate, id, sql, action string, perms []auth.Permission, args ...any) error {
	var project string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text FROM `+table+` WHERE id = $1::uuid`, id).Scan(&project))
	}); err != nil {
		return err
	}
	if err := s.requireProjectAny(p, project, perms...); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, sql, append([]any{id}, args...)...); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, aggregate, id, strings.ReplaceAll(action, ".", "_"), map[string]any{"id": id})
	})
}

// ---------------------------------------------------------------- quality ---

// ITPItem is one item of an inspection and test plan.
type ITPItem struct {
	Description   string          `json:"description"`
	Kind          string          `json:"kind"` // HOLD | WITNESS | REVIEW
	HoldAtPercent decimal.Decimal `json:"hold_at_percent"`
}

// ITPIn prepares an inspection and test plan.
type ITPIn struct {
	ProjectID    string    `json:"project_id"`
	Title        string    `json:"title"`
	ActivityCode string    `json:"activity_code"`
	Items        []ITPItem `json:"items"`
}

// CreateITP prepares a DRAFT plan.
func (s *Service) CreateITP(ctx context.Context, p auth.Principal, in ITPIn) (string, error) {
	if len(in.Items) == 0 {
		return "", fmt.Errorf("%w: a plan has at least one item", ErrInvalidInput)
	}
	if err := s.authz.RequireProject(p, auth.PermQualityManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "ITP")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO itps (project_id, number, title, activity_code, created_by) VALUES ($1::uuid, $2, $3, NULLIF($4, ''), $5) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Title), strings.ToUpper(strings.TrimSpace(in.ActivityCode)), p.UserID)
		if err != nil {
			return err
		}
		for i, it := range in.Items {
			if _, err := tx.Exec(ctx, `INSERT INTO itp_items (itp_id, seq, description, kind, hold_at_percent) VALUES ($1::uuid, $2, $3, $4, $5)`,
				id, i+1, strings.TrimSpace(it.Description), strings.ToUpper(strings.TrimSpace(it.Kind)), it.HoldAtPercent); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "itp.create", "itp", id, "itp.created", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// SubmitITP, DecideITP and CancelITP move a plan.
func (s *Service) SubmitITP(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermQualityManage, "itp", "itps", id, "itp.submit", "itp.submitted")
}
func (s *Service) DecideITP(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideSimple(ctx, p, auth.PermQualityApprove, "itp", "itps", id, "itp.decide", "inspection plan", approve, reason)
}
func (s *Service) CancelITP(ctx context.Context, p auth.Principal, id string) error {
	return s.cancelDraft(ctx, p, auth.PermQualityManage, "itp", "itps", id)
}

// InspectionIn asks for an inspection of one plan item.
type InspectionIn struct {
	ProjectID    string `json:"project_id"`
	ITPID        string `json:"itp_id"`
	ItemSeq      int    `json:"item_seq"`
	RequestedFor string `json:"requested_for"`
	Location     string `json:"location"`
}

// RequestInspection asks for an inspection (the site).
func (s *Service) RequestInspection(ctx context.Context, p auth.Principal, in InspectionIn) (string, error) {
	day, err := parseDay("requested_for", in.RequestedFor)
	if err != nil {
		return "", err
	}
	if err := s.requireProjectAny(p, in.ProjectID, auth.PermQualityRequest, auth.PermQualityManage); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "IR")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO inspection_requests (project_id, number, itp_id, item_seq, requested_for, location, requested_by) VALUES ($1::uuid, $2, $3::uuid, $4, $5, NULLIF($6, ''), $7) RETURNING id::text`,
			in.ProjectID, num, in.ITPID, in.ItemSeq, day, strings.TrimSpace(in.Location), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "inspection.request", "inspection_request", id, "inspection.requested", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// DecideInspection records the result (a person other than the requester).
func (s *Service) DecideInspection(ctx context.Context, p auth.Principal, id string, pass bool, note string) error {
	st := map[bool]string{true: "PASSED", false: "FAILED"}[pass]
	return s.stepByID(ctx, p, "inspection_requests", "inspection_request", id, `UPDATE inspection_requests SET status = $2, result_by = $3, result_note = NULLIF($4, '') WHERE id = $1::uuid`,
		"inspection.decide", []auth.Permission{auth.PermQualityManage}, st, p.UserID, strings.TrimSpace(note))
}

// NCRIn raises a non-conformance.
type NCRIn struct {
	ProjectID   string `json:"project_id"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Location    string `json:"location"`
}

// RaiseNCR raises a non-conformance report.
func (s *Service) RaiseNCR(ctx context.Context, p auth.Principal, in NCRIn) (string, error) {
	if err := s.requireProjectAny(p, in.ProjectID, auth.PermQualityRequest, auth.PermQualityManage); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = s.insertNCR(ctx, tx, p, in, "")
		return err
	})
	return id, err
}

func (s *Service) insertNCR(ctx context.Context, tx pgx.Tx, p auth.Principal, in NCRIn, test string) (string, error) {
	num, err := postgres.NextNumber(ctx, tx, "NCR")
	if err != nil {
		return "", err
	}
	id, err := postgres.InsertReturningID(ctx, tx, `INSERT INTO ncrs (project_id, number, severity, description, location, raised_by, source_test_id) VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6, NULLIF($7, '')::uuid) RETURNING id::text`,
		in.ProjectID, num, strings.ToUpper(strings.TrimSpace(in.Severity)), strings.TrimSpace(in.Description), strings.TrimSpace(in.Location), p.UserID, test)
	if err != nil {
		return "", err
	}
	return id, s.record(ctx, tx, p, "ncr.raise", "ncr", id, "ncr.raised", map[string]any{"id": id, "number": num})
}

// ActionNCR records the root cause and the corrective action.
func (s *Service) ActionNCR(ctx context.Context, p auth.Principal, id, rootCause, action string, cost decimal.Decimal) error {
	return s.stepByID(ctx, p, "ncrs", "ncr", id, `UPDATE ncrs SET status = 'ACTIONED', root_cause = $2, corrective_action = $3, cost_impact = $4, actioned_by = $5 WHERE id = $1::uuid`,
		"ncr.action", []auth.Permission{auth.PermQualityManage}, strings.TrimSpace(rootCause), strings.TrimSpace(action), cost, p.UserID)
}

// CloseNCR closes (verifies) an actioned non-conformance.
func (s *Service) CloseNCR(ctx context.Context, p auth.Principal, id, note string) error {
	return s.stepByID(ctx, p, "ncrs", "ncr", id, `UPDATE ncrs SET status = 'CLOSED', closed_by = $2, close_note = $3 WHERE id = $1::uuid`,
		"ncr.close", []auth.Permission{auth.PermQualityApprove}, p.UserID, strings.TrimSpace(note))
}

// MaterialTestIn records a laboratory or site test.
type MaterialTestIn struct {
	ProjectID string           `json:"project_id"`
	BatchRef  string           `json:"batch_ref"`
	TestType  string           `json:"test_type"`
	Value     decimal.Decimal  `json:"value"`
	Unit      string           `json:"unit"`
	SpecMin   *decimal.Decimal `json:"spec_min"`
	SpecMax   *decimal.Decimal `json:"spec_max"`
	TestedOn  string           `json:"tested_on"`
}

// RecordMaterialTest records a test; the database computes pass or fail and a
// failed test raises a non-conformance automatically.
func (s *Service) RecordMaterialTest(ctx context.Context, p auth.Principal, in MaterialTestIn) (string, bool, error) {
	day, err := parseDay("tested_on", in.TestedOn)
	if err != nil {
		return "", false, err
	}
	if err := s.authz.RequireProject(p, auth.PermQualityManage, p.TenantID, in.ProjectID); err != nil {
		return "", false, err
	}
	var id string
	var passed bool
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO material_tests (project_id, batch_ref, test_type, value, unit, spec_min, spec_max, tested_on, passed, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, true, $9) RETURNING id::text, passed`, in.ProjectID, strings.TrimSpace(in.BatchRef), strings.TrimSpace(in.TestType), in.Value, strings.TrimSpace(in.Unit), in.SpecMin, in.SpecMax, day, p.UserID).Scan(&id, &passed); err != nil {
			return err
		}
		if !passed {
			if _, err := s.insertNCR(ctx, tx, p, NCRIn{ProjectID: in.ProjectID, Severity: "MINOR", Location: "Batch " + in.BatchRef,
				Description: fmt.Sprintf("%s test of batch %s failed: %s %s against the specification", in.TestType, in.BatchRef, in.Value.String(), in.Unit)}, id); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "quality.test.record", "material_test", id, "quality.test_recorded", map[string]any{"id": id, "passed": passed})
	})
	return id, passed, err
}

// QualityView lists plans, inspections, non-conformances and tests.
func (s *Service) QualityView(ctx context.Context, p auth.Principal) (map[string]any, error) {
	out := map[string]any{}
	if !s.authz.Has(p, auth.PermQualityManage) && !s.authz.Has(p, auth.PermQualityApprove) && !s.authz.Has(p, auth.PermQualityRequest) {
		return out, auth.ErrForbidden
	}
	wide, projects := s.scopeArgs(p)
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out["projects"], err = rowsAsMaps(ctx, tx, `SELECT id, code, name FROM projects WHERE $1 OR id = ANY($2::uuid[]) ORDER BY code`, wide, projects); err != nil {
			return err
		}
		if out["plans"], err = rowsAsMaps(ctx, tx, `SELECT t.id, t.number, pr.code AS project, t.project_id, t.title, t.activity_code, t.status, t.created_by,
			(SELECT json_agg(json_build_object('seq', i.seq, 'description', i.description, 'kind', i.kind, 'hold_at_percent', i.hold_at_percent,
			    'passed', EXISTS (SELECT 1 FROM inspection_requests r WHERE r.itp_id = i.itp_id AND r.item_seq = i.seq AND r.status = 'PASSED')) ORDER BY i.seq) FROM itp_items i WHERE i.itp_id = t.id) AS items
			FROM itps t JOIN projects pr ON pr.id = t.project_id WHERE ($1 OR t.project_id = ANY($2::uuid[])) ORDER BY t.created_at DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["inspections"], err = rowsAsMaps(ctx, tx, `SELECT r.id, r.number, pr.code AS project, t.number AS plan, r.item_seq, i.description AS item, i.kind, r.requested_for, r.location, r.status, r.requested_by, r.result_by, r.result_note
			FROM inspection_requests r JOIN projects pr ON pr.id = r.project_id JOIN itps t ON t.id = r.itp_id JOIN itp_items i ON i.itp_id = r.itp_id AND i.seq = r.item_seq
			WHERE ($1 OR r.project_id = ANY($2::uuid[])) ORDER BY r.requested_for DESC, r.number DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["ncrs"], err = rowsAsMaps(ctx, tx, `SELECT n.id, n.number, pr.code AS project, n.severity, n.description, n.location, n.root_cause, n.corrective_action, n.cost_impact, n.status, n.raised_by, n.actioned_by, n.closed_by, n.source_test_id IS NOT NULL AS from_test
			FROM ncrs n JOIN projects pr ON pr.id = n.project_id WHERE ($1 OR n.project_id = ANY($2::uuid[])) ORDER BY n.raised_at DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		out["tests"], err = rowsAsMaps(ctx, tx, `SELECT m.id, pr.code AS project, m.batch_ref, m.test_type, m.value, m.unit, m.spec_min, m.spec_max, m.passed, m.tested_on FROM material_tests m JOIN projects pr ON pr.id = m.project_id
			WHERE ($1 OR m.project_id = ANY($2::uuid[])) ORDER BY m.tested_on DESC, m.created_at DESC LIMIT 200`, wide, projects)
		return err
	})
	return out, err
}

// ------------------------------------------------------------------- site ---

// PunchIn raises a punch item.
type PunchIn struct {
	ProjectID   string `json:"project_id"`
	Location    string `json:"location"`
	Description string `json:"description"`
	Assignee    string `json:"assignee"`
	Priority    string `json:"priority"`
	DueOn       string `json:"due_on"`
}

// RaisePunch raises a punch item.
func (s *Service) RaisePunch(ctx context.Context, p auth.Principal, in PunchIn) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermSiteRecord, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	if in.Priority == "" {
		in.Priority = "MEDIUM"
	}
	var due any
	if strings.TrimSpace(in.DueOn) != "" {
		d, err := parseDay("due_on", in.DueOn)
		if err != nil {
			return "", err
		}
		due = d
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "PUN")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO punch_items (project_id, number, location, description, assignee, priority, due_on, raised_by) VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6, $7, $8) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Location), strings.TrimSpace(in.Description), strings.TrimSpace(in.Assignee), strings.ToUpper(strings.TrimSpace(in.Priority)), due, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "punch.raise", "punch_item", id, "punch.raised", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// FixPunch marks an item fixed; ClosePunch closes it (not by the fixer); ReopenPunch sends it back.
func (s *Service) FixPunch(ctx context.Context, p auth.Principal, id string) error {
	return s.stepByID(ctx, p, "punch_items", "punch_item", id, `UPDATE punch_items SET status = 'FIXED', fixed_by = $2 WHERE id = $1::uuid`, "punch.fix", []auth.Permission{auth.PermSiteRecord}, p.UserID)
}
func (s *Service) ClosePunch(ctx context.Context, p auth.Principal, id string) error {
	return s.stepByID(ctx, p, "punch_items", "punch_item", id, `UPDATE punch_items SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, "punch.close", []auth.Permission{auth.PermSiteRecord}, p.UserID)
}
func (s *Service) ReopenPunch(ctx context.Context, p auth.Principal, id string) error {
	return s.stepByID(ctx, p, "punch_items", "punch_item", id, `UPDATE punch_items SET status = 'OPEN', fixed_by = NULL WHERE id = $1::uuid`, "punch.reopen", []auth.Permission{auth.PermSiteRecord})
}

// InstructionIn issues a site instruction.
type InstructionIn struct {
	ProjectID     string `json:"project_id"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	ChangeEventID string `json:"change_event_id"`
}

// IssueInstruction issues a site instruction (optionally linked to a change event).
func (s *Service) IssueInstruction(ctx context.Context, p auth.Principal, in InstructionIn) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermSiteInstruct, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "SI")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO site_instructions (project_id, number, title, body, change_event_id, issued_by) VALUES ($1::uuid, $2, $3, $4, NULLIF($5, '')::uuid, $6) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Title), strings.TrimSpace(in.Body), in.ChangeEventID, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "instruction.issue", "site_instruction", id, "instruction.issued", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// AckInstruction acknowledges an instruction (not its issuer); CloseInstruction closes it.
func (s *Service) AckInstruction(ctx context.Context, p auth.Principal, id string) error {
	return s.stepByID(ctx, p, "site_instructions", "site_instruction", id, `UPDATE site_instructions SET status = 'ACKNOWLEDGED', acknowledged_by = $2 WHERE id = $1::uuid`, "instruction.ack", []auth.Permission{auth.PermSiteRecord}, p.UserID)
}
func (s *Service) CloseInstruction(ctx context.Context, p auth.Principal, id string) error {
	return s.stepByID(ctx, p, "site_instructions", "site_instruction", id, `UPDATE site_instructions SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, "instruction.close", []auth.Permission{auth.PermSiteInstruct}, p.UserID)
}

// MeetingAction is an action item of a meeting.
type MeetingAction struct {
	Description string `json:"description"`
	Owner       string `json:"owner_name"`
	DueOn       string `json:"due_on"`
}

// MeetingIn drafts meeting minutes.
type MeetingIn struct {
	ProjectID string          `json:"project_id"`
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	HeldOn    string          `json:"held_on"`
	Attendees string          `json:"attendees"`
	Minutes   string          `json:"minutes"`
	Actions   []MeetingAction `json:"actions"`
}

// CreateMeeting drafts minutes with their actions.
func (s *Service) CreateMeeting(ctx context.Context, p auth.Principal, in MeetingIn) (string, error) {
	day, err := parseDay("held_on", in.HeldOn)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermSiteRecord, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "MIN")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO meetings (project_id, number, kind, title, held_on, attendees, minutes, created_by) VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, ''), $7, $8) RETURNING id::text`,
			in.ProjectID, num, strings.ToUpper(strings.TrimSpace(in.Kind)), strings.TrimSpace(in.Title), day, strings.TrimSpace(in.Attendees), strings.TrimSpace(in.Minutes), p.UserID)
		if err != nil {
			return err
		}
		for _, a := range in.Actions {
			due, err := parseDay("due_on", a.DueOn)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO meeting_actions (meeting_id, description, owner_name, due_on) VALUES ($1::uuid, $2, $3, $4)`, id, strings.TrimSpace(a.Description), strings.TrimSpace(a.Owner), due); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "meeting.create", "meeting", id, "meeting.created", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// IssueMinutes issues draft minutes (they never change afterwards).
func (s *Service) IssueMinutes(ctx context.Context, p auth.Principal, id string) error {
	return s.stepByID(ctx, p, "meetings", "meeting", id, `UPDATE meetings SET status = 'ISSUED' WHERE id = $1::uuid AND status = 'DRAFT'`, "meeting.issue", []auth.Permission{auth.PermSiteRecord})
}

// CompleteMeetingAction marks an action done.
func (s *Service) CompleteMeetingAction(ctx context.Context, p auth.Principal, id string) error {
	var project string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT m.project_id::text FROM meeting_actions a JOIN meetings m ON m.id = a.meeting_id WHERE a.id = $1::uuid`, id).Scan(&project))
	}); err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, auth.PermSiteRecord, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE meeting_actions SET status = 'DONE', done_on = current_date WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "meeting.action.done", "meeting_action", id, "meeting.action_done", map[string]any{"id": id})
	})
}

// SiteView lists punch items, instructions and meetings.
func (s *Service) SiteView(ctx context.Context, p auth.Principal) (map[string]any, error) {
	out := map[string]any{}
	if !s.authz.Has(p, auth.PermSiteRecord) && !s.authz.Has(p, auth.PermSiteInstruct) {
		return out, auth.ErrForbidden
	}
	wide, projects := s.scopeArgs(p)
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out["projects"], err = rowsAsMaps(ctx, tx, `SELECT id, code, name FROM projects WHERE $1 OR id = ANY($2::uuid[]) ORDER BY code`, wide, projects); err != nil {
			return err
		}
		if out["punch"], err = rowsAsMaps(ctx, tx, `SELECT x.id, x.number, pr.code AS project, x.location, x.description, x.assignee, x.priority, x.due_on, x.status, x.raised_by, x.fixed_by, (x.status <> 'CLOSED' AND x.due_on < current_date) AS overdue
			FROM punch_items x JOIN projects pr ON pr.id = x.project_id WHERE ($1 OR x.project_id = ANY($2::uuid[])) ORDER BY (x.status = 'CLOSED'), x.due_on NULLS LAST, x.number DESC LIMIT 300`, wide, projects); err != nil {
			return err
		}
		if out["instructions"], err = rowsAsMaps(ctx, tx, `SELECT x.id, x.number, pr.code AS project, x.title, x.body, x.status, x.issued_by, x.acknowledged_by, x.issued_at,
			(SELECT number FROM change_events c WHERE c.id = x.change_event_id) AS change_event FROM site_instructions x JOIN projects pr ON pr.id = x.project_id
			WHERE ($1 OR x.project_id = ANY($2::uuid[])) ORDER BY x.issued_at DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["change_events"], err = rowsAsMaps(ctx, tx, `SELECT c.id, c.number, c.title, c.project_id FROM change_events c WHERE ($1 OR c.project_id = ANY($2::uuid[])) ORDER BY c.number DESC LIMIT 100`, wide, projects); err != nil {
			out["change_events"] = []map[string]any{}
		}
		if out["meetings"], err = rowsAsMaps(ctx, tx, `SELECT m.id, m.number, pr.code AS project, m.kind, m.title, m.held_on, m.attendees, m.minutes, m.status, m.created_by,
			(SELECT json_agg(json_build_object('id', a.id, 'description', a.description, 'owner_name', a.owner_name, 'due_on', a.due_on, 'status', a.status, 'overdue', (a.status = 'OPEN' AND a.due_on < current_date)) ORDER BY a.due_on) FROM meeting_actions a WHERE a.meeting_id = m.id) AS actions
			FROM meetings m JOIN projects pr ON pr.id = m.project_id WHERE ($1 OR m.project_id = ANY($2::uuid[])) ORDER BY m.held_on DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		return nil
	})
	return out, err
}
