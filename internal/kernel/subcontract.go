// Subcontracts (WP-2.5 part 1; SRS SUB-01..05, PCC-02). Migration:
// 0055_subcontracts.sql.
//
// A subcontract (subcontractor, project, schedule of values, retention,
// advance, withholding) is prepared by a quantity surveyor and approved by a
// commercial manager; approval commits its value against the budget. Each
// period the quantity surveyor enters the subcontractor's cumulative claim and
// the commercial manager certifies cumulative quantities; the database
// computes the certificate and the kernel generates its SUB_CERT journal for
// finance. Back-charges are approved by a second person and deducted once.
package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// SubcontractPolicyInput proposes an entity's subcontract accounts.
type SubcontractPolicyInput struct {
	LegalEntityID string `json:"legal_entity_id"`
	Payable       string `json:"payable_account_id"`
	Retention     string `json:"retention_account_id"`
	Advance       string `json:"advance_account_id"`
	WHT           string `json:"wht_account_id"`
	Backcharge    string `json:"backcharge_account_id"`
}

// SubcontractLine is one schedule-of-values line.
type SubcontractLine struct {
	LineNo      int             `json:"line_no"`
	Description string          `json:"description"`
	Unit        string          `json:"unit"`
	Quantity    decimal.Decimal `json:"quantity"`
	Rate        decimal.Decimal `json:"rate"`
	CostCodeID  string          `json:"cost_code_id"`
	AccountID   string          `json:"account_id"`
}

// SubcontractInput prepares a subcontract.
type SubcontractInput struct {
	ProjectID          string            `json:"project_id"`
	SupplierID         string            `json:"supplier_id"`
	Title              string            `json:"title"`
	RetentionPct       decimal.Decimal   `json:"retention_pct"`
	RetentionCap       decimal.Decimal   `json:"retention_cap"`
	AdvanceAmount      decimal.Decimal   `json:"advance_amount"`
	AdvanceRecoveryPct decimal.Decimal   `json:"advance_recovery_pct"`
	WHTRate            decimal.Decimal   `json:"wht_rate"`
	PayWhenCertified   bool              `json:"pay_when_certified"`
	Lines              []SubcontractLine `json:"lines"`
}

// ApplicationLine is a cumulative quantity claimed (or certified) on a line.
type ApplicationLine struct {
	LineNo int             `json:"line_no"`
	CumQty decimal.Decimal `json:"cumulative_qty"`
}

