// Guarantees and bonds; price indices, escalation formulas and price
// adjustment (WP-2.7; SRS CON-05, CON-07, TRS-04). Migration:
// 0050_bonds_escalation.sql.
//
// A guarantee (bid, advance, performance, retention) is registered by
// treasury against a contract; its value may only be reduced and its expiry
// only extended, each with a reason. Releasing it to the bank or recording
// its call is a decision by a different person with a fresh multi-factor
// sign-in. The register shows what expires soon and whether the advance
// still owed to the client is covered.
//
// Price adjustment follows the contract's formula Pn = a + Σ b·(Ln/Lo):
// index values are proposed and approved by two people; the formula is
// approved once per contract; for each certified payment certificate the
// database computes the factor and the adjustment (work certified in the
// period × (Pn − 1)) from approved values only, and recomputes it at
// approval.
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

// GuaranteeInput registers a guarantee.
type GuaranteeInput struct {
	ContractID string          `json:"contract_id"`
	Kind       string          `json:"kind"` // BID | ADVANCE | PERFORMANCE | RETENTION
	Issuer     string          `json:"issuer"`
	Reference  string          `json:"reference"`
	Currency   string          `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
	IssuedOn   string          `json:"issued_on"`
	ExpiresOn  string          `json:"expires_on"`
}

// GuaranteeChange reduces the value or extends the expiry (or both).
type GuaranteeChange struct {
	Amount    *decimal.Decimal `json:"amount,omitempty"`
	ExpiresOn string           `json:"expires_on,omitempty"`
	Reason    string           `json:"reason"`
}

// GuaranteeClose releases a guarantee to the bank or records its call.
type GuaranteeClose struct {
	Outcome      string           `json:"outcome"` // RELEASED | CALLED
	Reference    string           `json:"reference"`
	Reason       string           `json:"reason"`
	CalledAmount *decimal.Decimal `json:"called_amount,omitempty"`
}

// Guarantee is a guarantee with its standing.
type Guarantee struct {
	ID             string           `json:"id"`
	Number         string           `json:"number"`
	ContractID     string           `json:"contract_id"`
	ContractNumber string           `json:"contract_number"`
	ProjectID      string           `json:"project_id"`
	Kind           string           `json:"kind"`
	Issuer         string           `json:"issuer"`
	Reference      string           `json:"reference"`
	Currency       string           `json:"currency"`
	OriginalAmount decimal.Decimal  `json:"original_amount"`
	Amount         decimal.Decimal  `json:"amount"`
	IssuedOn       string           `json:"issued_on"`
	ExpiresOn      string           `json:"expires_on"`
	Status         string           `json:"status"`
	Standing       string           `json:"standing"` // ACTIVE | EXPIRING | EXPIRED | RELEASED | CALLED
	DaysLeft       int              `json:"days_left"`
	Version        int              `json:"version"`
	CreatedBy      string           `json:"created_by"`
	ClosedBy       string           `json:"closed_by,omitempty"`
	CloseReference string           `json:"close_reference,omitempty"`
	CloseReason    string           `json:"close_reason,omitempty"`
	CalledAmount   *decimal.Decimal `json:"called_amount,omitempty"`
}

// AdvanceCover compares the advance the client is still owed back with the
// advance guarantees in force.
type AdvanceCover struct {
	ContractID     string          `json:"contract_id"`
	ContractNumber string          `json:"contract_number"`
	Currency       string          `json:"currency"`
	Received       decimal.Decimal `json:"advance_received"`
	Recovered      decimal.Decimal `json:"advance_recovered"`
	Outstanding    decimal.Decimal `json:"advance_outstanding"`
	Cover          decimal.Decimal `json:"advance_cover"`
	Shortfall      decimal.Decimal `json:"shortfall"`
}

// GuaranteeRegister is the guarantee register of the principal's projects.
type GuaranteeRegister struct {
	Guarantees []Guarantee    `json:"guarantees"`
	Cover      []AdvanceCover `json:"advance_cover"`
	Totals     map[string]any `json:"totals"`
}

const expiringDays = 30

func parseDay(name, v string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return d, fmt.Errorf("%w: %s must be a date (YYYY-MM-DD)", ErrInvalidInput, name)
	}
	return d, nil
}

// parseMonth accepts YYYY-MM (or a date, whose month is taken).
func parseMonth(name, v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if len(v) >= 10 {
		v = v[:7]
	}
	d, err := time.Parse("2006-01", v)
	if err != nil {
		return d, fmt.Errorf("%w: %s must be a month (YYYY-MM)", ErrInvalidInput, name)
	}
	return d, nil
}

// contractTx checks perm on the contract's project, then runs fn.
func (s *Service) contractTx(ctx context.Context, p auth.Principal, perm auth.Permission, contractID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM contracts WHERE id = $1::uuid`, contractID).Scan(&project); err != nil {
			return mapNoRows(err)
		}
		if err := s.authz.RequireProject(p, perm, p.TenantID, project); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// contractOf returns the contract a row of table belongs to (visibility is
