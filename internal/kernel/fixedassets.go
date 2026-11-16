// Fixed assets (WP-2.9; SRS FIN-06, EQP-01; IAS 16 cost model). Migration:
// 0057_fixed_assets.sql.
//
// An accountant keeps asset classes, registers assets and prepares the
// monthly depreciation run; a finance manager activates assets and approves
// disposals. The database computes every charge, net book value and gain or
// loss; the kernel generates the FA_CAP, FA_DEP and FA_DISP journals that
// finance approves and posts.
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

// AssetClassInput defines an asset class.
type AssetClassInput struct {
	LegalEntityID string `json:"legal_entity_id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	CostAccount   string `json:"cost_account_id"`
	AccumAccount  string `json:"accum_account_id"`
	ExpenseAcct   string `json:"expense_account_id"`
	GainLossAcct  string `json:"gain_loss_account_id"`
	Method        string `json:"default_method"`
	LifeMonths    int    `json:"default_life_months"`
}

// AssetInput registers an asset.
type AssetInput struct {
	ClassID         string          `json:"class_id"`
	Description     string          `json:"description"`
	ProjectID       string          `json:"project_id,omitempty"`
	Location        string          `json:"location,omitempty"`
	InServiceOn     string          `json:"in_service_on"`
	Cost            decimal.Decimal `json:"cost"`
	Residual        decimal.Decimal `json:"residual"`
	LifeMonths      int             `json:"life_months"`
	Method          string          `json:"method"`
	CapitalisedFrom string          `json:"capitalised_from_account_id"`
}

// DisposalInput proposes a disposal.
type DisposalInput struct {
	AssetID         string          `json:"asset_id"`
	DisposedOn      string          `json:"disposed_on"`
	Proceeds        decimal.Decimal `json:"proceeds"`
	ProceedsAccount string          `json:"proceeds_account_id"`
	Reason          string          `json:"reason"`
}

func (s *Service) assetTx(ctx context.Context, p auth.Principal, perm auth.Permission, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !s.tenantWide(p) {
		return auth.ErrForbidden
	}
	return s.p2pTx(ctx, p, perm, fn)
}

// CreateAssetClass adds an asset class (its accounts are typed by the database).
func (s *Service) CreateAssetClass(ctx context.Context, p auth.Principal, in AssetClassInput) (string, error) {
	if in.Method == "" {
		in.Method = "SL"
	}
	var id string
	err := s.assetTx(ctx, p, auth.PermAssetManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO asset_classes (legal_entity_id, code, name, cost_account_id, accum_account_id, expense_account_id, gain_loss_account_id, default_method, default_life_months, created_by)
			VALUES ($1::uuid, $2, $3, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, $9, $10) RETURNING id::text`,
			in.LegalEntityID, strings.ToUpper(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Name), in.CostAccount, in.AccumAccount, in.ExpenseAcct, in.GainLossAcct, strings.ToUpper(in.Method), in.LifeMonths, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "asset.class.create", "asset_class", id, "asset_class.created", map[string]any{"id": id, "code": in.Code})
	})
	return id, err
}

