// Fleet policy (migration 0057, ADR 0024): the thresholds used when fuel is
// reconciled and maintenance is judged due are an approved, versioned,
// effective-dated document, not constants in code.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/fleet"
)

// FleetPolicyInput prepares a fleet policy.
type FleetPolicyInput struct {
	EffectiveFrom         string          `json:"effective_from"`
	FuelVarianceTolerance decimal.Decimal `json:"fuel_variance_tolerance"`  // share of the larger figure, e.g. 0.08
	FuelVarianceMinLitres decimal.Decimal `json:"fuel_variance_min_litres"` // differences up to this are never listed
	DueSoonFraction       decimal.Decimal `json:"due_soon_fraction"`        // share of a service interval left
	DueSoonDays           int             `json:"due_soon_days"`
	Basis                 string          `json:"basis"` // who decided this and on what grounds
}

// PolicyInForce says which thresholds a report used and where they came from.
type PolicyInForce struct {
	Source                string `json:"source"` // APPROVED_POLICY | ENGINEERING_DEFAULT
	PolicyID              string `json:"policy_id,omitempty"`
	Number                string `json:"number,omitempty"`
	EffectiveFrom         string `json:"effective_from,omitempty"`
	FuelVarianceTolerance string `json:"fuel_variance_tolerance"`
	FuelVarianceMinLitres string `json:"fuel_variance_min_litres"`
	DueSoonFraction       string `json:"due_soon_fraction"`
	DueSoonDays           int    `json:"due_soon_days"`
	Note                  string `json:"note,omitempty"`
}

// CreateFleetPolicy prepares a DRAFT policy (fleet.manage).
func (s *Service) CreateFleetPolicy(ctx context.Context, p auth.Principal, in FleetPolicyInput) (string, error) {
	from, err := time.Parse("2006-01-02", strings.TrimSpace(in.EffectiveFrom))
	if err != nil {
		return "", fmt.Errorf("%w: effective_from must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	one := decimal.NewFromInt(1)
	if !in.FuelVarianceTolerance.IsPositive() || !in.FuelVarianceTolerance.LessThan(one) || in.FuelVarianceMinLitres.IsNegative() ||
		!in.DueSoonFraction.IsPositive() || !in.DueSoonFraction.LessThan(one) || in.DueSoonDays < 0 || in.DueSoonDays > 365 || len(strings.TrimSpace(in.Basis)) < 10 {
		return "", fmt.Errorf("%w: a fleet policy states a fuel tolerance and a due-soon share between 0 and 1, a minimum of litres (0 or more), due-soon days (0 to 365) and its basis (10+ characters)", ErrInvalidInput)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermFleetManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "FPL")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO fleet_policies (number, effective_from, fuel_variance_tolerance, fuel_variance_min_litres, due_soon_fraction, due_soon_days, basis, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, num, from, in.FuelVarianceTolerance.Round(4), in.FuelVarianceMinLitres.Round(2), in.DueSoonFraction.Round(4), in.DueSoonDays, strings.TrimSpace(in.Basis), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.policy.create", "fleet_policy", id, "fleet.policy_prepared", map[string]any{"id": id, "number": num, "effective_from": in.EffectiveFrom})
	})
	return id, err
}

// SubmitFleetPolicy submits the preparer's draft.
func (s *Service) SubmitFleetPolicy(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermFleetManage, "fleet_policy", "fleet_policies", id, "fleet.policy.submit", "fleet.policy_submitted")
}

// DecideFleetPolicy approves or rejects (fleet.approve, fresh MFA, not the preparer). An approved policy applies from its effective date.
func (s *Service) DecideFleetPolicy(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermFleetApprove, "fleet_policy", "fleet_policies", id, "fleet.policy.decide", "fleet policy", approve, reason, nil)
}

// fleetPolicyAt returns the thresholds in force on a date and their provenance.
func fleetPolicyAt(ctx context.Context, tx pgx.Tx, on time.Time) (fleet.Thresholds, PolicyInForce, error) {
	var t fleet.Thresholds
	var src PolicyInForce
	err := tx.QueryRow(ctx, `SELECT id::text, number, effective_from::text, fuel_variance_tolerance, fuel_variance_min_litres, due_soon_fraction, due_soon_days FROM fleet_policy_at($1::date)`, on).
		Scan(&src.PolicyID, &src.Number, &src.EffectiveFrom, &t.FuelTolerance, &t.FuelMinLitres, &t.DueSoonFraction, &t.DueSoonDays)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		t = fleet.DefaultThresholds()
		src = PolicyInForce{Source: "ENGINEERING_DEFAULT", Note: "No fleet policy has been approved for this date. These thresholds are engineering defaults, not a decision of the company; a plant manager approves a fleet policy to replace them."}
	case err != nil:
		return t, src, err
	default:
		src.Source = "APPROVED_POLICY"
	}
	src.FuelVarianceTolerance, src.FuelVarianceMinLitres, src.DueSoonFraction, src.DueSoonDays = t.FuelTolerance.String(), t.FuelMinLitres.String(), t.DueSoonFraction.String(), t.DueSoonDays
	return t, src, nil
}

// FleetPolicyInForce returns the policy applied on a date (today when empty).
func (s *Service) FleetPolicyInForce(ctx context.Context, p auth.Principal, on string) (PolicyInForce, error) {
	day := time.Now().UTC()
	if strings.TrimSpace(on) != "" {
		var err error
		if day, err = time.Parse("2006-01-02", strings.TrimSpace(on)); err != nil {
			return PolicyInForce{}, fmt.Errorf("%w: on must be a date (YYYY-MM-DD)", ErrInvalidInput)
		}
	}
	var out PolicyInForce
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		_, out, err = fleetPolicyAt(ctx, tx, day)
		return err
	})
	return out, err
}