// checked again by the caller's contractTx).
func (s *Service) contractOf(ctx context.Context, p auth.Principal, table, id string) (string, error) {
	var contract string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT contract_id::text FROM `+table+` WHERE id = $1::uuid`, id).Scan(&contract)
	})
	return contract, mapNoRows(err)
}

// RegisterGuarantee records a guarantee received from the bank.
func (s *Service) RegisterGuarantee(ctx context.Context, p auth.Principal, in GuaranteeInput) (string, error) {
	in.Kind = strings.ToUpper(strings.TrimSpace(in.Kind))
	switch in.Kind {
	case "BID", "ADVANCE", "PERFORMANCE", "RETENTION":
	default:
		return "", fmt.Errorf("%w: kind must be BID, ADVANCE, PERFORMANCE or RETENTION", ErrInvalidInput)
	}
	if !in.Amount.IsPositive() {
		return "", fmt.Errorf("%w: the guarantee's amount must be positive", ErrInvalidInput)
	}
	issued, err := parseDay("issued_on", in.IssuedOn)
	if err != nil {
		return "", err
	}
	expires, err := parseDay("expires_on", in.ExpiresOn)
	if err != nil {
		return "", err
	}
	if !expires.After(issued) {
		return "", fmt.Errorf("%w: the expiry must be after the issue date", ErrInvalidInput)
	}
	var id string
	err = s.contractTx(ctx, p, auth.PermBondManage, in.ContractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "BG")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO guarantees (contract_id, number, kind, issuer, reference, currency, original_amount, amount, issued_on, expires_on, created_by)
			SELECT c.id, $2, $3, $4, $5, COALESCE(NULLIF(upper($6), ''), c.currency), $7, $7, $8, $9, $10 FROM contracts c WHERE c.id = $1::uuid RETURNING id::text`,
			in.ContractID, num, in.Kind, strings.TrimSpace(in.Issuer), strings.TrimSpace(in.Reference), strings.TrimSpace(in.Currency), in.Amount, issued, expires, p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		return s.record(ctx, tx, p, "bond.create", "guarantee", id, "guarantee.registered", map[string]any{"guarantee_id": id, "contract_id": in.ContractID, "number": num, "kind": in.Kind, "amount": in.Amount.String()})
	})
	return id, err
}

