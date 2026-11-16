// Resource rate library and rate build-ups (EST-06; migration 0094).
//
// The figures of a build-up are computed by the database (rate_buildup_figures)
// and fixed on submission; nothing here computes a price.
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

// ResourceRateInput is a priced resource with its provenance.
type ResourceRateInput struct {
	LegalEntityID string          `json:"legal_entity_id"`
	Code          string          `json:"code"`
	Description   string          `json:"description"`
	Kind          string          `json:"kind"` // MATERIAL, LABOUR, EQUIPMENT, SUBCONTRACT
	Unit          string          `json:"unit"`
	Currency      string          `json:"currency"`
	Rate          decimal.Decimal `json:"rate"`
	Source        string          `json:"source"`
	Region        string          `json:"region"`
	Confidence    string          `json:"confidence"` // LOW, MEDIUM, HIGH
	FXDate        string          `json:"fx_date,omitempty"`
	ValidFrom     string          `json:"valid_from"`
	ValidTo       string          `json:"valid_to"`
}

// RecordResourceRate records a PENDING rate (boq.manage).
func (s *Service) RecordResourceRate(ctx context.Context, p auth.Principal, in ResourceRateInput) (string, error) {
	from, err := parseDay("valid_from", in.ValidFrom)
	if err != nil {
		return "", err
	}
	to, err := parseDay("valid_to", in.ValidTo)
	if err != nil {
		return "", err
	}
	if to.Before(from) {
		return "", fmt.Errorf("%w: a rate's validity ends on or after it starts", ErrInvalidInput)
	}
	var fx any
	if strings.TrimSpace(in.FXDate) != "" {
		d, err := parseDay("fx_date", in.FXDate)
		if err != nil {
			return "", err
		}
		fx = d
	}
	if !in.Rate.IsPositive() {
		return "", fmt.Errorf("%w: a rate is more than zero", ErrInvalidInput)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermBOQManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO resource_rates (legal_entity_id, code, number, description, kind, unit, currency, rate, source, region, confidence, fx_date, valid_from, valid_to, created_by)
			VALUES ($1::uuid, $2, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id::text`,
			in.LegalEntityID, strings.ToUpper(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Description), strings.ToUpper(strings.TrimSpace(in.Kind)), strings.TrimSpace(in.Unit),
			strings.ToUpper(strings.TrimSpace(in.Currency)), in.Rate, strings.TrimSpace(in.Source), strings.TrimSpace(in.Region), strings.ToUpper(strings.TrimSpace(in.Confidence)),
			fx, from, to, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "resource_rate.record", "resource_rate", id, "estimate.rate_recorded", map[string]any{"id": id, "code": in.Code})
	})
	return id, err
}

// DecideResourceRate approves or rejects a rate (boq.approve, not its recorder; attested).
func (s *Service) DecideResourceRate(ctx context.Context, p auth.Principal, id string, approve bool, note string) error {
	outcome, err := decisionOutcome(approve, note)
	if err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermBOQApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermBOQApprove, "resource_rate", id)
		if err != nil {
			return err
		}
		if d.Status != "PENDING" {
			return fmt.Errorf("%w: the rate was already decided", ErrWorkflowState)
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "resource rate"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "resource_rate.decide", "resource_rate", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE resource_rates SET status = $2, approved_by = $3, decision_note = NULLIF($4, '') WHERE id = $1::uuid`,
			id, outcome, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "resource_rate.decide", "resource_rate", id, "estimate.rate_"+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

func decisionOutcome(approve bool, note string) (string, error) {
	if approve {
		return "APPROVED", nil
	}
	if len(strings.TrimSpace(note)) < 5 {
		return "", fmt.Errorf("%w: rejecting needs a reason", ErrInvalidInput)
	}
	return "REJECTED", nil
}

// RateBuildupLineInput is one resource of a build-up: how much of it one unit
// of output takes, and the wastage allowed on that quantity.
type RateBuildupLineInput struct {
	ResourceRateID string          `json:"resource_rate_id"`
	Quantity       decimal.Decimal `json:"quantity"`
	WastagePct     decimal.Decimal `json:"wastage_pct"`
}

// RateBuildupInput starts a build-up on a project.
type RateBuildupInput struct {
	ProjectID      string                 `json:"project_id"`
	Code           string                 `json:"code"`
	Description    string                 `json:"description"`
	Unit           string                 `json:"unit"`
	Currency       string                 `json:"currency"`
	PriceDate      string                 `json:"price_date"`
	OverheadPct    decimal.Decimal        `json:"overhead_pct"`
	ContingencyPct decimal.Decimal        `json:"contingency_pct"`
	EscalationPct  decimal.Decimal        `json:"escalation_pct"`
	MarginPct      decimal.Decimal        `json:"margin_pct"`
	Lines          []RateBuildupLineInput `json:"lines"`
}

// CreateRateBuildup creates a DRAFT build-up with its lines (boq.manage on the project).
func (s *Service) CreateRateBuildup(ctx context.Context, p auth.Principal, in RateBuildupInput) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermBOQManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	day, err := parseDay("price_date", in.PriceDate)
	if err != nil {
		return "", err
	}
	if len(in.Lines) == 0 || len(in.Lines) > 200 {
		return "", fmt.Errorf("%w: a build-up has 1 to 200 resource lines", ErrInvalidInput)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermBOQManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO rate_buildups (legal_entity_id, project_id, code, number, description, unit, currency, price_date,
				overhead_pct, contingency_pct, escalation_pct, margin_pct, created_by)
			SELECT pr.legal_entity_id, pr.id, $2, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11 FROM projects pr WHERE pr.id = $1::uuid RETURNING id::text`,
			in.ProjectID, strings.ToUpper(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Description), strings.TrimSpace(in.Unit), strings.ToUpper(strings.TrimSpace(in.Currency)), day,
			in.OverheadPct, in.ContingencyPct, in.EscalationPct, in.MarginPct, p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		if err := insertBuildupLines(ctx, tx, id, 1, in.Lines); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rate_buildup.create", "rate_buildup", id, "estimate.buildup_created", map[string]any{"id": id, "project_id": in.ProjectID, "code": in.Code})
	})
	return id, err
}

func insertBuildupLines(ctx context.Context, tx pgx.Tx, id string, first int, lines []RateBuildupLineInput) error {
	for i, l := range lines {
		// kind, unit, rate and amount are the rate's and the database's, set by the guard.
		if _, err := tx.Exec(ctx, `INSERT INTO rate_buildup_lines (buildup_id, line_no, resource_rate_id, kind, unit, rate, quantity, wastage_pct, amount)
			VALUES ($1::uuid, $2, $3::uuid, '', '', 0, $4, $5, 0)`, id, first+i, l.ResourceRateID, l.Quantity, l.WastagePct); err != nil {
			return err
		}
	}
	return nil
}

// AddRateBuildupLines appends lines to a draft build-up.
func (s *Service) AddRateBuildupLines(ctx context.Context, p auth.Principal, id string, lines []RateBuildupLineInput) error {
	if len(lines) == 0 {
		return fmt.Errorf("%w: no line to add", ErrInvalidInput)
	}
	return s.p2pTx(ctx, p, auth.PermBOQManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermBOQManage, "rate_buildup", id); err != nil {
			return err
		}
		var next int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(line_no), 0) + 1 FROM rate_buildup_lines WHERE buildup_id = $1::uuid`, id).Scan(&next); err != nil {
			return err
		}
		if err := insertBuildupLines(ctx, tx, id, next, lines); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rate_buildup.edit", "rate_buildup", id, "estimate.buildup_lines_added", map[string]any{"id": id, "lines": len(lines)})
	})
}

