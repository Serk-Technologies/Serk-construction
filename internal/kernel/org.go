// Organisation structure and project master (WP-1.7; SRS ORG-01, ORG-02,
// ORG-09). Database side: migration 0042.
//
//   - Regions and business units are reference data for roll-ups.
//   - A project is more than a code and a name: client, contract type,
//     location, dates, manager, region, business unit and phase.
//   - A project created by a person starts PLANNED in phase BID. Its phase
//     moves only through a stage gate: one person prepares and submits it with
//     the evidence, a different person decides it with a fresh multi-factor
//     sign-in, every step attested. Approving moves the project atomically.
//   - Company holidays and exceptional working days are entered by people;
//     movable holidays change every year, so nothing is assumed.
package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ProjectPhases are the phases of a project's life, in order.
var ProjectPhases = []string{"BID", "MOBILISATION", "EXECUTION", "PRACTICAL_COMPLETION", "DEFECTS", "CLOSED"}

// Project is a project with its master data.
type Project struct {
	ID             string           `json:"id"`
	Code           string           `json:"code"`
	Name           string           `json:"name"`
	EntityID       string           `json:"legal_entity_id"`
	Entity         string           `json:"entity"`
	CustomerID     string           `json:"customer_id,omitempty"`
	Customer       string           `json:"customer,omitempty"`
	RegionID       string           `json:"region_id,omitempty"`
	Region         string           `json:"region,omitempty"`
	BusinessUnitID string           `json:"business_unit_id,omitempty"`
	BusinessUnit   string           `json:"business_unit,omitempty"`
	ContractType   string           `json:"contract_type,omitempty"`
	Latitude       *decimal.Decimal `json:"latitude,omitempty"`
	Longitude      *decimal.Decimal `json:"longitude,omitempty"`
	StartDate      string           `json:"start_date,omitempty"`
	PlannedFinish  string           `json:"planned_finish,omitempty"`
	ProjectManager string           `json:"project_manager,omitempty"`
	Description    string           `json:"description,omitempty"`
	Phase          string           `json:"phase"`
	Status         string           `json:"status"`
	OpenGateID     string           `json:"open_gate_id,omitempty"`
	OpenGateTo     string           `json:"open_gate_to,omitempty"`
	OpenGateStatus string           `json:"open_gate_status,omitempty"`
	CreatedBy      string           `json:"created_by,omitempty"`
}

// ProjectInput creates or updates a project's master data. Empty = not set.
type ProjectInput struct {
	EntityID       string           `json:"legal_entity_id"`
	Code           string           `json:"code"`
	Name           string           `json:"name"`
	CustomerID     string           `json:"customer_id"`
	RegionID       string           `json:"region_id"`
	BusinessUnitID string           `json:"business_unit_id"`
	ContractType   string           `json:"contract_type"`
	Latitude       *decimal.Decimal `json:"latitude"`
	Longitude      *decimal.Decimal `json:"longitude"`
	StartDate      string           `json:"start_date"`
	PlannedFinish  string           `json:"planned_finish"`
	ProjectManager string           `json:"project_manager"`
	Description    string           `json:"description"`
	Currency       string           `json:"contract_currency"`
}

func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func validDate(s string) error {
	if s == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return fmt.Errorf("%w: dates are YYYY-MM-DD", ErrInvalidInput)
	}
	return nil
}