// ChangeGuarantee reduces a guarantee's value or extends its expiry, with a reason.
func (s *Service) ChangeGuarantee(ctx context.Context, p auth.Principal, id string, in GuaranteeChange) error {
	if err := requireReason(in.Reason); err != nil {
		return err
	}
	if in.Amount == nil && strings.TrimSpace(in.ExpiresOn) == "" {
		return fmt.Errorf("%w: give a reduced amount, a later expiry, or both", ErrInvalidInput)
	}
	var expires *time.Time
	if strings.TrimSpace(in.ExpiresOn) != "" {
		d, err := parseDay("expires_on", in.ExpiresOn)
		if err != nil {
			return err
		}
		expires = &d
	}
	contract, err := s.contractOf(ctx, p, "guarantees", id)
	if err != nil {
		return err
	}
	return s.contractTx(ctx, p, auth.PermBondManage, contract, func(ctx context.Context, tx pgx.Tx) error {
		var amount decimal.Decimal
		var exp time.Time
		var status string
		if err := tx.QueryRow(ctx, `SELECT amount, expires_on, status FROM guarantees WHERE id = $1::uuid FOR UPDATE`, id).Scan(&amount, &exp, &status); err != nil {
			return mapNoRows(err)
		}
		if status != "ACTIVE" {
			return fmt.Errorf("%w: the guarantee is %s", ErrInvalidInput, status)
		}
		newAmount, newExp := amount, exp
		if in.Amount != nil {
			if !in.Amount.IsPositive() || in.Amount.GreaterThan(amount) {
				return fmt.Errorf("%w: a guarantee can only be reduced (now %s); register a new one for more cover", ErrInvalidInput, amount.String())
			}
			newAmount = *in.Amount
		}
		if expires != nil {
			if expires.Before(exp) {
				return fmt.Errorf("%w: the expiry can only be extended (now %s)", ErrInvalidInput, exp.Format("2006-01-02"))
			}
			newExp = *expires
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE guarantees SET amount = $2, expires_on = $3, change_reason = $4 WHERE id = $1::uuid AND status = 'ACTIVE'`,
			id, newAmount, newExp, strings.TrimSpace(in.Reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "bond.change", "guarantee", id, "guarantee.changed", map[string]any{"guarantee_id": id,
			"amount_before": amount.String(), "amount_after": newAmount.String(), "expires_before": exp.Format("2006-01-02"), "expires_after": newExp.Format("2006-01-02"), "reason": in.Reason})
	})
}

// CloseGuarantee releases a guarantee to the bank or records that the
// client called it: not the person who registered it, fresh MFA, attested.
func (s *Service) CloseGuarantee(ctx context.Context, p auth.Principal, id string, in GuaranteeClose) error {
	in.Outcome = strings.ToUpper(strings.TrimSpace(in.Outcome))
	switch in.Outcome {
	case "RELEASED":
		in.CalledAmount = nil
	case "CALLED":
		if in.CalledAmount == nil || !in.CalledAmount.IsPositive() {
			return fmt.Errorf("%w: give the amount the client called", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: outcome must be RELEASED or CALLED", ErrInvalidInput)
	}
	if err := requireReason(in.Reason); err != nil {
		return err
	}
	if len(strings.TrimSpace(in.Reference)) < 2 {
		return fmt.Errorf("%w: give the reference of the release or call letter", ErrInvalidInput)
	}
	if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermBondApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermBondApprove, "guarantee", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "ACTIVE"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "guarantee"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "bond.close", "guarantee", id, in.Outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE guarantees SET status = $2, closed_by = $3, close_reference = $4, close_reason = $5, called_amount = $6, closed_at = now() WHERE id = $1::uuid`,
			id, in.Outcome, p.UserID, strings.TrimSpace(in.Reference), strings.TrimSpace(in.Reason), in.CalledAmount); err != nil {
			return err
		}
		payload := map[string]any{"guarantee_id": id, "outcome": in.Outcome, "reference": in.Reference, "reason": in.Reason}
		if in.CalledAmount != nil {
			payload["called_amount"] = in.CalledAmount.String()
		}
		return s.record(ctx, tx, p, "bond.close", "guarantee", id, "guarantee."+strings.ToLower(in.Outcome), payload)
	})
}

