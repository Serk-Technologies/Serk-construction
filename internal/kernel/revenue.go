// Revenue over time and the month-end WIP adjustment (WP-2.2 completion and
// WP-2.3; SRS PCC-06, FIN-15, CON-07). Migration: 0053_revenue_recognition.sql.
//
// A revenue policy (per legal entity) names the contract asset and contract
// liability accounts and the progress measure; a controller proposes it and
// the CFO approves it. Each month a project accountant runs revenue for a
// project: the database computes, per active contract, revenue earned to date
// (contract value × progress), billed to date, and the adjustment between
// them, and the run generates a DRAFT journal that moves the contract asset
// and liability from the last posted run's balances to the new ones. Finance
// approves and a different person posts it, like every subledger journal.
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

// RevenuePolicyInput proposes an entity's revenue policy.
type RevenuePolicyInput struct {
	LegalEntityID       string `json:"legal_entity_id"`
	Method              string `json:"method"` // INPUT_COST | OUTPUT_MEASURED
	ContractAssetAcct   string `json:"contract_asset_account_id"`
	ContractLiabilityAc string `json:"contract_liability_account_id"`
}

// RevenueLine is one contract of a run (or a preview).
type RevenueLine struct {
	ContractID     string          `json:"contract_id"`
	ContractNumber string          `json:"contract_number"`
	ContractValue  decimal.Decimal `json:"contract_value"`
	Measure        decimal.Decimal `json:"measure"`
	MeasureTotal   decimal.Decimal `json:"measure_total"`
	Progress       decimal.Decimal `json:"progress"`
	Earned         decimal.Decimal `json:"earned"`
	Billed         decimal.Decimal `json:"billed"`
	Cumulative     decimal.Decimal `json:"cumulative"`
	Previous       decimal.Decimal `json:"previous"`
	Movement       decimal.Decimal `json:"movement"`
}

// RevenueRun is a prepared run with its lines and journal.
type RevenueRun struct {
	ID            string        `json:"id,omitempty"`
	Number        string        `json:"number,omitempty"`
	ProjectID     string        `json:"project_id"`
	PeriodEnd     string        `json:"period_end"`
	Method        string        `json:"method"`
	Currency      string        `json:"currency"`
	Status        string        `json:"status,omitempty"`
	JournalID     string        `json:"journal_id,omitempty"`
	JournalStatus string        `json:"journal_status,omitempty"`
	CreatedBy     string        `json:"created_by,omitempty"`
	CancelReason  string        `json:"cancel_reason,omitempty"`
	Lines         []RevenueLine `json:"lines"`
	// Totals: positive cumulative is a contract asset, negative a liability.
	ContractAsset     decimal.Decimal `json:"contract_asset"`
	ContractLiability decimal.Decimal `json:"contract_liability"`
	RevenueMovement   decimal.Decimal `json:"revenue_movement"`
}

func (r *RevenueRun) total() {
	r.ContractAsset, r.ContractLiability, r.RevenueMovement = decimal.Zero, decimal.Zero, decimal.Zero
	for _, l := range r.Lines {
		if l.Cumulative.IsPositive() {
			r.ContractAsset = r.ContractAsset.Add(l.Cumulative)
		} else {
			r.ContractLiability = r.ContractLiability.Sub(l.Cumulative)
		}
		r.RevenueMovement = r.RevenueMovement.Add(l.Movement)
	}
}

