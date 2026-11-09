// Workforce and payroll (WP-2.8; SRS HCM-01, HCM-04, HCM-05, HCM-06).
// Migration: 0061_payroll.sql.
//
// A payroll officer keeps employees, prepares effective-dated rule sets and
// pay runs; an HR manager approves rule sets and pay runs (fresh MFA); a
// treasury officer releases the payment (a third person, fresh MFA). The
// database computes every pay line; approval generates one PAY_RUN journal
// per project section and release the PAY_PAY journal and the bank file.
package kernel

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// PayrollPolicyInput proposes an entity's payroll accounts.
type PayrollPolicyInput struct {
	LegalEntityID  string `json:"legal_entity_id"`
	Salary         string `json:"salary_account_id"`
	PensionExpense string `json:"pension_expense_account_id"`
	NetPayable     string `json:"net_payable_account_id"`
	TaxPayable     string `json:"tax_payable_account_id"`
	PensionPayable string `json:"pension_payable_account_id"`
}

// EmployeeInput registers an employee.
type EmployeeInput struct {
	LegalEntityID string          `json:"legal_entity_id"`
	Number        string          `json:"number"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"` // SALARIED | DAILY | HOURLY
	Trade         string          `json:"trade"`
	BaseRate      decimal.Decimal `json:"base_rate"`
	TIN           string          `json:"tin"`
	PensionNo     string          `json:"pension_no"`
	BankName      string          `json:"bank_name"`
	AccountNumber string          `json:"account_number"`
	StartsOn      string          `json:"starts_on"`
}

// TaxBand is one income tax band: tax = gross x rate - deduction.
type TaxBand struct {
	UpTo      *decimal.Decimal `json:"upto"`
	Rate      decimal.Decimal  `json:"rate"`
	Deduction decimal.Decimal  `json:"deduction"`
}

// RuleSetInput prepares a payroll rule set.
type RuleSetInput struct {
	LegalEntityID      string          `json:"legal_entity_id"`
	EffectiveFrom      string          `json:"effective_from"`
	Bands              []TaxBand       `json:"tax_bands"`
	PensionEE          decimal.Decimal `json:"pension_ee_rate"`
	PensionER          decimal.Decimal `json:"pension_er_rate"`
	OvertimeMultiplier decimal.Decimal `json:"overtime_multiplier"`
	HoursPerMonth      decimal.Decimal `json:"hours_per_month"`
	HoursPerDay        decimal.Decimal `json:"hours_per_day"`
	SourceRef          string          `json:"source_ref"`
}

// PayInput is one employee's input to a run.
type PayInput struct {
	EmployeeID    string          `json:"employee_id"`
	Units         decimal.Decimal `json:"units"`
	OvertimeHours decimal.Decimal `json:"overtime_hours"`
	Allowances    decimal.Decimal `json:"allowances"`
	ProjectID     string          `json:"project_id,omitempty"`
	CostCodeID    string          `json:"cost_code_id,omitempty"`
}

func (s *Service) payrollTx(ctx context.Context, p auth.Principal, perm auth.Permission, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !s.tenantWide(p) {
		return auth.ErrForbidden
	}
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, fn)
}