// RegisterAsset records a DRAFT asset.
func (s *Service) RegisterAsset(ctx context.Context, p auth.Principal, in AssetInput) (string, error) {
	day, err := parseDay("in_service_on", in.InServiceOn)
	if err != nil {
		return "", err
	}
	in.Method = strings.ToUpper(strings.TrimSpace(in.Method))
	var id string
	err = s.assetTx(ctx, p, auth.PermAssetManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "FA")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO fixed_assets (legal_entity_id, class_id, number, description, project_id, location, in_service_on, cost, residual, life_months, method, capitalised_from_account_id, created_by)
			SELECT c.legal_entity_id, c.id, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, ''), $6, $7, $8, COALESCE(NULLIF($9, 0), c.default_life_months), COALESCE(NULLIF($10, ''), c.default_method), $11::uuid, $12
			  FROM asset_classes c WHERE c.id = $1::uuid RETURNING id::text`,
			in.ClassID, num, strings.TrimSpace(in.Description), in.ProjectID, strings.TrimSpace(in.Location), day, in.Cost, in.Residual, in.LifeMonths, in.Method, in.CapitalisedFrom, p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		return s.record(ctx, tx, p, "asset.create", "fixed_asset", id, "fixed_asset.registered", map[string]any{"id": id, "number": num, "cost": in.Cost.String()})
	})
	return id, err
}

// SubmitAsset submits a draft asset for activation (its author).
func (s *Service) SubmitAsset(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermAssetManage, "fixed_asset", "fixed_assets", id, "asset.submit", "fixed_asset.submitted")
}

// DecideAsset activates (generating the capitalisation journal) or rejects an asset.
func (s *Service) DecideAsset(ctx context.Context, p auth.Principal, id string, approve bool, reason string) (string, error) {
	if !approve {
		if err := requireReason(reason); err != nil {
			return "", err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return "", err
	}
	var jid string
	err := s.assetTx(ctx, p, auth.PermAssetApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermAssetApprove, "fixed_asset", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "fixed asset"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "asset.decide", "fixed_asset", id, outcome); err != nil {
			return err
		}
		next := map[bool]string{true: "ACTIVE", false: "REJECTED"}[approve]
		if err := postgres.Exec1(ctx, tx, `UPDATE fixed_assets SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, next, p.UserID, reason); err != nil {
			return err
		}
		if approve {
			var entity, ccy string
			var day time.Time
			if err := tx.QueryRow(ctx, `SELECT a.legal_entity_id::text, e.functional_currency, a.in_service_on FROM fixed_assets a JOIN legal_entities e ON e.id = a.legal_entity_id WHERE a.id = $1::uuid`, id).Scan(&entity, &ccy, &day); err != nil {
				return err
			}
			if jid, err = s.subledgerJournal(ctx, tx, p, "FA_CAP", id, entity, ccy, "Capitalisation of asset "+d.Number, day); err != nil {
				return err
			}
			if err := postgres.Exec1(ctx, tx, `UPDATE fixed_assets SET cap_journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "asset.decide", "fixed_asset", id, "fixed_asset."+strings.ToLower(next), map[string]any{"id": id, "journal_id": jid})
	})
	return jid, err
}

// DepreciationLine is one asset's charge in a run (or preview).
type DepreciationLine struct {
	AssetID   string          `json:"asset_id"`
	Number    string          `json:"number"`
	Desc      string          `json:"description"`
	Class     string          `json:"class"`
	NBVBefore decimal.Decimal `json:"nbv_before"`
	Amount    decimal.Decimal `json:"amount"`
}

func depreciationLines(ctx context.Context, tx pgx.Tx, entity string, period time.Time) ([]DepreciationLine, error) {
	rows, err := tx.Query(ctx, `SELECT c.asset_id::text, a.number, a.description, k.code, c.nbv_before, c.amount FROM fa_depreciation_compute($1::uuid, $2) c
		JOIN fixed_assets a ON a.id = c.asset_id JOIN asset_classes k ON k.id = a.class_id ORDER BY a.number`, entity, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DepreciationLine{}
	for rows.Next() {
		var l DepreciationLine
		if err := rows.Scan(&l.AssetID, &l.Number, &l.Desc, &l.Class, &l.NBVBefore, &l.Amount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func monthEnd(v string) (time.Time, error) {
	m, err := parseMonth("period", v)
	if err != nil {
		return m, err
	}
	return m.AddDate(0, 1, -1), nil
}

// PreviewDepreciation computes, without saving, a month's charges.
func (s *Service) PreviewDepreciation(ctx context.Context, p auth.Principal, entity, month string) ([]DepreciationLine, error) {
	end, err := monthEnd(month)
	if err != nil {
		return nil, err
	}
	var out []DepreciationLine
	err = s.assetTx(ctx, p, auth.PermJournalRead, func(ctx context.Context, tx pgx.Tx) error {
		out, err = depreciationLines(ctx, tx, entity, end)
		return err
	})
	return out, err
}

// PrepareDepreciation records the month's run and generates its FA_DEP journal.
func (s *Service) PrepareDepreciation(ctx context.Context, p auth.Principal, entity, month string) (string, error) {
	end, err := monthEnd(month)
	if err != nil {
		return "", err
	}
	var id string
	err = s.assetTx(ctx, p, auth.PermAssetManage, func(ctx context.Context, tx pgx.Tx) error {
		lines, err := depreciationLines(ctx, tx, entity, end)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return fmt.Errorf("%w: no asset has depreciation to charge for %s", ErrInvalidInput, end.Format("January 2006"))
		}
		num, err := postgres.NextNumber(ctx, tx, "DEP")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO depreciation_runs (legal_entity_id, number, period_end, created_by) VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`, entity, num, end, p.UserID)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO depreciation_lines (run_id, asset_id, nbv_before, amount) VALUES ($1::uuid, $2::uuid, $3, $4)`, id, l.AssetID, l.NBVBefore, l.Amount); err != nil {
				return err
			}
		}
		var ccy string
		if err := tx.QueryRow(ctx, `SELECT functional_currency FROM legal_entities WHERE id = $1::uuid`, entity).Scan(&ccy); err != nil {
			return err
		}
		jid, err := s.subledgerJournal(ctx, tx, p, "FA_DEP", id, entity, ccy, "Depreciation "+end.Format("January 2006"), end)
		if err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE depreciation_runs SET journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "asset.depreciate", "depreciation_run", id, "depreciation_run.prepared", map[string]any{"id": id, "period_end": end.Format("2006-01-02"), "journal_id": jid})
	})
	return id, err
}

// CancelDepreciation cancels a run whose journal is still a draft (finance), with its journal.
func (s *Service) CancelDepreciation(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	if !s.authz.Has(p, auth.PermJournalApprove) && !s.authz.Has(p, auth.PermJournalPost) {
		return fmt.Errorf("%w: finance (journal approval or posting) cancels a depreciation run", auth.ErrForbidden)
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var jid string
		if err := tx.QueryRow(ctx, `SELECT journal_id::text FROM depreciation_runs WHERE id = $1::uuid FOR UPDATE`, id).Scan(&jid); err != nil {
			return mapNoRows(err)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE depreciation_runs SET status = 'CANCELLED', cancel_reason = $2 WHERE id = $1::uuid AND status = 'PREPARED'`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		if err := s.cancelJournalTx(ctx, tx, p, jid, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "asset.depreciate.cancel", "depreciation_run", id, "depreciation_run.cancelled", map[string]any{"id": id, "reason": reason})
	})
}