// CreateProject creates a PLANNED project in phase BID. The creation is attested.
func (s *Service) CreateProject(ctx context.Context, p auth.Principal, in ProjectInput) (string, error) {
	if err := s.authz.Require(p, auth.PermProjectCreate, p.TenantID); err != nil {
		return "", err
	}
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	if len(in.Code) < 2 || len(in.Code) > 20 || len(in.Name) < 3 || in.EntityID == "" {
		return "", fmt.Errorf("%w: a project needs a code (2-20 characters), a name (3+ characters) and a company", ErrInvalidInput)
	}
	if err := validDate(in.StartDate); err != nil {
		return "", err
	}
	if err := validDate(in.PlannedFinish); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var cur *string
		if in.Currency != "" {
			cur = &in.Currency
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO projects (legal_entity_id, code, name, contract_currency, status, phase, customer_id, region_id, business_unit_id,
			contract_type, latitude, longitude, start_date, planned_finish, project_manager, description, created_by)
			SELECT $1::uuid, $2, $3, COALESCE($4, le.functional_currency), 'PLANNED', 'BID', $5::uuid, $6::uuid, $7::uuid, $8, $9, $10, $11::date, $12::date, $13, $14, $15
			  FROM legal_entities le WHERE le.id = $1::uuid RETURNING id::text`,
			in.EntityID, in.Code, in.Name, cur, nullable(in.CustomerID), nullable(in.RegionID), nullable(in.BusinessUnitID), nullable(in.ContractType),
			in.Latitude, in.Longitude, nullable(in.StartDate), nullable(in.PlannedFinish), nullable(in.ProjectManager), nullable(in.Description), p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		if err := s.authorize(ctx, tx, p, "project.create", "project", id, ""); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "project.create", "project", id, "project.created", map[string]any{"project_id": id, "code": in.Code})
	})
	return id, err
}

// UpdateProject changes a project's master data (not its code, company or phase).
func (s *Service) UpdateProject(ctx context.Context, p auth.Principal, id string, in ProjectInput) error {
	if err := s.authz.RequireProject(p, auth.PermProjectUpdate, p.TenantID, id); err != nil {
		return err
	}
	if err := validDate(in.StartDate); err != nil {
		return err
	}
	if err := validDate(in.PlannedFinish); err != nil {
		return err
	}
	if len(strings.TrimSpace(in.Name)) < 3 {
		return fmt.Errorf("%w: a project needs a name (3+ characters)", ErrInvalidInput)
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return s.updateProjectTx(ctx, tx, p, id, in)
	})
}

func (s *Service) updateProjectTx(ctx context.Context, tx pgx.Tx, p auth.Principal, id string, in ProjectInput) error {
	if err := postgres.Exec1(ctx, tx, `UPDATE projects SET name = $2, customer_id = $3::uuid, region_id = $4::uuid, business_unit_id = $5::uuid, contract_type = $6,
		latitude = $7, longitude = $8, start_date = $9::date, planned_finish = $10::date, project_manager = $11, description = $12 WHERE id = $1::uuid`,
		id, strings.TrimSpace(in.Name), nullable(in.CustomerID), nullable(in.RegionID), nullable(in.BusinessUnitID), nullable(in.ContractType),
		in.Latitude, in.Longitude, nullable(in.StartDate), nullable(in.PlannedFinish), nullable(in.ProjectManager), nullable(in.Description)); err != nil {
		return mapNoRows(err)
	}
	return s.record(ctx, tx, p, "project.update", "project", id, "project.updated", map[string]any{"project_id": id, "project_manager": in.ProjectManager})
}

// ListProjects lists every project in the caller's scope with its master data
// (any status, unlike the active-only lists in forms). A person also sees the
// projects they created: a new project is not in anyone's identity-provider
// scope yet, and its creator should not lose sight of it.
func (s *Service) ListProjects(ctx context.Context, p auth.Principal) ([]Project, error) {
	if !s.authz.Has(p, auth.PermP2PRead) && !s.authz.Has(p, auth.PermProjectCreate) && !s.authz.Has(p, auth.PermProjectUpdate) && !s.authz.Has(p, auth.PermProjectGateDecide) {
		return nil, auth.ErrForbidden
	}
	scope := s.authz.ScopeOf(p)
	projects := scope.Projects
	if projects == nil {
		projects = []string{}
	}
	out := []Project{}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT pr.id::text, pr.code, pr.name, pr.legal_entity_id::text, le.name, COALESCE(pr.customer_id::text, ''), COALESCE(c.name, ''),
			COALESCE(pr.region_id::text, ''), COALESCE(rg.name, ''), COALESCE(pr.business_unit_id::text, ''), COALESCE(bu.name, ''), COALESCE(pr.contract_type, ''),
			pr.latitude, pr.longitude, COALESCE(pr.start_date::text, ''), COALESCE(pr.planned_finish::text, ''), COALESCE(pr.project_manager, ''), COALESCE(pr.description, ''),
			pr.phase, pr.status, COALESCE(pr.created_by, ''), COALESCE(g.id::text, ''), COALESCE(g.to_phase, ''), COALESCE(g.status, '')
			FROM projects pr JOIN legal_entities le ON le.id = pr.legal_entity_id
			LEFT JOIN customers c ON c.id = pr.customer_id LEFT JOIN regions rg ON rg.id = pr.region_id LEFT JOIN business_units bu ON bu.id = pr.business_unit_id
			LEFT JOIN project_stage_gates g ON g.project_id = pr.id AND g.status IN ('DRAFT', 'SUBMITTED')
			WHERE ($1 OR pr.id = ANY($2::uuid[]) OR pr.created_by = $3) ORDER BY pr.code`, scope.TenantWide, projects, p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x Project
			if err := rows.Scan(&x.ID, &x.Code, &x.Name, &x.EntityID, &x.Entity, &x.CustomerID, &x.Customer, &x.RegionID, &x.Region, &x.BusinessUnitID, &x.BusinessUnit,
				&x.ContractType, &x.Latitude, &x.Longitude, &x.StartDate, &x.PlannedFinish, &x.ProjectManager, &x.Description, &x.Phase, &x.Status, &x.CreatedBy,
				&x.OpenGateID, &x.OpenGateTo, &x.OpenGateStatus); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// CreateStageGate drafts the step from the project's current phase to toPhase,
// with the evidence for passing it.
func (s *Service) CreateStageGate(ctx context.Context, p auth.Principal, projectID, toPhase, notes string) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermProjectGateSubmit, p.TenantID, projectID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var from string
		if err := tx.QueryRow(ctx, `SELECT phase FROM projects WHERE id = $1::uuid`, projectID).Scan(&from); err != nil {
			return mapNoRows(err)
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO project_stage_gates (project_id, from_phase, to_phase, notes, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`, projectID, from, strings.ToUpper(strings.TrimSpace(toPhase)), notes, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "project.gate.create", "project_stage_gate", id, "project.gate.created", map[string]any{"gate_id": id, "from": from, "to": toPhase})
	})
	return id, err
}

// SubmitStageGate submits a draft gate (by its author).
func (s *Service) SubmitStageGate(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermProjectGateSubmit, "project_stage_gate", "project_stage_gates", id, "project.gate.submit", "project.gate.submitted")
}

// DecideStageGate approves (moving the project) or rejects a submitted gate.
// Approval needs a fresh multi-factor sign-in and a person other than the author.
func (s *Service) DecideStageGate(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermProjectGateDecide, "project_stage_gate", "project_stage_gates", id, "project.gate.decide", "stage gate", approve, reason, nil)
}

// ---------------------------------------------------------------- reference data

// OrgItem is a region or business unit.
type OrgItem struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

var orgTables = map[string]string{"region": "regions", "business_unit": "business_units"}

// CreateOrgItem adds a region or business unit.
func (s *Service) CreateOrgItem(ctx context.Context, p auth.Principal, kind, code, name string) (string, error) {
	table, ok := orgTables[kind]
	if !ok {
		return "", ErrNotFound
	}
	if err := s.authz.Require(p, auth.PermOrgManage, p.TenantID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO `+table+` (code, name, created_by) VALUES ($1, $2, $3) RETURNING id::text`, strings.TrimSpace(code), strings.TrimSpace(name), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "org."+kind+".create", kind, id, "org."+kind+".created", map[string]any{"id": id, "code": code, "name": name})
	})
	return id, err
}