// ProposeRevenuePolicy proposes the next version of an entity's revenue policy.
func (s *Service) ProposeRevenuePolicy(ctx context.Context, p auth.Principal, in RevenuePolicyInput) (string, error) {
	in.Method = strings.ToUpper(strings.TrimSpace(in.Method))
	if in.Method != "INPUT_COST" && in.Method != "OUTPUT_MEASURED" {
		return "", fmt.Errorf("%w: method must be INPUT_COST or OUTPUT_MEASURED", ErrInvalidInput)
	}
	if !s.tenantWide(p) {
		return "", auth.ErrForbidden
	}
	var id string
	err := s.p2pTx(ctx, p, auth.PermContractPolicyManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO revenue_policies (legal_entity_id, version, method, contract_asset_account_id, contract_liability_account_id, created_by)
			SELECT $1::uuid, COALESCE(max(version), 0) + 1, $2, $3::uuid, $4::uuid, $5 FROM revenue_policies WHERE legal_entity_id = $1::uuid RETURNING id::text`,
			in.LegalEntityID, in.Method, in.ContractAssetAcct, in.ContractLiabilityAc, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "revenue.policy.create", "revenue_policy", id, "revenue_policy.proposed", map[string]any{"id": id, "method": in.Method})
	})
	return id, err
}

// DecideRevenuePolicy approves (retiring the one in force) or rejects a
// proposed policy: another person, fresh MFA to approve, attested.
func (s *Service) DecideRevenuePolicy(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermContractPolicyApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermContractPolicyApprove, "revenue_policy", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "revenue policy"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "revenue.policy.decide", "revenue_policy", id, outcome); err != nil {
			return err
		}
		if approve {
			if _, err := tx.Exec(ctx, `UPDATE revenue_policies SET status = 'RETIRED' WHERE status = 'APPROVED'
				AND legal_entity_id = (SELECT legal_entity_id FROM revenue_policies WHERE id = $1::uuid)`, id); err != nil {
				return err
			}
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE revenue_policies SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "revenue.policy.decide", "revenue_policy", id, "revenue_policy."+strings.ToLower(outcome), map[string]any{"id": id, "reason": reason})
	})
}

// RevenuePolicies lists the revenue policies of every entity (tenant-wide readers).
func (s *Service) RevenuePolicies(ctx context.Context, p auth.Principal) ([]map[string]any, error) {
	out := []map[string]any{}
	if !s.tenantWide(p) {
		return out, nil
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id::text, r.legal_entity_id::text, e.name, r.version, r.method, r.status, a.code || ' ' || a.name, l.code || ' ' || l.name,
			r.created_by, COALESCE(r.approved_by, '') FROM revenue_policies r JOIN legal_entities e ON e.id = r.legal_entity_id
			JOIN accounts a ON a.id = r.contract_asset_account_id JOIN accounts l ON l.id = r.contract_liability_account_id ORDER BY e.name, r.version DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, ent, code, method, status, asset, liab, by, appr string
			var v int
			if err := rows.Scan(&id, &ent, &code, &v, &method, &status, &asset, &liab, &by, &appr); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": id, "legal_entity_id": ent, "entity": code, "version": v, "method": method, "status": status,
				"contract_asset_account": asset, "contract_liability_account": liab, "created_by": by, "approved_by": appr})
		}
		return rows.Err()
	})
	return out, err
}

func revenueLines(ctx context.Context, tx pgx.Tx, project string, period time.Time, method string) ([]RevenueLine, error) {
	rows, err := tx.Query(ctx, `SELECT r.contract_id::text, c.number, r.contract_value, r.measure, r.measure_total, r.progress, r.earned, r.billed, r.cumulative, r.previous, r.movement
		FROM revenue_compute($1::uuid, $2, $3) r JOIN contracts c ON c.id = r.contract_id ORDER BY c.number`, project, period, method)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RevenueLine{}
	for rows.Next() {
		var l RevenueLine
		if err := rows.Scan(&l.ContractID, &l.ContractNumber, &l.ContractValue, &l.Measure, &l.MeasureTotal, &l.Progress, &l.Earned, &l.Billed, &l.Cumulative, &l.Previous, &l.Movement); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Service) revenueTx(ctx context.Context, p auth.Principal, perm auth.Permission, project string, fn func(ctx context.Context, tx pgx.Tx, entity, method, ccy string) error) error {
	if err := s.authz.RequireProject(p, perm, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var entity, ccy string
		if err := tx.QueryRow(ctx, `SELECT pr.legal_entity_id::text, e.functional_currency FROM projects pr JOIN legal_entities e ON e.id = pr.legal_entity_id WHERE pr.id = $1::uuid`, project).Scan(&entity, &ccy); err != nil {
			return mapNoRows(err)
		}
		var method string
		if err := tx.QueryRow(ctx, `SELECT method FROM revenue_policies WHERE legal_entity_id = $1::uuid AND status = 'APPROVED'`, entity).Scan(&method); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("%w: the project's legal entity has no approved revenue policy", ErrInvalidInput)
			}
			return err
		}
		return fn(ctx, tx, entity, method, ccy)
	})
}

// PreviewRevenue computes, without saving, what a run would record.
func (s *Service) PreviewRevenue(ctx context.Context, p auth.Principal, project, periodEnd string) (RevenueRun, error) {
	out := RevenueRun{ProjectID: project, PeriodEnd: periodEnd}
	period, err := parseDay("period_end", periodEnd)
	if err != nil {
		return out, err
	}
	// Anyone who reads the ledger for the project may preview; preparing needs revenue.run.
	err = s.revenueTx(ctx, p, auth.PermJournalRead, project, func(ctx context.Context, tx pgx.Tx, entity, method, ccy string) error {
		out.Method, out.Currency = method, ccy
		out.Lines, err = revenueLines(ctx, tx, project, period, method)
		return err
	})
	out.total()
	return out, err
}

// PrepareRevenueRun records the run and generates its DRAFT journal.
func (s *Service) PrepareRevenueRun(ctx context.Context, p auth.Principal, project, periodEnd string) (string, error) {
	period, err := parseDay("period_end", periodEnd)
	if err != nil {
		return "", err
	}
	var id string
	err = s.revenueTx(ctx, p, auth.PermRevenueRun, project, func(ctx context.Context, tx pgx.Tx, entity, method, ccy string) error {
		lines, err := revenueLines(ctx, tx, project, period, method)
		if err != nil {
			return err
		}
		moved := false
		for _, l := range lines {
			if !l.Movement.IsZero() || !l.Cumulative.Equal(l.Previous) {
				moved = true
			}
		}
		if len(lines) == 0 {
			return fmt.Errorf("%w: the project has no active contract", ErrInvalidInput)
		}
		num, err := postgres.NextNumber(ctx, tx, "REV")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO revenue_runs (legal_entity_id, project_id, number, period_end, policy_id, method, currency, created_by)
			SELECT $1::uuid, $2::uuid, $3, $4, id, method, $5, $6 FROM revenue_policies WHERE legal_entity_id = $1::uuid AND status = 'APPROVED' RETURNING id::text`,
			entity, project, num, period, ccy, p.UserID)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO revenue_lines (run_id, contract_id, contract_value, measure, measure_total, progress, earned, billed, cumulative, previous, movement)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, l.ContractID, l.ContractValue, l.Measure, l.MeasureTotal, l.Progress, l.Earned, l.Billed, l.Cumulative, l.Previous, l.Movement); err != nil {
				return err
			}
		}
		if !moved {
			return fmt.Errorf("%w: nothing to adjust: earned and billed revenue already agree with the last posted run", ErrInvalidInput)
		}
		jid, err := s.subledgerJournal(ctx, tx, p, "REV_RUN", id, entity, ccy, "Revenue run "+num+" to "+period.Format("2006-01-02"), period)
		if err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE revenue_runs SET journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "revenue.run", "revenue_run", id, "revenue_run.prepared", map[string]any{"id": id, "project_id": project, "period_end": periodEnd, "journal_id": jid})
	})
	return id, err
}

