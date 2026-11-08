// Liquidated damages, extension of time and contract obligations (WP-2.6
// completion; SRS CON-09, CON-04, SCH-07). Migration: 0051_contract_time.sql.
//
// The damages terms (time for completion, rate per day, cap) are approved
// once per contract by a second person. The revised completion date is the
// approved date plus the days the client agreed on claims, so an agreed
// extension of time is carried automatically. Damages accrued to date and
// the damages forecast from the baseline schedule are computed here, capped.
// Obligations (who must do what by when under the contract) are raised open
// and closed once.
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

// LDTermsInput sets a contract's time for completion and damages.
type LDTermsInput struct {
	ContractID     string          `json:"contract_id"`
	ClauseRef      string          `json:"clause_ref"`
	CompletionDate string          `json:"completion_date"`
	RatePerDay     decimal.Decimal `json:"rate_per_day"`
	CapAmount      decimal.Decimal `json:"cap_amount"`
}

// ObligationInput records an obligation under a contract.
type ObligationInput struct {
	ContractID  string `json:"contract_id"`
	ClauseRef   string `json:"clause_ref"`
	Description string `json:"description"`
	Party       string `json:"party"` // CONTRACTOR | CLIENT | ENGINEER
	Owner       string `json:"owner"`
	DueOn       string `json:"due_on"`
}

// Obligation is an obligation with its standing.
type Obligation struct {
	ID             string `json:"id"`
	Number         string `json:"number"`
	ContractID     string `json:"contract_id"`
	ContractNumber string `json:"contract_number"`
	ClauseRef      string `json:"clause_ref"`
	Description    string `json:"description"`
	Party          string `json:"party"`
	Owner          string `json:"owner"`
	DueOn          string `json:"due_on"`
	Status         string `json:"status"`
	Standing       string `json:"standing"` // OPEN | DUE_SOON | OVERDUE | DONE | WAIVED
	DaysLeft       int    `json:"days_left"`
	ClosedOn       string `json:"closed_on,omitempty"`
	ClosedBy       string `json:"closed_by,omitempty"`
	CloseNote      string `json:"close_note,omitempty"`
}

// ContractTime is a contract's time position and damages.
type ContractTime struct {
	ContractID        string           `json:"contract_id"`
	ContractNumber    string           `json:"contract_number"`
	Currency          string           `json:"currency"`
	Terms             map[string]any   `json:"terms,omitempty"` // approved or in preparation
	CompletionDate    string           `json:"completion_date,omitempty"`
	ExtensionDays     int              `json:"extension_days"`
	RevisedCompletion string           `json:"revised_completion,omitempty"`
	Extensions        []map[string]any `json:"extensions"`
	ScheduleFinish    string           `json:"schedule_finish,omitempty"`
	ForecastDelay     int              `json:"forecast_delay_days"`
	DaysLate          int              `json:"days_late"`
	RatePerDay        decimal.Decimal  `json:"rate_per_day"`
	Cap               decimal.Decimal  `json:"cap"`
	AccruedToDate     decimal.Decimal  `json:"damages_to_date"`
	Forecast          decimal.Decimal  `json:"damages_forecast"`
	CapReached        bool             `json:"cap_reached"`
	Obligations       []Obligation     `json:"obligations"`
}

// LDAmount is days × rate, never negative and never above the cap.
func LDAmount(days int, rate, cap decimal.Decimal) decimal.Decimal {
	if days <= 0 {
		return decimal.Zero
	}
	return decimal.Min(rate.Mul(decimal.NewFromInt(int64(days))), cap)
}