// ProposePayrollPolicy proposes the next version of an entity's payroll accounts.
func (s *Service) ProposePayrollPolicy(ctx context.Context, p auth.Principal, in PayrollPolicyInput) (string, error) {
	var id string
	err := s.payrollTx(ctx, p, auth.PermContractPolicyManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO payroll_policies (legal_entity_id, version, salary_account_id, pension_expense_account_id, net_payable_account_id, tax_payable_account_id, pension_payable_account_id, created_by)
			SELECT $1::uuid, COALESCE(max(version), 0) + 1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7 FROM payroll_policies WHERE legal_entity_id = $1::uuid RETURNING id::text`,
			in.LegalEntityID, in.Salary, in.PensionExpense, in.NetPayable, in.TaxPayable, in.PensionPayable, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "paypolicy.create", "payroll_policy", id, "payroll_policy.proposed", map[string]any{"id": id})
	})
	return id, err
}

// DecidePayrollPolicy approves (retiring the one in force) or rejects.
func (s *Service) DecidePayrollPolicy(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermContractPolicyApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermContractPolicyApprove, "payroll_policy", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "payroll policy"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "paypolicy.decide", "payroll_policy", id, outcome); err != nil {
			return err
		}
		if approve {
			if _, err := tx.Exec(ctx, `UPDATE payroll_policies SET status = 'RETIRED' WHERE status = 'APPROVED' AND legal_entity_id = (SELECT legal_entity_id FROM payroll_policies WHERE id = $1::uuid)`, id); err != nil {
				return err
			}
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE payroll_policies SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "paypolicy.decide", "payroll_policy", id, "payroll_policy."+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// RegisterEmployee records an employee (the bank number is sealed).
func (s *Service) RegisterEmployee(ctx context.Context, p auth.Principal, in EmployeeInput) (string, error) {
	start, err := parseDay("starts_on", in.StartsOn)
	if err != nil {
		return "", err
	}
	in.Kind = strings.ToUpper(strings.TrimSpace(in.Kind))
	var id string
	err = s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		var enc []byte
		var digest, last4 *string
		id = ""
		if n := strings.TrimSpace(in.AccountNumber); n != "" {
			num, err := normalizeAccountNumber(n)
			if err != nil {
				return err
			}
			sealed, err := s.sealAccountNumber(ctx, tx, p.TenantID, "employees", num)
			if err != nil {
				return err
			}
			id, enc, digest, last4 = sealed.id, sealed.enc, &sealed.digest, &sealed.last4
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO employees (id, legal_entity_id, number, name, kind, trade, base_rate, tin, pension_no, bank_name, account_number_enc, account_digest, account_last4, starts_on, created_by)
			VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2::uuid, $3, $4, $5, NULLIF($6, ''), $7, NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), $11, $12, $13, $14, $15) RETURNING id::text`,
			id, in.LegalEntityID, strings.ToUpper(strings.TrimSpace(in.Number)), strings.TrimSpace(in.Name), in.Kind, strings.TrimSpace(in.Trade), in.BaseRate, strings.TrimSpace(in.TIN),
			strings.TrimSpace(in.PensionNo), strings.TrimSpace(in.BankName), enc, digest, last4, start, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "employee.create", "employee", id, "employee.registered", map[string]any{"id": id, "number": in.Number})
	})
	return id, err
}

// EmployeeLeaves records an employee's leaving date.
func (s *Service) EmployeeLeaves(ctx context.Context, p auth.Principal, id, endsOn string) error {
	end, err := parseDay("ends_on", endsOn)
	if err != nil {
		return err
	}
	return s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE employees SET status = 'LEFT', ends_on = $2 WHERE id = $1::uuid AND status = 'ACTIVE'`, id, end); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "employee.leave", "employee", id, "employee.left", map[string]any{"id": id, "ends_on": endsOn})
	})
}

// CreateRuleSet prepares a DRAFT rule set.
func (s *Service) CreateRuleSet(ctx context.Context, p auth.Principal, in RuleSetInput) (string, error) {
	from, err := parseDay("effective_from", in.EffectiveFrom)
	if err != nil {
		return "", err
	}
	// The database validates numbers, not the strings decimal marshals to.
	raw := make([]map[string]any, len(in.Bands))
	for i, b := range in.Bands {
		raw[i] = map[string]any{"rate": json.Number(b.Rate.String()), "deduction": json.Number(b.Deduction.String()), "upto": nil}
		if b.UpTo != nil {
			raw[i]["upto"] = json.Number(b.UpTo.String())
		}
	}
	bands, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	if in.OvertimeMultiplier.IsZero() {
		in.OvertimeMultiplier = decimal.RequireFromString("1.5")
	}
	if in.HoursPerMonth.IsZero() {
		in.HoursPerMonth = decimal.NewFromInt(208)
	}
	if in.HoursPerDay.IsZero() {
		in.HoursPerDay = decimal.NewFromInt(8)
	}
	var id string
	err = s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "PRS")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO payroll_rule_sets (legal_entity_id, number, effective_from, tax_bands, pension_ee_rate, pension_er_rate, overtime_multiplier, hours_per_month, hours_per_day, source_ref, created_by)
			VALUES ($1::uuid, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`,
			in.LegalEntityID, num, from, string(bands), in.PensionEE, in.PensionER, in.OvertimeMultiplier, in.HoursPerMonth, in.HoursPerDay, strings.TrimSpace(in.SourceRef), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "payrules.create", "payroll_rule_set", id, "payroll_rule_set.prepared", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// SubmitRuleSet / DecideRuleSet: maker/checker.
func (s *Service) SubmitRuleSet(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermPayrollPrepare, "payroll_rule_set", "payroll_rule_sets", id, "payrules.submit", "payroll_rule_set.submitted")
}