// CorrectRateBuildupLine changes the quantity and wastage of a draft line.
func (s *Service) CorrectRateBuildupLine(ctx context.Context, p auth.Principal, id string, lineNo int, quantity, wastagePct decimal.Decimal) error {
	return s.p2pTx(ctx, p, auth.PermBOQManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermBOQManage, "rate_buildup", id); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE rate_buildup_lines SET quantity = $3, wastage_pct = $4 WHERE buildup_id = $1::uuid AND line_no = $2`, id, lineNo, quantity, wastagePct); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rate_buildup.edit", "rate_buildup", id, "estimate.buildup_line_corrected", map[string]any{"id": id, "line_no": lineNo})
	})
}

// SetRateBuildupPercentages changes the overhead, contingency, escalation and margin of a draft.
func (s *Service) SetRateBuildupPercentages(ctx context.Context, p auth.Principal, id string, overhead, contingency, escalation, margin decimal.Decimal) error {
	return s.p2pTx(ctx, p, auth.PermBOQManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermBOQManage, "rate_buildup", id); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE rate_buildups SET overhead_pct = $2, contingency_pct = $3, escalation_pct = $4, margin_pct = $5 WHERE id = $1::uuid`,
			id, overhead, contingency, escalation, margin); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rate_buildup.edit", "rate_buildup", id, "estimate.buildup_percentages_set", map[string]any{"id": id})
	})
}

// SubmitRateBuildup fixes the figures of a draft and sends it for review.
func (s *Service) SubmitRateBuildup(ctx context.Context, p auth.Principal, id string) error {
	return s.p2pTx(ctx, p, auth.PermBOQManage, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermBOQManage, "rate_buildup", id)
		if err != nil {
			return err
		}
		if d.Status != "DRAFT" {
			return fmt.Errorf("%w: only a draft is submitted", ErrWorkflowState)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE rate_buildups SET status = 'SUBMITTED' WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rate_buildup.submit", "rate_buildup", id, "estimate.buildup_submitted", map[string]any{"id": id})
	})
}