// Guarantees returns the guarantee register of the principal's projects
// (all contracts, or one), with each guarantee's standing and the advance
// cover of each contract.
func (s *Service) Guarantees(ctx context.Context, p auth.Principal, contractID string) (GuaranteeRegister, error) {
	out := GuaranteeRegister{Guarantees: []Guarantee{}, Cover: []AdvanceCover{}}
	if !s.authz.Has(p, auth.PermP2PRead) && !s.authz.Has(p, auth.PermBondManage) && !s.authz.Has(p, auth.PermBondApprove) {
		return out, auth.ErrForbidden
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		rows, err := tx.Query(ctx, `SELECT g.id::text, g.number, g.contract_id::text, c.number, c.project_id::text, g.kind, g.issuer, g.reference, g.currency,
			g.original_amount, g.amount, g.issued_on::text, g.expires_on::text, g.status,
			CASE WHEN g.status <> 'ACTIVE' THEN g.status WHEN g.expires_on < current_date THEN 'EXPIRED'
			     WHEN g.expires_on - current_date <= $4 THEN 'EXPIRING' ELSE 'ACTIVE' END,
			(g.expires_on - current_date), g.version, g.created_by, COALESCE(g.closed_by, ''), COALESCE(g.close_reference, ''), COALESCE(g.close_reason, ''), g.called_amount
			FROM guarantees g JOIN contracts c ON c.id = g.contract_id
			WHERE ($1 = '' OR g.contract_id = $1::uuid) AND ($2 OR c.project_id = ANY($3::uuid[]))
			ORDER BY (g.status = 'ACTIVE') DESC, g.expires_on, g.number`, contractID, sc.TenantWide, sc.Projects, expiringDays)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g Guarantee
			if err := rows.Scan(&g.ID, &g.Number, &g.ContractID, &g.ContractNumber, &g.ProjectID, &g.Kind, &g.Issuer, &g.Reference, &g.Currency,
				&g.OriginalAmount, &g.Amount, &g.IssuedOn, &g.ExpiresOn, &g.Status, &g.Standing, &g.DaysLeft, &g.Version, &g.CreatedBy,
				&g.ClosedBy, &g.CloseReference, &g.CloseReason, &g.CalledAmount); err != nil {
				return err
			}
			out.Guarantees = append(out.Guarantees, g)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Advance still owed back to the client against the advance guarantees in force.
		rows, err = tx.Query(ctx, `SELECT c.id::text, c.number, c.currency, contract_advance_received(c.id),
			COALESCE((SELECT sum(i.advance_recovery) FROM ipcs i WHERE i.contract_id = c.id AND i.status IN ('CERTIFIED', 'INVOICED')), 0),
			COALESCE((SELECT sum(g.amount) FROM guarantees g WHERE g.contract_id = c.id AND g.kind = 'ADVANCE' AND g.status = 'ACTIVE' AND g.expires_on >= current_date), 0)
			FROM contracts c
			WHERE c.status = 'ACTIVE' AND ($1 = '' OR c.id = $1::uuid) AND ($2 OR c.project_id = ANY($3::uuid[]))
			  AND (c.advance_amount > 0 OR EXISTS (SELECT 1 FROM guarantees g WHERE g.contract_id = c.id AND g.kind = 'ADVANCE'))
			ORDER BY c.number`, contractID, sc.TenantWide, sc.Projects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a AdvanceCover
			if err := rows.Scan(&a.ContractID, &a.ContractNumber, &a.Currency, &a.Received, &a.Recovered, &a.Cover); err != nil {
				return err
			}
			a.Outstanding = decimal.Max(decimal.Zero, a.Received.Sub(a.Recovered))
			a.Shortfall = decimal.Max(decimal.Zero, a.Outstanding.Sub(a.Cover))
			out.Cover = append(out.Cover, a)
		}
		return rows.Err()
	})
	active, expiring, expired := 0, 0, 0
	held := map[string]decimal.Decimal{}
	for _, g := range out.Guarantees {
		switch g.Standing {
		case "ACTIVE":
			active++
		case "EXPIRING":
			active++
			expiring++
		case "EXPIRED":
			expired++
		}
		if g.Status == "ACTIVE" && g.Standing != "EXPIRED" {
			held[g.Currency] = held[g.Currency].Add(g.Amount)
		}
	}
	short := 0
	for _, a := range out.Cover {
		if a.Shortfall.IsPositive() {
			short++
		}
	}
	out.Totals = map[string]any{"active": active, "expiring": expiring, "expired": expired, "held": held, "cover_shortfalls": short}
	return out, err
}