// CancelRevenueRun cancels a run whose journal is still a draft, with the
// journal: finance (journal.approve or journal.post) declines the draft.
func (s *Service) CancelRevenueRun(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	if !s.authz.Has(p, auth.PermJournalApprove) && !s.authz.Has(p, auth.PermJournalPost) {
		return fmt.Errorf("%w: finance (journal approval or posting) cancels a revenue run", auth.ErrForbidden)
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var jid, project string
		if err := tx.QueryRow(ctx, `SELECT journal_id::text, project_id::text FROM revenue_runs WHERE id = $1::uuid FOR UPDATE`, id).Scan(&jid, &project); err != nil {
			return mapNoRows(err)
		}
		if !s.authz.ScopeOf(p).Allows(project) {
			return ErrNotFound
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE revenue_runs SET status = 'CANCELLED', cancel_reason = $2 WHERE id = $1::uuid AND status = 'PREPARED'`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		if err := s.cancelJournalTx(ctx, tx, p, jid, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "revenue.run.cancel", "revenue_run", id, "revenue_run.cancelled", map[string]any{"id": id, "journal_id": jid, "reason": reason})
	})
}

// RevenueRuns lists a project's runs with their lines and journal status.
func (s *Service) RevenueRuns(ctx context.Context, p auth.Principal, project string) ([]RevenueRun, error) {
	out := []RevenueRun{}
	if !s.authz.Has(p, auth.PermRevenueRun) && !s.authz.Has(p, auth.PermJournalRead) && !s.authz.Has(p, auth.PermP2PRead) {
		return out, auth.ErrForbidden
	}
	if !s.authz.ScopeOf(p).Allows(project) {
		return out, auth.ErrForbidden
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id::text, r.number, r.period_end::text, r.method, r.currency, r.status, COALESCE(r.journal_id::text, ''), COALESCE(j.status, ''), r.created_by, COALESCE(r.cancel_reason, '')
			FROM revenue_runs r LEFT JOIN journals j ON j.id = r.journal_id WHERE r.project_id = $1::uuid ORDER BY r.period_end DESC, r.created_at DESC`, project)
		if err != nil {
			return err
		}
		for rows.Next() {
			r := RevenueRun{ProjectID: project, Lines: []RevenueLine{}}
			if err := rows.Scan(&r.ID, &r.Number, &r.PeriodEnd, &r.Method, &r.Currency, &r.Status, &r.JournalID, &r.JournalStatus, &r.CreatedBy, &r.CancelReason); err != nil {
				rows.Close()
				return err
			}
			out = append(out, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			lr, err := tx.Query(ctx, `SELECT l.contract_id::text, c.number, l.contract_value, l.measure, l.measure_total, l.progress, l.earned, l.billed, l.cumulative, l.previous, l.movement
				FROM revenue_lines l JOIN contracts c ON c.id = l.contract_id WHERE l.run_id = $1::uuid ORDER BY c.number`, out[i].ID)
			if err != nil {
				return err
			}
			for lr.Next() {
				var l RevenueLine
				if err := lr.Scan(&l.ContractID, &l.ContractNumber, &l.ContractValue, &l.Measure, &l.MeasureTotal, &l.Progress, &l.Earned, &l.Billed, &l.Cumulative, &l.Previous, &l.Movement); err != nil {
					lr.Close()
					return err
				}
				out[i].Lines = append(out[i].Lines, l)
			}
			lr.Close()
			out[i].total()
		}
		return nil
	})
	return out, err
}

// RevenueProjects lists the projects the principal may run or read revenue for.
func (s *Service) RevenueProjects(ctx context.Context, p auth.Principal) ([]map[string]string, error) {
	out := []map[string]string{}
	if !s.authz.Has(p, auth.PermRevenueRun) && !s.authz.Has(p, auth.PermJournalApprove) && !s.authz.Has(p, auth.PermJournalPost) &&
		!s.authz.Has(p, auth.PermContractPolicyManage) && !s.authz.Has(p, auth.PermContractPolicyApprove) {
		return out, auth.ErrForbidden
	}
	sc := s.authz.ScopeOf(p)
	projects := sc.Projects
	if projects == nil {
		projects = []string{}
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, code, name FROM projects WHERE $1 OR id = ANY($2::uuid[]) ORDER BY code`, sc.TenantWide, projects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, code, name string
			if err := rows.Scan(&id, &code, &name); err != nil {
				return err
			}
			out = append(out, map[string]string{"id": id, "code": code, "name": name})
		}
		return rows.Err()
	})
	return out, err
}