// ProposeSubcontractPolicy proposes the next version of an entity's subcontract accounts.
func (s *Service) ProposeSubcontractPolicy(ctx context.Context, p auth.Principal, in SubcontractPolicyInput) (string, error) {
	if !s.tenantWide(p) {
		return "", auth.ErrForbidden
	}
	var id string
	err := s.p2pTx(ctx, p, auth.PermContractPolicyManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO subcontract_policies (legal_entity_id, version, payable_account_id, retention_account_id, advance_account_id, wht_account_id, backcharge_account_id, created_by)
			SELECT $1::uuid, COALESCE(max(version), 0) + 1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7 FROM subcontract_policies WHERE legal_entity_id = $1::uuid RETURNING id::text`,
			in.LegalEntityID, in.Payable, in.Retention, in.Advance, in.WHT, in.Backcharge, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "subpolicy.create", "subcontract_policy", id, "subcontract_policy.proposed", map[string]any{"id": id})
	})
	return id, err
}

// DecideSubcontractPolicy approves (retiring the one in force) or rejects a policy.
func (s *Service) DecideSubcontractPolicy(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermContractPolicyApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermContractPolicyApprove, "subcontract_policy", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "subcontract policy"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "subpolicy.decide", "subcontract_policy", id, outcome); err != nil {
			return err
		}
		if approve {
			if _, err := tx.Exec(ctx, `UPDATE subcontract_policies SET status = 'RETIRED' WHERE status = 'APPROVED'
				AND legal_entity_id = (SELECT legal_entity_id FROM subcontract_policies WHERE id = $1::uuid)`, id); err != nil {
				return err
			}
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE subcontract_policies SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "subpolicy.decide", "subcontract_policy", id, "subcontract_policy."+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// CreateSubcontract prepares a DRAFT subcontract with its schedule of values.
func (s *Service) CreateSubcontract(ctx context.Context, p auth.Principal, in SubcontractInput) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermSubcontractManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	if len(in.Lines) == 0 {
		return "", fmt.Errorf("%w: a subcontract needs a schedule of values", ErrInvalidInput)
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "SUB")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO subcontracts (legal_entity_id, project_id, supplier_id, number, title, currency, retention_pct, retention_cap,
				advance_amount, advance_recovery_pct, wht_rate, pay_when_certified, created_by)
			SELECT pr.legal_entity_id, pr.id, $2::uuid, $3, $4, e.functional_currency, $5, $6, $7, $8, $9, $10, $11
			  FROM projects pr JOIN legal_entities e ON e.id = pr.legal_entity_id WHERE pr.id = $1::uuid RETURNING id::text`,
			in.ProjectID, in.SupplierID, num, strings.TrimSpace(in.Title), in.RetentionPct, in.RetentionCap, in.AdvanceAmount, in.AdvanceRecoveryPct, in.WHTRate, in.PayWhenCertified, p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		for _, l := range in.Lines {
			if _, err := tx.Exec(ctx, `INSERT INTO subcontract_lines (subcontract_id, line_no, description, unit, quantity, rate, amount, cost_code_id, account_id)
				VALUES ($1::uuid, $2, $3, $4, $5, $6, 0, $7::uuid, $8::uuid)`, id, l.LineNo, strings.TrimSpace(l.Description), strings.TrimSpace(l.Unit), l.Quantity, l.Rate, l.CostCodeID, l.AccountID); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "subcontract.create", "subcontract", id, "subcontract.created", map[string]any{"id": id, "number": num, "project_id": in.ProjectID})
	})
	return id, err
}

// SubmitSubcontract submits a draft (its author).
func (s *Service) SubmitSubcontract(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermSubcontractManage, "subcontract", "subcontracts", id, "subcontract.submit", "subcontract.submitted")
}

// DecideSubcontract approves (committing it against the budget) or rejects.
func (s *Service) DecideSubcontract(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermSubcontractApprove, "subcontract", "subcontracts", id, "subcontract.decide", "subcontract", approve, reason, nil)
}