// ---------------------------------------------------------- price indices

// PriceIndexValue is one month of an index.
type PriceIndexValue struct {
	ID        string          `json:"id"`
	Period    string          `json:"period"`
	Value     decimal.Decimal `json:"value"`
	SourceRef string          `json:"source_ref"`
	Status    string          `json:"status"`
	CreatedBy string          `json:"created_by"`
}

// PriceIndex is an index series with its values (newest first).
type PriceIndex struct {
	Code   string            `json:"code"`
	Name   string            `json:"name"`
	Source string            `json:"source"`
	Values []PriceIndexValue `json:"values"`
}

// IndexValueInput proposes the value of an index for a month.
type IndexValueInput struct {
	IndexCode string          `json:"index_code"`
	Period    string          `json:"period"` // YYYY-MM
	Value     decimal.Decimal `json:"value"`
	SourceRef string          `json:"source_ref"`
}

func (s *Service) escalationRead(p auth.Principal) error {
	if s.authz.Has(p, auth.PermP2PRead) || s.authz.Has(p, auth.PermEscalationManage) || s.authz.Has(p, auth.PermEscalationApprove) {
		return nil
	}
	return auth.ErrForbidden
}

// CreatePriceIndex adds an index series (code, name, publisher).
func (s *Service) CreatePriceIndex(ctx context.Context, p auth.Principal, code, name, source string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	return s.p2pTx(ctx, p, auth.PermEscalationManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO price_indices (code, name, source, created_by) VALUES ($1, $2, $3, $4)`, code, strings.TrimSpace(name), strings.TrimSpace(source), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "index.create", "price_index", code, "price_index.created", map[string]any{"code": code, "name": name})
	})
}

// ProposeIndexValue proposes a month's value (DRAFT); another person approves it.
func (s *Service) ProposeIndexValue(ctx context.Context, p auth.Principal, in IndexValueInput) (string, error) {
	period, err := parseMonth("period", in.Period)
	if err != nil {
		return "", err
	}
	if !in.Value.IsPositive() {
		return "", fmt.Errorf("%w: an index value must be positive", ErrInvalidInput)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermEscalationManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO price_index_values (index_code, period, value, source_ref, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
			strings.ToUpper(strings.TrimSpace(in.IndexCode)), period, in.Value, strings.TrimSpace(in.SourceRef), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "index.create", "price_index_value", id, "index_value.proposed", map[string]any{"id": id, "index": in.IndexCode, "period": period.Format("2006-01"), "value": in.Value.String()})
	})
	return id, err
}

// SubmitIndexValue submits a proposed value (its author).
func (s *Service) SubmitIndexValue(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermEscalationManage, "price_index_value", "price_index_values", id, "index.submit", "index_value.submitted")
}

// DecideIndexValue approves or rejects a value (not its author; fresh MFA to approve).
func (s *Service) DecideIndexValue(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermEscalationApprove, "price_index_value", "price_index_values", id, "index.decide", "index value", approve, reason, nil)
}

// PriceIndices lists the index series with their values.
func (s *Service) PriceIndices(ctx context.Context, p auth.Principal) ([]PriceIndex, error) {
	out := []PriceIndex{}
	if err := s.escalationRead(p); err != nil {
		return out, err
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT x.code, x.name, x.source,
			COALESCE((SELECT jsonb_agg(jsonb_build_object('id', v.id, 'period', to_char(v.period, 'YYYY-MM'), 'value', v.value, 'source_ref', v.source_ref, 'status', v.status, 'created_by', v.created_by)
			          ORDER BY v.period DESC, v.created_at DESC) FROM price_index_values v WHERE v.index_code = x.code), '[]')
			FROM price_indices x ORDER BY x.code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x PriceIndex
			var raw []byte
			if err := rows.Scan(&x.Code, &x.Name, &x.Source, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &x.Values); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// ---------------------------------------------------- escalation formulas

// FormulaTerm is one index of a formula and its weight b.
type FormulaTerm struct {
	IndexCode string          `json:"index_code"`
	Weight    decimal.Decimal `json:"weight"`
}

// FormulaInput sets a contract's price adjustment formula.
type FormulaInput struct {
	ContractID   string          `json:"contract_id"`
	ClauseRef    string          `json:"clause_ref"`
	FixedPortion decimal.Decimal `json:"fixed_portion"`
	BasePeriod   string          `json:"base_period"` // YYYY-MM
	Terms        []FormulaTerm   `json:"terms"`
}

// CreateFormula prepares a DRAFT formula; a + Σb must be exactly 1.
func (s *Service) CreateFormula(ctx context.Context, p auth.Principal, in FormulaInput) (string, error) {
	base, err := parseMonth("base_period", in.BasePeriod)
	if err != nil {
		return "", err
	}
	if len(in.Terms) == 0 {
		return "", fmt.Errorf("%w: a formula needs at least one index", ErrInvalidInput)
	}
	total := in.FixedPortion
	seen := map[string]bool{}
	for i := range in.Terms {
		in.Terms[i].IndexCode = strings.ToUpper(strings.TrimSpace(in.Terms[i].IndexCode))
		if seen[in.Terms[i].IndexCode] {
			return "", fmt.Errorf("%w: index %s appears twice", ErrInvalidInput, in.Terms[i].IndexCode)
		}
		seen[in.Terms[i].IndexCode] = true
		if !in.Terms[i].Weight.IsPositive() {
			return "", fmt.Errorf("%w: every weight must be positive", ErrInvalidInput)
		}
		total = total.Add(in.Terms[i].Weight)
	}
	if !total.Equal(decimal.NewFromInt(1)) {
		return "", fmt.Errorf("%w: the fixed portion and the weights must add up to exactly 1 (they add up to %s)", ErrInvalidInput, total.String())
	}
	var id string
	err = s.contractTx(ctx, p, auth.PermEscalationManage, in.ContractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "ESC")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO escalation_formulas (contract_id, number, fixed_portion, base_period, clause_ref, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`, in.ContractID, num, in.FixedPortion, base, strings.TrimSpace(in.ClauseRef), p.UserID)
		if err != nil {
			return err
		}
		for _, t := range in.Terms {
			if _, err := tx.Exec(ctx, `INSERT INTO escalation_terms (formula_id, index_code, weight) VALUES ($1::uuid, $2, $3)`, id, t.IndexCode, t.Weight); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "formula.create", "escalation_formula", id, "formula.created", map[string]any{"formula_id": id, "contract_id": in.ContractID, "number": num})
	})
	return id, err
}