// ListOrgItems lists regions or business units (any signed-in reader of projects).
func (s *Service) ListOrgItems(ctx context.Context, p auth.Principal, kind string) ([]OrgItem, error) {
	table, ok := orgTables[kind]
	if !ok {
		return nil, ErrNotFound
	}
	if !s.authz.Has(p, auth.PermP2PRead) && !s.authz.Has(p, auth.PermOrgManage) && !s.authz.Has(p, auth.PermProjectCreate) {
		return nil, auth.ErrForbidden
	}
	out := []OrgItem{}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, code, name FROM `+table+` ORDER BY code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x OrgItem
			if err := rows.Scan(&x.ID, &x.Code, &x.Name); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// CalendarException is a company holiday or an exceptional working day.
type CalendarException struct {
	ID   string `json:"id"`
	Day  string `json:"day"`
	Name string `json:"name"`
	Kind string `json:"kind"` // HOLIDAY | WORKING
}

// AddCalendarException records a holiday (or an exceptional working day).
func (s *Service) AddCalendarException(ctx context.Context, p auth.Principal, day, name, kind string) (string, error) {
	if err := s.authz.Require(p, auth.PermOrgManage, p.TenantID); err != nil {
		return "", err
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return "", fmt.Errorf("%w: the day is YYYY-MM-DD", ErrInvalidInput)
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO calendar_exceptions (day, name, kind, created_by) VALUES ($1::date, $2, $3, $4) RETURNING id::text`,
			day, strings.TrimSpace(name), strings.ToUpper(strings.TrimSpace(kind)), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "org.calendar.add", "calendar_exception", id, "org.calendar.added", map[string]any{"day": day, "name": name, "kind": kind})
	})
	return id, err
}

// RemoveCalendarException removes an entry (it is kept, marked removed, and the audit trail records who).
func (s *Service) RemoveCalendarException(ctx context.Context, p auth.Principal, id string) error {
	if err := s.authz.Require(p, auth.PermOrgManage, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE calendar_exceptions SET removed_at = now(), removed_by = $2 WHERE id = $1::uuid AND removed_at IS NULL`, id, p.UserID); err != nil {
			return ErrNotFound
		}
		return s.record(ctx, tx, p, "org.calendar.remove", "calendar_exception", id, "org.calendar.removed", map[string]any{"id": id})
	})
}

// ListCalendarExceptions lists the entries in date order.
func (s *Service) ListCalendarExceptions(ctx context.Context, p auth.Principal) ([]CalendarException, error) {
	if !s.authz.Has(p, auth.PermP2PRead) && !s.authz.Has(p, auth.PermOrgManage) {
		return nil, auth.ErrForbidden
	}
	out := []CalendarException{}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, day::text, name, kind FROM calendar_exceptions WHERE removed_at IS NULL ORDER BY day`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x CalendarException
			if err := rows.Scan(&x.ID, &x.Day, &x.Name, &x.Kind); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
