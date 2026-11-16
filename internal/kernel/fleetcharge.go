// Equipment charge-out to projects (FLT-01.14; migration 0079). Rates are
// approved policy documents; a charge run prices a project's approved logs of
// a month and produces an EQP_CHG subledger journal that finance approves and
// posts like any journal.
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

// ChargeRateInput prepares a machine's charge-out rate from a date.
type ChargeRateInput struct {
	EquipmentID    string          `json:"equipment_id"`
	EffectiveFrom  string          `json:"effective_from"`
	ProductiveRate decimal.Decimal `json:"productive_rate"`
	IdleRate       decimal.Decimal `json:"idle_rate"`
	Basis          string          `json:"basis"`
}

// CreateChargeRate prepares a DRAFT rate (fleet.manage). Rates are the
// business's policy inputs; none is shipped.
func (s *Service) CreateChargeRate(ctx context.Context, p auth.Principal, in ChargeRateInput) (string, error) {
	from, err := parseDay("effective_from", in.EffectiveFrom)
	if err != nil {
		return "", err
	}
	if !in.ProductiveRate.IsPositive() || in.IdleRate.IsNegative() || len(strings.TrimSpace(in.Basis)) < 5 {
		return "", fmt.Errorf("%w: a charge rate has a positive rate per working hour, an idle rate of 0 or more and its basis", ErrInvalidInput)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermFleetManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "ECR")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_charge_rates (number, equipment_id, effective_from, productive_rate, idle_rate, basis, created_by)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7) RETURNING id::text`, num, in.EquipmentID, from, in.ProductiveRate.Round(6), in.IdleRate.Round(6), strings.TrimSpace(in.Basis), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.charge_rate.create", "equipment_charge_rate", id, "fleet.charge_rate_prepared", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// SubmitChargeRate submits the preparer's draft.
func (s *Service) SubmitChargeRate(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermFleetManage, "equipment_charge_rate", "equipment_charge_rates", id, "fleet.charge_rate.submit", "fleet.charge_rate_submitted")
}

// DecideChargeRate approves or rejects (fleet.approve, fresh MFA, not the preparer; attested).
func (s *Service) DecideChargeRate(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermFleetApprove, "equipment_charge_rate", "equipment_charge_rates", id, "fleet.charge_rate.decide", "charge rate", approve, reason, nil)
}

// ChargeLine is one approved equipment log priced for a charge run.
type ChargeLine struct {
	LogID           string          `json:"log_id"`
	LogNumber       string          `json:"log_number"`
	LogDate         string          `json:"log_date"`
	EquipmentID     string          `json:"equipment_id"`
	Equipment       string          `json:"equipment"`
	RateID          string          `json:"rate_id,omitempty"`
	CostCodeID      string          `json:"cost_code_id,omitempty"`
	ProductiveHours decimal.Decimal `json:"productive_hours"`
	IdleHours       decimal.Decimal `json:"idle_hours"`
	Amount          decimal.Decimal `json:"amount"`
}

// ChargePreview is what a run would charge, and the logs it cannot price.
type ChargePreview struct {
	Lines    []ChargeLine    `json:"lines"`
	Unpriced []ChargeLine    `json:"unpriced"`
	Total    decimal.Decimal `json:"total"`
}

func chargePreview(ctx context.Context, tx pgx.Tx, project string, month time.Time) (ChargePreview, error) {
	out := ChargePreview{Lines: []ChargeLine{}, Unpriced: []ChargeLine{}}
	rows, err := tx.Query(ctx, `
		WITH logs AS (
		  SELECT l.id, l.number, l.log_date, l.equipment_id, e.number || ' ' || e.name AS eq, l.cost_code_id, l.productive_hours, l.idle_hours,
		         equipment_charge_rate_at(l.equipment_id, l.log_date) AS rate_id
		    FROM equipment_logs l JOIN equipment e ON e.id = l.equipment_id
		   WHERE l.project_id = $1::uuid AND l.status = 'APPROVED' AND date_trunc('month', l.log_date)::date = $2::date
		     AND NOT EXISTS (SELECT 1 FROM equipment_charge_lines x JOIN equipment_charge_runs r ON r.id = x.run_id WHERE x.log_id = l.id AND r.status = 'PREPARED'))
		SELECT g.id::text, g.number, g.log_date::text, g.equipment_id::text, g.eq, COALESCE(g.rate_id::text, ''), COALESCE(g.cost_code_id::text, ''), g.productive_hours, g.idle_hours,
		       COALESCE(round_half_even(g.productive_hours * r.productive_rate + g.idle_hours * r.idle_rate, currency_minor_units(le.functional_currency)), 0)
		  FROM logs g LEFT JOIN equipment_charge_rates r ON r.id = g.rate_id
		  JOIN projects p ON p.id = $1::uuid JOIN legal_entities le ON le.id = p.legal_entity_id
		 ORDER BY g.log_date, g.number`, project, month)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var l ChargeLine
		if err := rows.Scan(&l.LogID, &l.LogNumber, &l.LogDate, &l.EquipmentID, &l.Equipment, &l.RateID, &l.CostCodeID, &l.ProductiveHours, &l.IdleHours, &l.Amount); err != nil {
			return out, err
		}
		if l.RateID == "" {
			out.Unpriced = append(out.Unpriced, l)
			continue
		}
		out.Lines = append(out.Lines, l)
		out.Total = out.Total.Add(l.Amount)
	}
	return out, rows.Err()
}

func (s *Service) chargeTx(ctx context.Context, p auth.Principal, project string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := s.authz.RequireProject(p, auth.PermJournalCreate, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, fn)
}

// PreviewChargeRun shows what a project's month would be charged.
func (s *Service) PreviewChargeRun(ctx context.Context, p auth.Principal, project, month string) (ChargePreview, error) {
	m, err := parseMonth("month", month)
	if err != nil {
		return ChargePreview{}, err
	}
	var out ChargePreview
	err = s.chargeTx(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = chargePreview(ctx, tx, project, m)
		return err
	})
	return out, err
}

// ChargeRunInput prepares a charge run.
type ChargeRunInput struct {
	ProjectID         string `json:"project_id"`
	Month             string `json:"month"` // YYYY-MM
	CostAccountID     string `json:"cost_account_id"`
	RecoveryAccountID string `json:"recovery_account_id"`
}

// PrepareChargeRun charges a project's approved logs of a month at the rates in
// force and creates the run's DRAFT EQP_CHG journal. It refuses while any
// approved log of the month has no approved rate: nothing is charged at a
// guessed rate, and no log is left out silently.
func (s *Service) PrepareChargeRun(ctx context.Context, p auth.Principal, in ChargeRunInput) (string, error) {
	m, err := parseMonth("month", in.Month)
	if err != nil {
		return "", err
	}
	var id string
	err = s.chargeTx(ctx, p, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		pv, err := chargePreview(ctx, tx, in.ProjectID, m)
		if err != nil {
			return err
		}
		if len(pv.Unpriced) > 0 {
			names := []string{}
			for _, u := range pv.Unpriced {
				names = append(names, u.LogNumber+" ("+u.Equipment+")")
			}
			return fmt.Errorf("%w: no approved charge rate for %s; approve a rate before charging the month", ErrInvalidInput, strings.Join(names, ", "))
		}
		if len(pv.Lines) == 0 {
			return fmt.Errorf("%w: no approved equipment log of %s is left to charge", ErrInvalidInput, m.Format("January 2006"))
		}
		num, err := postgres.NextNumber(ctx, tx, "ECH")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_charge_runs (number, project_id, month, cost_account_id, recovery_account_id, created_by)
			VALUES ($1, $2::uuid, $3, $4::uuid, $5::uuid, $6) RETURNING id::text`, num, in.ProjectID, m, in.CostAccountID, in.RecoveryAccountID, p.UserID)
		if err != nil {
			return err
		}
		for _, l := range pv.Lines {
			if _, err := tx.Exec(ctx, `INSERT INTO equipment_charge_lines (run_id, log_id, equipment_id, rate_id, cost_code_id, productive_hours, idle_hours, amount)
				VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5, '')::uuid, $6, $7, $8)`, id, l.LogID, l.EquipmentID, l.RateID, l.CostCodeID, l.ProductiveHours, l.IdleHours, l.Amount); err != nil {
				return err
			}
		}
		var entity, ccy string
		if err := tx.QueryRow(ctx, `SELECT p.legal_entity_id::text, e.functional_currency FROM projects p JOIN legal_entities e ON e.id = p.legal_entity_id WHERE p.id = $1::uuid`, in.ProjectID).Scan(&entity, &ccy); err != nil {
			return err
		}
		jid, err := s.subledgerJournal(ctx, tx, p, "EQP_CHG", id, entity, ccy, "Equipment charge-out "+m.Format("January 2006"), m.AddDate(0, 1, -1))
		if err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE equipment_charge_runs SET journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.charge_run.prepare", "equipment_charge_run", id, "fleet.charge_run_prepared", map[string]any{"id": id, "journal_id": jid, "total": pv.Total.String()})
	})
	return id, err
}

// CancelChargeRun cancels a run whose journal is still a draft (finance), with its journal; its logs can be charged again.
func (s *Service) CancelChargeRun(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	if !s.authz.Has(p, auth.PermJournalApprove) && !s.authz.Has(p, auth.PermJournalPost) {
		return fmt.Errorf("%w: finance (journal approval or posting) cancels a charge run", auth.ErrForbidden)
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var jid string
		if err := tx.QueryRow(ctx, `SELECT journal_id::text FROM equipment_charge_runs WHERE id = $1::uuid FOR UPDATE`, id).Scan(&jid); err != nil {
			return mapNoRows(err)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE equipment_charge_runs SET status = 'CANCELLED', cancel_reason = $2 WHERE id = $1::uuid AND status = 'PREPARED'`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		if err := s.cancelJournalTx(ctx, tx, p, jid, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.charge_run.cancel", "equipment_charge_run", id, "fleet.charge_run_cancelled", map[string]any{"id": id, "reason": reason})
	})
}