// SubmitFormula submits a draft formula (its author).
func (s *Service) SubmitFormula(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermEscalationManage, "escalation_formula", "escalation_formulas", id, "formula.submit", "formula.submitted")
}

// DecideFormula approves or rejects a formula (not its author; fresh MFA to approve).
func (s *Service) DecideFormula(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermEscalationApprove, "escalation_formula", "escalation_formulas", id, "formula.decide", "formula", approve, reason, nil)
}

// ------------------------------------------------------ price adjustments

// AdjustmentTerm is what one index contributed.
type AdjustmentTerm struct {
	Index        string          `json:"index"`
	Weight       decimal.Decimal `json:"weight"`
	BaseValue    decimal.Decimal `json:"base_value"`
	CurrentValue decimal.Decimal `json:"current_value"`
	Ratio        decimal.Decimal `json:"ratio"`
}

// PriceAdjustment is a computed adjustment (saved or previewed).
type PriceAdjustment struct {
	ID            string           `json:"id,omitempty"`
	Number        string           `json:"number,omitempty"`
	IPCID         string           `json:"ipc_id"`
	IPCNumber     string           `json:"ipc_number"`
	CurrentPeriod string           `json:"current_period"`
	Currency      string           `json:"currency"`
	BaseValue     decimal.Decimal  `json:"base_value"`
	Factor        decimal.Decimal  `json:"factor"`
	Amount        decimal.Decimal  `json:"amount"`
	Terms         []AdjustmentTerm `json:"terms"`
	Status        string           `json:"status,omitempty"`
	CreatedBy     string           `json:"created_by,omitempty"`
	ApprovedBy    string           `json:"approved_by,omitempty"`
}

