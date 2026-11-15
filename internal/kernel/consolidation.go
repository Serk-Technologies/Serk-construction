// Multi-entity consolidation and intercompany (WP-2.10; SRS FIN-08, FIN-09).
// Migration: 0062_consolidation.sql.
//
// A controller proposes a consolidation group (members, ownership, method)
// and designates intercompany accounts; the CFO approves the group. A run
// for a period carries the closing and average rates it uses; the database
// computes every consolidated line (translation, proportionate share,
// equity method, non-controlling interests, eliminations and unmatched
// intercompany differences). The CFO approves the run with fresh MFA.
// Nothing is posted to any entity's ledger.
package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// GroupMember is one entity of a consolidation group.
type GroupMember struct {
	EntityID  string          `json:"legal_entity_id"`
	Ownership decimal.Decimal `json:"ownership_pct"` // 0 < x <= 1
	Method    string          `json:"method"`        // FULL | PROPORTIONATE | EQUITY
}

// GroupInput proposes a consolidation group version.
type GroupInput struct {
	Code         string        `json:"code"`
	Name         string        `json:"name"`
	Presentation string        `json:"presentation_currency"`
	ParentID     string        `json:"parent_entity_id"`
	Members      []GroupMember `json:"members"`
}

// RatePair is the closing and average rate of one functional currency
// (presentation currency per one functional unit).
type RatePair struct {
	Closing decimal.Decimal `json:"closing"`
	Average decimal.Decimal `json:"average"`
}

// ConsolidationRunInput prepares a run.
type ConsolidationRunInput struct {
	GroupID     string              `json:"group_id"`
	PeriodStart string              `json:"period_start"`
	PeriodEnd   string              `json:"period_end"`
	Rates       map[string]RatePair `json:"rates"`
	RateSource  string              `json:"rate_source"`
}

// ConsolidationLine is one computed line.
type ConsolidationLine struct {
	EntityID string           `json:"legal_entity_id,omitempty"`
	Entity   string           `json:"entity,omitempty"`
	Code     string           `json:"account_code"`
	Name     string           `json:"account_name"`
	Type     string           `json:"account_type"`
	Kind     string           `json:"kind"`
	Local    decimal.Decimal  `json:"local_amount"`
	Rate     *decimal.Decimal `json:"rate,omitempty"`
	Amount   decimal.Decimal  `json:"amount"`
}

// ConsolidatedRow is one account code of the consolidated trial balance.
type ConsolidatedRow struct {
	Code         string                     `json:"account_code"`
	Name         string                     `json:"account_name"`
	Type         string                     `json:"account_type"`
	ByEntity     map[string]decimal.Decimal `json:"by_entity"`
	Adjustments  decimal.Decimal            `json:"adjustments"`
	Eliminations decimal.Decimal            `json:"eliminations"`
	Total        decimal.Decimal            `json:"total"`
}

func (s *Service) consTx(ctx context.Context, p auth.Principal, perm auth.Permission, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !s.tenantWide(p) {
		return auth.ErrForbidden
	}
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, fn)
}

// ProposeGroup proposes the next version of a consolidation group.
func (s *Service) ProposeGroup(ctx context.Context, p auth.Principal, in GroupInput) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if len(in.Members) < 2 {
		return "", fmt.Errorf("%w: a group has at least two member entities", ErrInvalidInput)
	}
	var id string
	err := s.consTx(ctx, p, auth.PermConsolidationManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO consolidation_groups (code, version, name, presentation_currency, parent_entity_id, created_by)
			SELECT $1, COALESCE(max(version), 0) + 1, $2, $3, $4::uuid, $5 FROM consolidation_groups WHERE code = $1 RETURNING id::text`,
			code, strings.TrimSpace(in.Name), strings.ToUpper(strings.TrimSpace(in.Presentation)), in.ParentID, p.UserID)
		if err != nil {
			return err
		}
		for _, m := range in.Members {
			if _, err := tx.Exec(ctx, `INSERT INTO consolidation_members (group_id, legal_entity_id, ownership_pct, method) VALUES ($1::uuid, $2::uuid, $3, $4)`,
				id, m.EntityID, m.Ownership, strings.ToUpper(strings.TrimSpace(m.Method))); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "congroup.create", "consolidation_group", id, "consolidation_group.proposed", map[string]any{"id": id, "code": code})
	})
	return id, err
}

// DecideGroup approves (retiring the version in force) or rejects a group.
func (s *Service) DecideGroup(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.consTx(ctx, p, auth.PermConsolidationApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermConsolidationApprove, "consolidation_group", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "consolidation group"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "congroup.decide", "consolidation_group", id, outcome); err != nil {
			return err
		}
		if approve {
			if _, err := tx.Exec(ctx, `UPDATE consolidation_groups SET status = 'RETIRED' WHERE status = 'APPROVED' AND code = (SELECT code FROM consolidation_groups WHERE id = $1::uuid)`, id); err != nil {
				return err
			}
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE consolidation_groups SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "congroup.decide", "consolidation_group", id, "consolidation_group."+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// DesignateIntercompany marks an entity account as intercompany with its
// counterparty and role (RECEIVABLE, PAYABLE, REVENUE, EXPENSE).
func (s *Service) DesignateIntercompany(ctx context.Context, p auth.Principal, account, counterparty, role string) (string, error) {
	var id string
	err := s.consTx(ctx, p, auth.PermConsolidationManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO intercompany_accounts (account_id, counterparty_entity_id, role, created_by) VALUES ($1::uuid, $2::uuid, $3, $4) RETURNING id::text`,
			account, counterparty, strings.ToUpper(strings.TrimSpace(role)), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "intercompany.designate", "intercompany_account", id, "intercompany_account.designated", map[string]any{"id": id, "account_id": account, "role": role})
	})
	return id, err
}