// DecideRateBuildup approves (freezes) or rejects a submitted build-up
// (boq.approve, not its author; attested).
func (s *Service) DecideRateBuildup(ctx context.Context, p auth.Principal, id string, approve bool, note string) error {
	outcome, err := decisionOutcome(approve, note)
	if err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermBOQApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermBOQApprove, "rate_buildup", id)
		if err != nil {
			return err
		}
		if d.Status != "SUBMITTED" {
			return fmt.Errorf("%w: only a submitted build-up is decided", ErrWorkflowState)
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "rate build-up"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "rate_buildup.decide", "rate_buildup", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE rate_buildups SET status = $2, approved_by = $3, decision_note = NULLIF($4, '') WHERE id = $1::uuid`,
			id, outcome, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rate_buildup.decide", "rate_buildup", id, "estimate.buildup_"+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// ReviseRateBuildup starts the next revision of the latest decided build-up:
// a draft with the same resources and quantities, each priced at the approved
// rate of the same code valid on the new price date.
func (s *Service) ReviseRateBuildup(ctx context.Context, p auth.Principal, id, priceDate string) (string, error) {
	day, err := parseDay("price_date", priceDate)
	if err != nil {
		return "", err
	}
	var nid string
	err = s.p2pTx(ctx, p, auth.PermBOQManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermBOQManage, "rate_buildup", id); err != nil {
			return err
		}
		var err error
		nid, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO rate_buildups (legal_entity_id, project_id, code, number, description, unit, currency, price_date,
				overhead_pct, contingency_pct, escalation_pct, margin_pct, revision, supersedes_id, created_by)
			SELECT legal_entity_id, project_id, code, code, description, unit, currency, $2, overhead_pct, contingency_pct, escalation_pct, margin_pct, revision + 1, id, $3
			  FROM rate_buildups WHERE id = $1::uuid RETURNING id::text`, id, day, p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		var missing string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(string_agg(r.code, ', ' ORDER BY l.line_no), '') FROM rate_buildup_lines l JOIN resource_rates r ON r.id = l.resource_rate_id
			WHERE l.buildup_id = $1::uuid AND NOT EXISTS (SELECT 1 FROM resource_rates n WHERE n.legal_entity_id = r.legal_entity_id AND n.code = r.code
			  AND n.status = 'APPROVED' AND $2::date BETWEEN n.valid_from AND n.valid_to)`, id, day).Scan(&missing); err != nil {
			return err
		}
		if missing != "" {
			return fmt.Errorf("%w: no approved rate valid on %s for %s", ErrInvalidInput, priceDate, missing)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO rate_buildup_lines (buildup_id, line_no, resource_rate_id, kind, unit, rate, quantity, wastage_pct, amount)
			SELECT $2::uuid, l.line_no, n.id, '', '', 0, l.quantity, l.wastage_pct, 0
			  FROM rate_buildup_lines l JOIN resource_rates r ON r.id = l.resource_rate_id
			  JOIN resource_rates n ON n.legal_entity_id = r.legal_entity_id AND n.code = r.code AND n.status = 'APPROVED' AND $3::date BETWEEN n.valid_from AND n.valid_to
			 WHERE l.buildup_id = $1::uuid ORDER BY l.line_no`, id, nid, day); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rate_buildup.create", "rate_buildup", nid, "estimate.buildup_revised", map[string]any{"id": nid, "supersedes_id": id})
	})
	return nid, err
}

// RateBuildupFigures are the database's figures for a build-up.
type RateBuildupFigures struct {
	Materials   decimal.Decimal  `json:"materials"`
	Labour      decimal.Decimal  `json:"labour"`
	Equipment   decimal.Decimal  `json:"equipment"`
	Subcontract decimal.Decimal  `json:"subcontract"`
	DirectCost  decimal.Decimal  `json:"direct_cost"`
	Overhead    decimal.Decimal  `json:"overhead"`
	Contingency decimal.Decimal  `json:"contingency"`
	Escalation  decimal.Decimal  `json:"escalation"`
	FullCost    decimal.Decimal  `json:"full_cost"`
	SellingRate decimal.Decimal  `json:"selling_rate"`
	MarkupPct   *decimal.Decimal `json:"markup_pct"`
	// Fixed is true once the build-up was submitted: the stored figures, which
	// equal these because its lines no longer change.
	Fixed bool `json:"fixed"`
}

// BuildupFigures returns a build-up's figures as the database computes them.
func (s *Service) BuildupFigures(ctx context.Context, p auth.Principal, id string) (RateBuildupFigures, error) {
	var f RateBuildupFigures
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.visibleDoc(ctx, tx, p, "rate_buildup", id, false)
		if err != nil {
			return err
		}
		f.Fixed = d.Status != "DRAFT"
		return tx.QueryRow(ctx, `SELECT materials, labour, equipment, subcontract, direct_cost, overhead, contingency, escalation, full_cost, selling_rate, markup_pct
			FROM rate_buildup_figures($1::uuid)`, id).Scan(&f.Materials, &f.Labour, &f.Equipment, &f.Subcontract, &f.DirectCost, &f.Overhead, &f.Contingency,
			&f.Escalation, &f.FullCost, &f.SellingRate, &f.MarkupPct)
	})
	return f, err
}