// PreviewAdjustment computes, without saving, the adjustment the database
// would record for a certified certificate and an index month.
func (s *Service) PreviewAdjustment(ctx context.Context, p auth.Principal, ipcID, period string) (PriceAdjustment, error) {
	var out PriceAdjustment
	month, err := parseMonth("period", period)
	if err != nil {
		return out, err
	}
	contract, err := s.contractOf(ctx, p, "ipcs", ipcID)
	if err != nil {
		return out, err
	}
	err = s.contractTx(ctx, p, auth.PermEscalationManage, contract, func(ctx context.Context, tx pgx.Tx) error {
		var formula, status string
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT i.number, i.status, i.currency, i.this_gross, COALESCE((SELECT id::text FROM escalation_formulas f WHERE f.contract_id = i.contract_id AND f.status = 'APPROVED'), '')
			FROM ipcs i WHERE i.id = $1::uuid`, ipcID).Scan(&out.IPCNumber, &status, &out.Currency, &out.BaseValue, &formula); err != nil {
			return mapNoRows(err)
		}
		if formula == "" {
			return fmt.Errorf("%w: the contract has no approved escalation formula", ErrInvalidInput)
		}
		if status != "CERTIFIED" && status != "INVOICED" {
			return fmt.Errorf("%w: a price adjustment is computed for a certified payment certificate (this one is %s)", ErrInvalidInput, status)
		}
		if err := tx.QueryRow(ctx, `SELECT r.factor, r.detail::text, p2p_round($3 * (r.factor - 1), $4) FROM price_adjustment_factor($1::uuid, $2) r`,
			formula, month, out.BaseValue, out.Currency).Scan(&out.Factor, &raw, &out.Amount); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out.Terms)
	})
	out.IPCID, out.CurrentPeriod = ipcID, month.Format("2006-01")
	return out, err
}

// PrepareAdjustment records the DRAFT price adjustment of a certified
// certificate; the database computes every figure.
func (s *Service) PrepareAdjustment(ctx context.Context, p auth.Principal, ipcID, period string) (string, error) {
	month, err := parseMonth("period", period)
	if err != nil {
		return "", err
	}
	contract, err := s.contractOf(ctx, p, "ipcs", ipcID)
	if err != nil {
		return "", err
	}
	var id string
	err = s.contractTx(ctx, p, auth.PermEscalationManage, contract, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "PA")
		if err != nil {
			return err
		}
		// contract_id, formula_id, currency and every amount are overwritten by the guard.
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO price_adjustments (ipc_id, contract_id, formula_id, number, current_period, currency, created_by)
			SELECT i.id, i.contract_id, f.id, $2, $3, i.currency, $4 FROM ipcs i
			LEFT JOIN escalation_formulas f ON f.contract_id = i.contract_id AND f.status = 'APPROVED'
			WHERE i.id = $1::uuid RETURNING id::text`, ipcID, num, month, p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		return s.record(ctx, tx, p, "adjustment.create", "price_adjustment", id, "price_adjustment.prepared", map[string]any{"adjustment_id": id, "ipc_id": ipcID, "period": month.Format("2006-01")})
	})
	return id, err
}

// SubmitAdjustment submits a draft adjustment (its author).
func (s *Service) SubmitAdjustment(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermEscalationManage, "price_adjustment", "price_adjustments", id, "adjustment.submit", "price_adjustment.submitted")
}

// DecideAdjustment approves or rejects an adjustment (not its author; fresh
// MFA to approve; the database recomputes it at approval).
func (s *Service) DecideAdjustment(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermEscalationApprove, "price_adjustment", "price_adjustments", id, "adjustment.decide", "price adjustment", approve, reason, nil)
}