// CreateLDTerms prepares a contract's damages terms (DRAFT).
func (s *Service) CreateLDTerms(ctx context.Context, p auth.Principal, in LDTermsInput) (string, error) {
	done, err := parseDay("completion_date", in.CompletionDate)
	if err != nil {
		return "", err
	}
	if !in.RatePerDay.IsPositive() || !in.CapAmount.IsPositive() || in.CapAmount.LessThan(in.RatePerDay) {
		return "", fmt.Errorf("%w: the rate per day and the cap must be positive, and the cap at least one day's damages", ErrInvalidInput)
	}
	var id string
	err = s.contractTx(ctx, p, auth.PermClaimManage, in.ContractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "LD")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO contract_ld_terms (contract_id, number, clause_ref, completion_date, rate_per_day, cap_amount, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7) RETURNING id::text`, in.ContractID, num, strings.TrimSpace(in.ClauseRef), done, in.RatePerDay, in.CapAmount, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "ld.create", "ld_terms", id, "ld_terms.created", map[string]any{"id": id, "contract_id": in.ContractID, "number": num})
	})
	return id, err
}

// SubmitLDTerms submits draft terms (their author).
func (s *Service) SubmitLDTerms(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermClaimManage, "ld_terms", "contract_ld_terms", id, "ld.submit", "ld_terms.submitted")
}

// DecideLDTerms approves or rejects terms (not their author; fresh MFA to approve).
func (s *Service) DecideLDTerms(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermClaimApprove, "ld_terms", "contract_ld_terms", id, "ld.decide", "damages terms", approve, reason, nil)
}

// ContractTimePosition returns the contract's time for completion, agreed
// extensions, schedule forecast, damages and obligations.
func (s *Service) ContractTimePosition(ctx context.Context, p auth.Principal, contractID string) (ContractTime, error) {
	out := ContractTime{ContractID: contractID, Extensions: []map[string]any{}, Obligations: []Obligation{}}
	err := s.contractTx(ctx, p, auth.PermP2PRead, contractID, func(ctx context.Context, tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(ctx, `SELECT number, currency, project_id::text FROM contracts WHERE id = $1::uuid`, contractID).Scan(&out.ContractNumber, &out.Currency, &project); err != nil {
			return mapNoRows(err)
		}
		var id, number, status, clause, by string
		var done time.Time
		var rate, cap decimal.Decimal
		err := tx.QueryRow(ctx, `SELECT id::text, number, status, clause_ref, completion_date, rate_per_day, cap_amount, created_by FROM contract_ld_terms
			WHERE contract_id = $1::uuid AND status IN ('DRAFT', 'SUBMITTED', 'APPROVED')`, contractID).Scan(&id, &number, &status, &clause, &done, &rate, &cap, &by)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if err == nil {
			out.Terms = map[string]any{"id": id, "number": number, "status": status, "clause_ref": clause, "completion_date": done.Format("2006-01-02"),
				"rate_per_day": rate, "cap_amount": cap, "created_by": by}
		}
		today := time.Now().UTC().Truncate(24 * time.Hour)
		if status == "APPROVED" {
			var completion, revised time.Time
			if err := tx.QueryRow(ctx, `SELECT completion_date, extension_days, revised_completion FROM contract_time($1::uuid)`, contractID).Scan(&completion, &out.ExtensionDays, &revised); err != nil {
				return err
			}
			out.CompletionDate, out.RevisedCompletion = completion.Format("2006-01-02"), revised.Format("2006-01-02")
			out.RatePerDay, out.Cap = rate, cap
			out.DaysLate = int(today.Sub(revised).Hours() / 24)
			if out.DaysLate < 0 {
				out.DaysLate = 0
			}
			out.AccruedToDate = LDAmount(out.DaysLate, rate, cap)
			var finish *time.Time
			if err := tx.QueryRow(ctx, `SELECT max(finish_date) FROM schedule_versions WHERE project_id = $1::uuid AND status = 'BASELINE'`, project).Scan(&finish); err != nil {
				return err
			}
			if finish != nil {
				out.ScheduleFinish = finish.Format("2006-01-02")
				out.ForecastDelay = int(finish.Sub(revised).Hours() / 24)
				out.Forecast = LDAmount(out.ForecastDelay, rate, cap)
			}
			out.CapReached = out.AccruedToDate.Equal(cap) || out.Forecast.Equal(cap)
		}
		rows, err := tx.Query(ctx, `SELECT id::text, number, title, agreed_days, outcome_on::text, client_reference FROM contract_claims
			WHERE contract_id = $1::uuid AND client_outcome = 'AGREED' AND agreed_days > 0 ORDER BY outcome_on`, contractID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cid, num, title, on, ref string
			var days int
			if err := rows.Scan(&cid, &num, &title, &days, &on, &ref); err != nil {
				return err
			}
			out.Extensions = append(out.Extensions, map[string]any{"claim_id": cid, "number": num, "title": title, "days": days, "agreed_on": on, "client_reference": ref})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.Obligations, err = obligationsTx(ctx, tx, contractID, true, nil)
		return err
	})
	return out, err
}

func obligationsTx(ctx context.Context, tx pgx.Tx, contractID string, wide bool, projects []string) ([]Obligation, error) {
	if projects == nil {
		projects = []string{}
	}
	rows, err := tx.Query(ctx, `SELECT o.id::text, o.number, o.contract_id::text, c.number, o.clause_ref, o.description, o.party, o.owner, o.due_on::text, o.status,
		CASE WHEN o.status <> 'OPEN' THEN o.status WHEN o.due_on < current_date THEN 'OVERDUE' WHEN o.due_on - current_date <= 7 THEN 'DUE_SOON' ELSE 'OPEN' END,
		(o.due_on - current_date), COALESCE(o.closed_on::text, ''), COALESCE(o.closed_by, ''), COALESCE(o.close_note, '')
		FROM contract_obligations o JOIN contracts c ON c.id = o.contract_id
		WHERE ($1 = '' OR o.contract_id = $1::uuid) AND ($2 OR c.project_id = ANY($3::uuid[]))
		ORDER BY (o.status = 'OPEN') DESC, o.due_on, o.number`, contractID, wide, projects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Obligation{}
	for rows.Next() {
		var o Obligation
		if err := rows.Scan(&o.ID, &o.Number, &o.ContractID, &o.ContractNumber, &o.ClauseRef, &o.Description, &o.Party, &o.Owner, &o.DueOn, &o.Status,
			&o.Standing, &o.DaysLeft, &o.ClosedOn, &o.ClosedBy, &o.CloseNote); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Obligations lists the obligations of the principal's projects.
func (s *Service) Obligations(ctx context.Context, p auth.Principal) ([]Obligation, error) {
	var out []Obligation
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		var err error
		out, err = obligationsTx(ctx, tx, "", sc.TenantWide, sc.Projects)
		return err
	})
	return out, err
}

// AddObligation records an obligation under an active contract.
func (s *Service) AddObligation(ctx context.Context, p auth.Principal, in ObligationInput) (string, error) {
	due, err := parseDay("due_on", in.DueOn)
	if err != nil {
		return "", err
	}
	in.Party = strings.ToUpper(strings.TrimSpace(in.Party))
	switch in.Party {
	case "CONTRACTOR", "CLIENT", "ENGINEER":
	default:
		return "", fmt.Errorf("%w: party must be CONTRACTOR, CLIENT or ENGINEER", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Owner) == "" {
		in.Owner = p.UserID
	}
	var id string
	err = s.contractTx(ctx, p, auth.PermClaimManage, in.ContractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "OBL")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO contract_obligations (contract_id, number, clause_ref, description, party, owner, due_on, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, in.ContractID, num, strings.TrimSpace(in.ClauseRef), strings.TrimSpace(in.Description), in.Party, strings.TrimSpace(in.Owner), due, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "obligation.create", "contract_obligation", id, "obligation.created", map[string]any{"id": id, "contract_id": in.ContractID, "number": num, "due_on": in.DueOn})
	})
	return id, err
}

// CloseObligation records an obligation as done or waived (once).
func (s *Service) CloseObligation(ctx context.Context, p auth.Principal, id, status, on, note string) error {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "DONE" && status != "WAIVED" {
		return fmt.Errorf("%w: status must be DONE or WAIVED", ErrInvalidInput)
	}
	day, err := parseDay("closed_on", on)
	if err != nil {
		return err
	}
	if status == "WAIVED" {
		if err := requireReason(note); err != nil {
			return err
		}
	} else if len(strings.TrimSpace(note)) < 2 {
		return fmt.Errorf("%w: say how it was met (for example the letter reference)", ErrInvalidInput)
	}
	contract, err := s.contractOf(ctx, p, "contract_obligations", id)
	if err != nil {
		return err
	}
	return s.contractTx(ctx, p, auth.PermClaimManage, contract, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE contract_obligations SET status = $2, closed_on = $3, closed_by = $4, close_note = $5 WHERE id = $1::uuid AND status = 'OPEN'`,
			id, status, day, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "obligation.close", "contract_obligation", id, "obligation."+strings.ToLower(status), map[string]any{"id": id, "note": note})
	})
}