// CreateBackcharge raises a DRAFT back-charge on an approved subcontract.
func (s *Service) CreateBackcharge(ctx context.Context, p auth.Principal, subcontractID, reason, evidence string, amount decimal.Decimal) (string, error) {
	var id string
	err := s.subTx(ctx, p, auth.PermSubcontractManage, subcontractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "BC")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO subcontract_backcharges (subcontract_id, number, reason, evidence_ref, amount, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`, subcontractID, num, strings.TrimSpace(reason), strings.TrimSpace(evidence), amount, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "backcharge.create", "subcontract_backcharge", id, "backcharge.created", map[string]any{"id": id, "amount": amount.String()})
	})
	return id, err
}

// SubmitBackcharge / DecideBackcharge: maker/checker.
func (s *Service) SubmitBackcharge(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermSubcontractManage, "subcontract_backcharge", "subcontract_backcharges", id, "backcharge.submit", "backcharge.submitted")
}

// DecideBackcharge approves or rejects a back-charge (not its author; fresh MFA to approve).
func (s *Service) DecideBackcharge(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermSubcontractApprove, "subcontract_backcharge", "subcontract_backcharges", id, "backcharge.decide", "back-charge", approve, reason, nil)
}

func (s *Service) subTx(ctx context.Context, p auth.Principal, perm auth.Permission, subcontractID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM subcontracts WHERE id = $1::uuid`, subcontractID).Scan(&project); err != nil {
			return mapNoRows(err)
		}
		if err := s.authz.RequireProject(p, perm, p.TenantID, project); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// CreateApplication enters a subcontractor's cumulative claim (DRAFT).
func (s *Service) CreateApplication(ctx context.Context, p auth.Principal, subcontractID, periodTo string, lines []ApplicationLine) (string, error) {
	to, err := parseDay("period_to", periodTo)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%w: an application needs at least one line", ErrInvalidInput)
	}
	var id string
	err = s.subTx(ctx, p, auth.PermSubcontractApply, subcontractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "SPA")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO subcontract_applications (subcontract_id, number, period_to, created_by) VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
			subcontractID, num, to, p.UserID)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO subcontract_application_lines (application_id, line_no, claimed_cum_qty) VALUES ($1::uuid, $2, $3)`, id, l.LineNo, l.CumQty); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "subapp.create", "subcontract_application", id, "subcontract_application.created", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// SubmitApplication submits an application for certification (its author).
func (s *Service) SubmitApplication(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermSubcontractApply, "subcontract_application", "subcontract_applications", id, "subapp.submit", "subcontract_application.submitted")
}

// CertifyApplication records the certified cumulative quantities and
// certifies: another person, fresh MFA, attested. The database computes the
// certificate; its SUB_CERT journal is generated for finance.
func (s *Service) CertifyApplication(ctx context.Context, p auth.Principal, id string, certified []ApplicationLine) (string, error) {
	if err := s.authz.RequireStepUp(p); err != nil {
		return "", err
	}
	var jid string
	err := s.p2pTx(ctx, p, auth.PermSubcontractCertify, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermSubcontractCertify, "subcontract_application", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "subcontractor application"); err != nil {
			return err
		}
		for _, l := range certified {
			tag, err := tx.Exec(ctx, `UPDATE subcontract_application_lines SET certified_cum_qty = $3 WHERE application_id = $1::uuid AND line_no = $2`, id, l.LineNo, l.CumQty)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("%w: line %d was not claimed in this application", ErrInvalidInput, l.LineNo)
			}
		}
		if err := s.authorize(ctx, tx, p, "subapp.certify", "subcontract_application", id, "CERTIFIED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE subcontract_applications SET status = 'CERTIFIED', certified_by = $2 WHERE id = $1::uuid`, id, p.UserID); err != nil {
			return err
		}
		var entity, ccy string
		var to time.Time
		if err := tx.QueryRow(ctx, `SELECT s.legal_entity_id::text, s.currency, a.period_to FROM subcontract_applications a JOIN subcontracts s ON s.id = a.subcontract_id WHERE a.id = $1::uuid`, id).Scan(&entity, &ccy, &to); err != nil {
			return err
		}
		var hasPolicy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subcontract_policies WHERE legal_entity_id = $1::uuid AND status = 'APPROVED')`, entity).Scan(&hasPolicy); err != nil {
			return err
		}
		if !hasPolicy {
			return fmt.Errorf("%w: the entity has no approved subcontract policy (accounts)", ErrInvalidInput)
		}
		jid, err = s.subledgerJournal(ctx, tx, p, "SUB_CERT", id, entity, ccy, "Subcontract certificate "+d.Number, to)
		if err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE subcontract_applications SET journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "subapp.certify", "subcontract_application", id, "subcontract_application.certified", map[string]any{"id": id, "journal_id": jid})
	})
	return jid, err
}

// RejectApplication rejects a submitted application with a reason.
func (s *Service) RejectApplication(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermSubcontractCertify, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermSubcontractCertify, "subcontract_application", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "subcontractor application"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "subapp.certify", "subcontract_application", id, "REJECTED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE subcontract_applications SET status = 'REJECTED', certified_by = $2, decision_reason = $3 WHERE id = $1::uuid`, id, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "subapp.certify", "subcontract_application", id, "subcontract_application.rejected", map[string]any{"id": id, "reason": reason})
	})
}

// SubcontractView is a subcontract with its lines, certificates and position.
type SubcontractView struct {
	Subcontract  map[string]any   `json:"subcontract"`
	Lines        []map[string]any `json:"lines"`
	Applications []map[string]any `json:"applications"`
	Backcharges  []map[string]any `json:"backcharges"`
	Position     map[string]any   `json:"position"`
}