// ContractEscalation is a contract's formula, its certificates and their adjustments.
type ContractEscalation struct {
	ContractID     string            `json:"contract_id"`
	ContractNumber string            `json:"contract_number"`
	Formula        map[string]any    `json:"formula,omitempty"`
	Adjustments    []PriceAdjustment `json:"adjustments"`
	Certificates   []map[string]any  `json:"certificates"`
	ApprovedTotal  decimal.Decimal   `json:"approved_total"`
}

// Escalation returns a contract's formula (approved, or the one in
// preparation), its adjustments and its certified certificates.
func (s *Service) Escalation(ctx context.Context, p auth.Principal, contractID string) (ContractEscalation, error) {
	out := ContractEscalation{ContractID: contractID, Adjustments: []PriceAdjustment{}, Certificates: []map[string]any{}}
	if err := s.escalationRead(p); err != nil {
		return out, err
	}
	perm := auth.PermP2PRead
	if !s.authz.Has(p, perm) {
		perm = auth.PermEscalationManage
		if !s.authz.Has(p, perm) {
			perm = auth.PermEscalationApprove
		}
	}
	err := s.contractTx(ctx, p, perm, contractID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT number FROM contracts WHERE id = $1::uuid`, contractID).Scan(&out.ContractNumber); err != nil {
			return mapNoRows(err)
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT jsonb_build_object('id', f.id, 'number', f.number, 'status', f.status, 'fixed_portion', f.fixed_portion, 'base_period', to_char(f.base_period, 'YYYY-MM'),
				'clause_ref', f.clause_ref, 'created_by', f.created_by, 'approved_by', f.approved_by,
				'terms', COALESCE((SELECT jsonb_agg(jsonb_build_object('index_code', t.index_code, 'weight', t.weight, 'name', x.name) ORDER BY t.index_code)
				                   FROM escalation_terms t JOIN price_indices x ON x.code = t.index_code WHERE t.formula_id = f.id), '[]'))::text
			FROM escalation_formulas f WHERE f.contract_id = $1::uuid AND f.status IN ('DRAFT', 'SUBMITTED', 'APPROVED')`, contractID).Scan(&raw)
		if err == nil {
			if err := json.Unmarshal(raw, &out.Formula); err != nil {
				return err
			}
		} else if err != pgx.ErrNoRows {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT a.id::text, a.number, a.ipc_id::text, i.number, to_char(a.current_period, 'YYYY-MM'), a.currency, a.base_value, a.factor, a.amount, a.detail::text,
			a.status, a.created_by, COALESCE(a.approved_by, '')
			FROM price_adjustments a JOIN ipcs i ON i.id = a.ipc_id WHERE a.contract_id = $1::uuid ORDER BY i.seq, i.period_end, a.created_at`, contractID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a PriceAdjustment
			if err := rows.Scan(&a.ID, &a.Number, &a.IPCID, &a.IPCNumber, &a.CurrentPeriod, &a.Currency, &a.BaseValue, &a.Factor, &a.Amount, &raw, &a.Status, &a.CreatedBy, &a.ApprovedBy); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &a.Terms); err != nil {
				return err
			}
			if a.Status == "APPROVED" {
				out.ApprovedTotal = out.ApprovedTotal.Add(a.Amount)
			}
			out.Adjustments = append(out.Adjustments, a)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT i.id::text, i.number, i.period_end::text, i.this_gross, i.status,
			EXISTS (SELECT 1 FROM price_adjustments a WHERE a.ipc_id = i.id AND a.status <> 'REJECTED')
			FROM ipcs i WHERE i.contract_id = $1::uuid AND i.status IN ('CERTIFIED', 'INVOICED') ORDER BY i.period_end`, contractID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, num, end, status string
			var gross decimal.Decimal
			var adjusted bool
			if err := rows.Scan(&id, &num, &end, &gross, &status, &adjusted); err != nil {
				return err
			}
			out.Certificates = append(out.Certificates, map[string]any{"id": id, "number": num, "period_end": end, "this_gross": gross, "status": status, "adjusted": adjusted})
		}
		return rows.Err()
	})
	return out, err
}