// PrepareConsolidation prepares a run: the database computes its lines.
func (s *Service) PrepareConsolidation(ctx context.Context, p auth.Principal, in ConsolidationRunInput) (string, error) {
	start, err := parseDay("period_start", in.PeriodStart)
	if err != nil {
		return "", err
	}
	end, err := parseDay("period_end", in.PeriodEnd)
	if err != nil {
		return "", err
	}
	rates := map[string]map[string]json.Number{}
	for ccy, r := range in.Rates {
		rates[strings.ToUpper(strings.TrimSpace(ccy))] = map[string]json.Number{"closing": json.Number(r.Closing.String()), "average": json.Number(r.Average.String())}
	}
	raw, err := json.Marshal(rates)
	if err != nil {
		return "", err
	}
	var id string
	err = s.consTx(ctx, p, auth.PermConsolidationManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "CON")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO consolidation_runs (group_id, number, period_start, period_end, rates, rate_source, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6, $7) RETURNING id::text`, in.GroupID, num, start, end, string(raw), strings.TrimSpace(in.RateSource), p.UserID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO consolidation_lines (run_id, legal_entity_id, account_code, account_name, account_type, kind, local_amount, rate, amount)
			SELECT $1::uuid, c.legal_entity_id, c.account_code, c.account_name, c.account_type, c.kind, c.local_amount, c.rate, c.amount FROM consolidation_compute($1::uuid) c`, id); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE consolidation_runs SET sealed = true WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "conrun.prepare", "consolidation_run", id, "consolidation_run.prepared", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// DecideConsolidation approves or rejects a prepared run (consolidation.approve,
// not the preparer, fresh MFA to approve).
func (s *Service) DecideConsolidation(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.consTx(ctx, p, auth.PermConsolidationApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermConsolidationApprove, "consolidation_run", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "PREPARED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "consolidation run"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "conrun.decide", "consolidation_run", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE consolidation_runs SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "conrun.decide", "consolidation_run", id, "consolidation_run."+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// CancelConsolidation cancels a prepared run with a reason (to prepare it again).
func (s *Service) CancelConsolidation(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.consTx(ctx, p, auth.PermConsolidationManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE consolidation_runs SET status = 'CANCELLED', decision_reason = $2 WHERE id = $1::uuid AND status = 'PREPARED'`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "conrun.cancel", "consolidation_run", id, "consolidation_run.cancelled", map[string]any{"id": id, "reason": reason})
	})
}

// ConsolidationView lists entities, groups, intercompany accounts and runs.
func (s *Service) ConsolidationView(ctx context.Context, p auth.Principal) (map[string]any, error) {
	out := map[string]any{}
	if !s.authz.Has(p, auth.PermConsolidationManage) && !s.authz.Has(p, auth.PermConsolidationApprove) {
		return out, auth.ErrForbidden
	}
	err := s.consTx(ctx, p, map[bool]auth.Permission{true: auth.PermConsolidationManage, false: auth.PermConsolidationApprove}[s.authz.Has(p, auth.PermConsolidationManage)], func(ctx context.Context, tx pgx.Tx) error {
		q := func(key, sql string) error {
			rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (`+sql+`) r`)
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
		if err := q("entities", `SELECT id, name, functional_currency FROM legal_entities ORDER BY name`); err != nil {
			return err
		}
		if err := q("accounts", `SELECT a.id, a.code, a.name, a.type, a.legal_entity_id, e.name AS entity FROM accounts a JOIN legal_entities e ON e.id = a.legal_entity_id WHERE a.active ORDER BY e.name, a.code`); err != nil {
			return err
		}
		if err := q("groups", `SELECT g.id, g.code, g.version, g.name, g.presentation_currency, g.parent_entity_id, g.status, g.created_by, g.approved_by,
			(SELECT json_agg(json_build_object('legal_entity_id', m.legal_entity_id, 'entity', e.name, 'currency', e.functional_currency, 'ownership_pct', m.ownership_pct, 'method', m.method) ORDER BY e.name)
			   FROM consolidation_members m JOIN legal_entities e ON e.id = m.legal_entity_id WHERE m.group_id = g.id) AS members
			FROM consolidation_groups g ORDER BY g.code, g.version DESC`); err != nil {
			return err
		}
		if err := q("intercompany", `SELECT ic.id, ic.role, a.code, a.name, e.name AS entity, c.name AS counterparty FROM intercompany_accounts ic JOIN accounts a ON a.id = ic.account_id
			JOIN legal_entities e ON e.id = a.legal_entity_id JOIN legal_entities c ON c.id = ic.counterparty_entity_id ORDER BY e.name, a.code`); err != nil {
			return err
		}
		return q("runs", `SELECT r.id, r.number, g.code AS group_code, g.presentation_currency, r.period_start, r.period_end, r.rates, r.rate_source, r.status, r.created_by, r.approved_by,
			(SELECT COALESCE(sum(abs(amount)), 0) FROM consolidation_lines l WHERE l.run_id = r.id AND l.kind = 'IC_DIFF') AS ic_difference
			FROM consolidation_runs r JOIN consolidation_groups g ON g.id = r.group_id ORDER BY r.period_end DESC, r.created_at DESC`)
	})
	return out, err
}