// DecideRuleSet approves or rejects a rule set (payroll.approve, fresh MFA to approve).
func (s *Service) DecideRuleSet(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermPayrollApprove, "payroll_rule_set", "payroll_rule_sets", id, "payrules.decide", "payroll rule set", approve, reason, nil)
}

// CreatePayRun opens a DRAFT pay run for a period.
func (s *Service) CreatePayRun(ctx context.Context, p auth.Principal, entity, start, end, payDate string) (string, error) {
	ps, err := parseDay("period_start", start)
	if err != nil {
		return "", err
	}
	pe, err := parseDay("period_end", end)
	if err != nil {
		return "", err
	}
	pd, err := parseDay("pay_date", payDate)
	if err != nil {
		return "", err
	}
	var id string
	err = s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "PAY")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO pay_runs (legal_entity_id, number, period_start, period_end, pay_date, created_by) VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`,
			entity, num, ps, pe, pd, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "payrun.create", "pay_run", id, "pay_run.created", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// AddPayInputs records inputs on a draft run (one per employee).
func (s *Service) AddPayInputs(ctx context.Context, p auth.Principal, run string, inputs []PayInput) error {
	return s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		for _, in := range inputs {
			if _, err := tx.Exec(ctx, `INSERT INTO pay_inputs (run_id, employee_id, units, overtime_hours, allowances, project_id, cost_code_id)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5, NULLIF($6, '')::uuid, NULLIF($7, '')::uuid)`, run, in.EmployeeID, in.Units, in.OvertimeHours, in.Allowances, in.ProjectID, in.CostCodeID); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "payrun.inputs", "pay_run", run, "pay_run.inputs_added", map[string]any{"id": run, "count": len(inputs)})
	})
}

// CalculatePayRun has the database compute every line, then marks the run CALCULATED.
func (s *Service) CalculatePayRun(ctx context.Context, p auth.Principal, run string) error {
	return s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO pay_lines (run_id, employee_id, project_id, cost_code_id, basic, overtime, allowances, gross, income_tax, pension_ee, pension_er, net)
			SELECT $1::uuid, employee_id, project_id, cost_code_id, basic, overtime, allowances, gross, income_tax, pension_ee, pension_er, net FROM payroll_compute($1::uuid)`, run); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE pay_runs SET status = 'CALCULATED' WHERE id = $1::uuid AND status = 'DRAFT'`, run); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "payrun.calculate", "pay_run", run, "pay_run.calculated", map[string]any{"id": run})
	})
}

// SubmitPayRun submits a calculated run (its preparer).
func (s *Service) SubmitPayRun(ctx context.Context, p auth.Principal, id string) error {
	return s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermPayrollPrepare, "pay_run", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "CALCULATED"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the preparer submits a pay run", auth.ErrForbidden)
		}
		if err := s.authorize(ctx, tx, p, "payrun.submit", "pay_run", id, ""); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE pay_runs SET status = 'SUBMITTED' WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "payrun.submit", "pay_run", id, "pay_run.submitted", map[string]any{"id": id})
	})
}

// DecidePayRun approves (generating one PAY_RUN journal per project section)
// or rejects a submitted run: payroll.approve, not the preparer, fresh MFA.
func (s *Service) DecidePayRun(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.payrollTx(ctx, p, auth.PermPayrollApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermPayrollApprove, "pay_run", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "pay run"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "payrun.decide", "pay_run", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE pay_runs SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		if approve {
			var entity, ccy string
			var end time.Time
			if err := tx.QueryRow(ctx, `SELECT r.legal_entity_id::text, e.functional_currency, r.period_end FROM pay_runs r JOIN legal_entities e ON e.id = r.legal_entity_id WHERE r.id = $1::uuid`, id).Scan(&entity, &ccy, &end); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT DISTINCT COALESCE(project_id::text, '') FROM pay_lines WHERE run_id = $1::uuid`, id)
			if err != nil {
				return err
			}
			var projects []string
			for rows.Next() {
				var pr string
				if err := rows.Scan(&pr); err != nil {
					rows.Close()
					return err
				}
				projects = append(projects, pr)
			}
			rows.Close()
			for _, pr := range projects {
				sec, err := postgres.InsertReturningID(ctx, tx, `INSERT INTO pay_run_sections (run_id, project_id) VALUES ($1::uuid, NULLIF($2, '')::uuid) RETURNING id::text`, id, pr)
				if err != nil {
					return err
				}
				jid, err := s.subledgerJournal(ctx, tx, p, "PAY_RUN", sec, entity, ccy, "Payroll "+d.Number, end)
				if err != nil {
					return err
				}
				if err := postgres.Exec1(ctx, tx, `UPDATE pay_run_sections SET journal_id = $2::uuid WHERE id = $1::uuid`, sec, jid); err != nil {
					return err
				}
			}
		}
		return s.record(ctx, tx, p, "payrun.decide", "pay_run", id, "pay_run."+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// ReleasePayRun pays an approved run from a company bank account (a third
// person: payment.release, fresh MFA) and generates the PAY_PAY journal.
func (s *Service) ReleasePayRun(ctx context.Context, p auth.Principal, id, companyBank string) (string, error) {
	if err := s.authz.RequireStepUp(p); err != nil {
		return "", err
	}
	var jid string
	err := s.payrollTx(ctx, p, auth.PermPaymentRelease, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermPaymentRelease, "pay_run", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "APPROVED"); err != nil {
			return err
		}
		var approver, entity, ccy string
		var payDate time.Time
		if err := tx.QueryRow(ctx, `SELECT r.approved_by, r.legal_entity_id::text, e.functional_currency, r.pay_date FROM pay_runs r JOIN legal_entities e ON e.id = r.legal_entity_id WHERE r.id = $1::uuid`, id).Scan(&approver, &entity, &ccy, &payDate); err != nil {
			return err
		}
		if p.UserID == d.CreatedBy || p.UserID == approver {
			return fmt.Errorf("%w: the person who pays must differ from the preparer and the approver", auth.ErrSoD)
		}
		if err := s.authorize(ctx, tx, p, "payrun.release", "pay_run", id, "PAID"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE pay_runs SET status = 'PAID', paid_by = $2, company_bank_id = $3::uuid WHERE id = $1::uuid`, id, p.UserID, companyBank); err != nil {
			return err
		}
		if jid, err = s.subledgerJournal(ctx, tx, p, "PAY_PAY", id, entity, ccy, "Payroll payment "+d.Number, payDate); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE pay_runs SET pay_journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "payrun.release", "pay_run", id, "pay_run.paid", map[string]any{"id": id, "journal_id": jid})
	})
	return jid, err
}

// CancelPayRun cancels a draft or calculated run with a reason (its preparer).
func (s *Service) CancelPayRun(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE pay_runs SET status = 'CANCELLED', decision_reason = $2 WHERE id = $1::uuid AND status IN ('DRAFT', 'CALCULATED')`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "payrun.cancel", "pay_run", id, "pay_run.cancelled", map[string]any{"id": id, "reason": reason})
	})
}