// ProposeDisposal records a DRAFT disposal of an active asset.
func (s *Service) ProposeDisposal(ctx context.Context, p auth.Principal, in DisposalInput) (string, error) {
	day, err := parseDay("disposed_on", in.DisposedOn)
	if err != nil {
		return "", err
	}
	var id string
	err = s.assetTx(ctx, p, auth.PermAssetManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "DSP")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO asset_disposals (asset_id, number, disposed_on, proceeds, proceeds_account_id, reason, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6, $7) RETURNING id::text`, in.AssetID, num, day, in.Proceeds, in.ProceedsAccount, strings.TrimSpace(in.Reason), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "disposal.create", "asset_disposal", id, "asset_disposal.proposed", map[string]any{"id": id, "asset_id": in.AssetID})
	})
	return id, err
}

// SubmitDisposal submits a draft disposal (its author).
func (s *Service) SubmitDisposal(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermAssetManage, "asset_disposal", "asset_disposals", id, "disposal.submit", "asset_disposal.submitted")
}

// DecideDisposal approves (the database computes net book value and gain or
// loss; the FA_DISP journal is generated) or rejects a disposal.
func (s *Service) DecideDisposal(ctx context.Context, p auth.Principal, id string, approve bool, reason string) (string, error) {
	if !approve {
		if err := requireReason(reason); err != nil {
			return "", err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return "", err
	}
	var jid string
	err := s.assetTx(ctx, p, auth.PermAssetApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermAssetApprove, "asset_disposal", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "disposal"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "disposal.decide", "asset_disposal", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE asset_disposals SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		if approve {
			var entity, ccy string
			var day time.Time
			if err := tx.QueryRow(ctx, `SELECT a.legal_entity_id::text, e.functional_currency, d.disposed_on FROM asset_disposals d JOIN fixed_assets a ON a.id = d.asset_id
				JOIN legal_entities e ON e.id = a.legal_entity_id WHERE d.id = $1::uuid`, id).Scan(&entity, &ccy, &day); err != nil {
				return err
			}
			if jid, err = s.subledgerJournal(ctx, tx, p, "FA_DISP", id, entity, ccy, "Disposal "+d.Number, day); err != nil {
				return err
			}
			if err := postgres.Exec1(ctx, tx, `UPDATE asset_disposals SET journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "disposal.decide", "asset_disposal", id, "asset_disposal."+strings.ToLower(outcome), map[string]any{"id": id, "journal_id": jid})
	})
	return jid, err
}

// AssetRegister is the register: classes, assets with net book value, runs.
func (s *Service) AssetRegister(ctx context.Context, p auth.Principal) (map[string]any, error) {
	out := map[string]any{}
	err := s.assetTx(ctx, p, auth.PermJournalRead, func(ctx context.Context, tx pgx.Tx) error {
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
		if err := q("classes", `SELECT c.id, c.legal_entity_id, c.code, c.name, c.default_method, c.default_life_months,
			(SELECT code || ' ' || name FROM accounts WHERE id = c.cost_account_id) AS cost_account, (SELECT code || ' ' || name FROM accounts WHERE id = c.accum_account_id) AS accum_account,
			(SELECT code || ' ' || name FROM accounts WHERE id = c.expense_account_id) AS expense_account FROM asset_classes c ORDER BY c.code`); err != nil {
			return err
		}
		if err := q("assets", `SELECT a.id, a.number, a.description, k.code AS class, a.status, a.in_service_on, a.cost, a.residual, a.life_months, a.method,
			fa_accumulated(a.id) AS accumulated, a.cost - fa_accumulated(a.id) AS nbv, a.location, (SELECT code FROM projects WHERE id = a.project_id) AS project,
			(SELECT status FROM journals WHERE id = a.cap_journal_id) AS cap_journal_status
			FROM fixed_assets a JOIN asset_classes k ON k.id = a.class_id ORDER BY a.number`); err != nil {
			return err
		}
		return q("runs", `SELECT r.id, r.number, r.period_end, r.status, r.created_by, (SELECT sum(amount) FROM depreciation_lines l WHERE l.run_id = r.id) AS total,
			(SELECT count(*) FROM depreciation_lines l WHERE l.run_id = r.id) AS assets, (SELECT status FROM journals WHERE id = r.journal_id) AS journal_status
			FROM depreciation_runs r ORDER BY r.period_end DESC, r.created_at DESC`)
	})
	return out, err
}