// GetSubcontract returns a subcontract's schedule of values with previous,
// this-period and to-date quantities, its certificates and back-charges.
func (s *Service) GetSubcontract(ctx context.Context, p auth.Principal, id string) (SubcontractView, error) {
	out := SubcontractView{Lines: []map[string]any{}, Applications: []map[string]any{}, Backcharges: []map[string]any{}}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "subcontract", id, false); err != nil {
			return err
		}
		q := func(dst *[]map[string]any, sql string) error {
			rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (`+sql+`) r`, id)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					return err
				}
				m, err := jsonMap(raw)
				if err != nil {
					return err
				}
				*dst = append(*dst, m)
			}
			return rows.Err()
		}
		var head []map[string]any
		if err := q(&head, `SELECT s.id, s.number, s.title, s.status, s.project_id, s.supplier_id, (SELECT name FROM suppliers WHERE id = s.supplier_id) AS supplier, (SELECT code FROM suppliers WHERE id = s.supplier_id) AS supplier_code, s.currency,
			s.retention_pct, s.retention_cap, s.advance_amount, s.advance_recovery_pct, s.wht_rate, s.pay_when_certified, s.created_by, s.approved_by, subcontract_value(s.id) AS value
			FROM subcontracts s WHERE s.id = $1::uuid`); err != nil {
			return err
		}
		if len(head) == 0 {
			return ErrNotFound
		}
		out.Subcontract = head[0]
		if err := q(&out.Lines, `SELECT l.line_no, l.description, l.unit, l.quantity, l.rate, l.amount, c.code AS cost_code,
			subcontract_prev_qty(l.subcontract_id, l.line_no, NULL) AS certified_qty, subcontract_certified_value(l.subcontract_id, l.line_no) AS certified_value
			FROM subcontract_lines l JOIN cost_codes c ON c.id = l.cost_code_id WHERE l.subcontract_id = $1::uuid ORDER BY l.line_no`); err != nil {
			return err
		}
		if err := q(&out.Applications, `SELECT a.id, a.number, a.period_to, a.status, a.created_by, a.certified_by, a.this_gross, a.retention, a.advance_recovery, a.backcharges, a.wht,
			a.net_payable, a.gross_cumulative, a.journal_id, (SELECT status FROM journals WHERE id = a.journal_id) AS journal_status,
			CASE WHEN a.status = 'CERTIFIED' THEN subcontract_certificate_open(a.id, NULL) END AS open_amount,
			(SELECT json_agg(json_build_object('line_no', l.line_no, 'claimed', l.claimed_cum_qty, 'certified', l.certified_cum_qty) ORDER BY l.line_no) FROM subcontract_application_lines l WHERE l.application_id = a.id) AS lines
			FROM subcontract_applications a WHERE a.subcontract_id = $1::uuid ORDER BY a.period_to DESC, a.created_at DESC`); err != nil {
			return err
		}
		if err := q(&out.Backcharges, `SELECT b.id, b.number, b.reason, b.evidence_ref, b.amount, b.status, b.created_by, b.deducted_in FROM subcontract_backcharges b WHERE b.subcontract_id = $1::uuid ORDER BY b.created_at`); err != nil {
			return err
		}
		var pos []map[string]any
		if err := q(&pos, `SELECT subcontract_value(s.id) AS value,
			COALESCE((SELECT sum(this_gross) FROM subcontract_applications WHERE subcontract_id = s.id AND status = 'CERTIFIED'), 0) AS certified,
			COALESCE((SELECT sum(retention) FROM subcontract_applications WHERE subcontract_id = s.id AND status = 'CERTIFIED'), 0) AS retention_held,
			s.advance_amount - COALESCE((SELECT sum(advance_recovery) FROM subcontract_applications WHERE subcontract_id = s.id AND status = 'CERTIFIED'), 0) AS advance_outstanding,
			COALESCE((SELECT sum(net_payable) FROM subcontract_applications WHERE subcontract_id = s.id AND status = 'CERTIFIED'), 0) AS net_certified
			FROM subcontracts s WHERE s.id = $1::uuid`); err != nil {
			return err
		}
		if len(pos) > 0 {
			out.Position = pos[0]
		}
		return nil
	})
	return out, err
}

// jsonMap decodes one JSON object row (numbers stay exact as json.Number).
func jsonMap(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	return m, dec.Decode(&m)
}