// PayrollView lists employees (masked), rule sets and runs; with run, its lines.
func (s *Service) PayrollView(ctx context.Context, p auth.Principal, run string) (map[string]any, error) {
	out := map[string]any{}
	pay := s.authz.Has(p, auth.PermPayrollPrepare) || s.authz.Has(p, auth.PermPayrollApprove) || s.authz.Has(p, auth.PermPaymentRelease)
	// The controller and CFO see the accounts policy only, never pay data.
	pol := s.authz.Has(p, auth.PermContractPolicyManage) || s.authz.Has(p, auth.PermContractPolicyApprove)
	if (!pay && !pol) || !s.tenantWide(p) {
		return out, auth.ErrForbidden
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := func(key, sql string, args ...any) error {
			rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (`+sql+`) r`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			list := []map[string]any{}
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					return err
				}
				m, err := jsonMap(raw)
				if err != nil {
					return err
				}
				list = append(list, m)
			}
			out[key] = list
			return rows.Err()
		}
		if err := q("policies", `SELECT p.id, p.version, p.status, p.created_by, p.approved_by,
			(SELECT code || ' ' || name FROM accounts WHERE id = p.salary_account_id) AS salary, (SELECT code || ' ' || name FROM accounts WHERE id = p.pension_expense_account_id) AS pension_expense,
			(SELECT code || ' ' || name FROM accounts WHERE id = p.net_payable_account_id) AS net_payable, (SELECT code || ' ' || name FROM accounts WHERE id = p.tax_payable_account_id) AS tax_payable,
			(SELECT code || ' ' || name FROM accounts WHERE id = p.pension_payable_account_id) AS pension_payable FROM payroll_policies p ORDER BY p.version DESC`); err != nil {
			return err
		}
		if err := q("entities", `SELECT id, name, functional_currency FROM legal_entities ORDER BY name`); err != nil {
			return err
		}
		if !pay {
			return nil
		}
		if err := q("projects", `SELECT id, code, name FROM projects ORDER BY code`); err != nil {
			return err
		}
		if err := q("employees", `SELECT id, number, name, kind, trade, base_rate, tin, pension_no, bank_name, CASE WHEN account_last4 IS NULL THEN NULL ELSE '****' || account_last4 END AS account, starts_on, ends_on, status FROM employees ORDER BY number`); err != nil {
			return err
		}
		if err := q("rule_sets", `SELECT id, number, effective_from, tax_bands, pension_ee_rate, pension_er_rate, overtime_multiplier, source_ref, status, created_by, approved_by FROM payroll_rule_sets ORDER BY effective_from DESC`); err != nil {
			return err
		}
		if err := q("runs", `SELECT r.id, r.number, r.period_start, r.period_end, r.pay_date, r.status, r.created_by, r.approved_by, r.paid_by,
			(SELECT count(*) FROM pay_inputs i WHERE i.run_id = r.id) AS employees, (SELECT sum(gross) FROM pay_lines l WHERE l.run_id = r.id) AS gross,
			(SELECT sum(net) FROM pay_lines l WHERE l.run_id = r.id) AS net FROM pay_runs r ORDER BY r.period_end DESC, r.created_at DESC`); err != nil {
			return err
		}
		if run != "" {
			return q("lines", `SELECT e.number, e.name, e.kind, (SELECT code FROM projects WHERE id = COALESCE(l.project_id, i.project_id)) AS project, i.units, i.overtime_hours,
				l.basic, l.overtime, l.allowances, l.gross, l.income_tax, l.pension_ee, l.pension_er, l.net
				FROM pay_inputs i JOIN employees e ON e.id = i.employee_id LEFT JOIN pay_lines l ON l.run_id = i.run_id AND l.employee_id = i.employee_id
				WHERE i.run_id = $1::uuid ORDER BY e.number`, run)
		}
		return nil
	})
	return out, err
}

// PayrollBankFile returns the bank file (CSV) of a paid run: the decrypted
// account numbers leave the system only here, for treasury, audited.
func (s *Service) PayrollBankFile(ctx context.Context, p auth.Principal, id string) ([]byte, error) {
	var buf bytes.Buffer
	err := s.payrollTx(ctx, p, auth.PermPaymentRelease, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM pay_runs WHERE id = $1::uuid`, id).Scan(&status); err != nil {
			return mapNoRows(err)
		}
		if status != "PAID" && status != "APPROVED" {
			return fmt.Errorf("%w: the bank file is produced for an approved or paid run (this one is %s)", ErrInvalidInput, status)
		}
		rows, err := tx.Query(ctx, `SELECT e.id::text, e.number, e.name, COALESCE(e.bank_name, ''), e.account_number_enc, l.net FROM pay_lines l JOIN employees e ON e.id = l.employee_id
			WHERE l.run_id = $1::uuid AND l.net > 0 ORDER BY e.number`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"employee_number", "name", "bank", "account_number", "amount"})
		n := 0
		for rows.Next() {
			var eid, num, name, bank string
			var enc []byte
			var net decimal.Decimal
			if err := rows.Scan(&eid, &num, &name, &bank, &enc, &net); err != nil {
				return err
			}
			acct := ""
			if len(enc) > 0 {
				if acct, err = s.openAccountNumber(p.TenantID, "employees", eid, enc, nil); err != nil {
					return err
				}
			}
			_ = w.Write([]string{num, name, bank, acct, net.StringFixed(2)})
			n++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		w.Flush()
		return s.record(ctx, tx, p, "payrun.bankfile", "pay_run", id, "pay_run.bank_file_exported", map[string]any{"id": id, "rows": n})
	})
	return buf.Bytes(), err
}