// ConsolidationReport returns a run's lines and its consolidated trial
// balance by account code, with totals per type and the profit.
func (s *Service) ConsolidationReport(ctx context.Context, p auth.Principal, id string) (map[string]any, error) {
	out := map[string]any{}
	perm := auth.PermConsolidationApprove
	if s.authz.Has(p, auth.PermConsolidationManage) {
		perm = auth.PermConsolidationManage
	}
	err := s.consTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		var number, status, ccy string
		if err := tx.QueryRow(ctx, `SELECT r.number, r.status, g.presentation_currency FROM consolidation_runs r JOIN consolidation_groups g ON g.id = r.group_id WHERE r.id = $1::uuid`, id).Scan(&number, &status, &ccy); err != nil {
			return mapNoRows(err)
		}
		rows, err := tx.Query(ctx, `SELECT COALESCE(l.legal_entity_id::text, ''), COALESCE(e.name, ''), l.account_code, l.account_name, l.account_type, l.kind, l.local_amount, l.rate, l.amount
			FROM consolidation_lines l LEFT JOIN legal_entities e ON e.id = l.legal_entity_id WHERE l.run_id = $1::uuid ORDER BY l.account_code COLLATE "C", e.name, l.kind`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		lines := []ConsolidationLine{}
		byCode := map[string]*ConsolidatedRow{}
		entities := []string{}
		seen := map[string]bool{}
		totals := map[string]decimal.Decimal{}
		for rows.Next() {
			var l ConsolidationLine
			if err := rows.Scan(&l.EntityID, &l.Entity, &l.Code, &l.Name, &l.Type, &l.Kind, &l.Local, &l.Rate, &l.Amount); err != nil {
				return err
			}
			lines = append(lines, l)
			r := byCode[l.Code]
			if r == nil {
				r = &ConsolidatedRow{Code: l.Code, Name: l.Name, Type: l.Type, ByEntity: map[string]decimal.Decimal{}}
				byCode[l.Code] = r
			}
			switch l.Kind {
			case "TB":
				r.ByEntity[l.Entity] = r.ByEntity[l.Entity].Add(l.Amount)
				if !seen[l.Entity] {
					seen[l.Entity] = true
					entities = append(entities, l.Entity)
				}
			case "ELIMINATION", "IC_DIFF":
				r.Eliminations = r.Eliminations.Add(l.Amount)
			default:
				r.Adjustments = r.Adjustments.Add(l.Amount)
			}
			r.Total = r.Total.Add(l.Amount)
			totals[l.Type] = totals[l.Type].Add(l.Amount)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		tb := make([]ConsolidatedRow, 0, len(byCode))
		for _, r := range byCode {
			tb = append(tb, *r)
		}
		sort.Slice(tb, func(i, j int) bool { return tb[i].Code < tb[j].Code })
		sort.Strings(entities)
		profit := totals["REVENUE"].Add(totals["EXPENSE"]).Neg()
		out["number"], out["status"], out["currency"] = number, status, ccy
		out["lines"], out["trial_balance"], out["entities"] = lines, tb, entities
		out["totals"] = map[string]any{"assets": totals["ASSET"], "liabilities": totals["LIABILITY"].Neg(), "equity": totals["EQUITY"].Neg(),
			"revenue": totals["REVENUE"].Neg(), "expense": totals["EXPENSE"], "profit": profit, "suspense": totals["SUSPENSE"]}
		return nil
	})
	return out, err
}
