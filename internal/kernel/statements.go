package kernel

// Financial statements (CAP-01.5) derived from posted journals through the
// trial balance: an income statement for a period and a balance sheet at its
// end. Every line is an account, so each figure drills down to the account's
// journal lines; the balance sheet must balance, and says so.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ReportLine is one account on a financial statement; Amount is shown in the
// account's natural sign (debit for assets and expenses, credit otherwise).
type ReportLine struct {
	AccountCode string          `json:"account_code"`
	AccountName string          `json:"account_name"`
	Amount      decimal.Decimal `json:"amount"`
}

// FinancialStatements is an income statement for [From, To] and a balance sheet at To.
type FinancialStatements struct {
	EntityID string `json:"entity_id"`
	From     string `json:"from"`
	To       string `json:"to"`

	Revenue      []ReportLine    `json:"revenue"`
	Expenses     []ReportLine    `json:"expenses"`
	TotalRevenue decimal.Decimal `json:"total_revenue"`
	TotalExpense decimal.Decimal `json:"total_expenses"`
	NetProfit    decimal.Decimal `json:"net_profit"`

	Assets           []ReportLine    `json:"assets"`
	Liabilities      []ReportLine    `json:"liabilities"`
	Equity           []ReportLine    `json:"equity"`
	TotalAssets      decimal.Decimal `json:"total_assets"`
	TotalLiabilities decimal.Decimal `json:"total_liabilities"`
	TotalEquity      decimal.Decimal `json:"total_equity"`
	// UnclosedEarnings is the cumulative balance of revenue and expense
	// accounts at To (profit not yet closed to equity).
	UnclosedEarnings decimal.Decimal `json:"unclosed_earnings"`
	Balanced         bool            `json:"balanced"`
	Difference       decimal.Decimal `json:"difference"` // assets - (liabilities + equity + unclosed earnings)
}

// Statements returns the income statement of an entity for [from, to] and its
// balance sheet at to, from posted (and reversed) journals only.
func (s *Service) Statements(ctx context.Context, p auth.Principal, entityID string, from, to time.Time) (FinancialStatements, error) {
	out := FinancialStatements{EntityID: entityID, From: from.Format("2006-01-02"), To: to.Format("2006-01-02")}
	if to.Before(from) {
		return out, fmt.Errorf("%w: the period ends before it starts", ErrInvalidInput)
	}
	if err := s.reportScope(p, auth.PermJournalRead); err != nil {
		return out, err
	}
	var before, end []postgres.TrialBalanceRow
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if before, err = postgres.TrialBalance(ctx, tx, entityID, from.AddDate(0, 0, -1)); err != nil {
			return err
		}
		end, err = postgres.TrialBalance(ctx, tx, entityID, to)
		return err
	})
	if err != nil {
		return out, err
	}
	prior := map[string]decimal.Decimal{}
	for _, r := range before {
		prior[r.AccountCode] = r.Balance
	}
	for _, r := range end {
		bal := r.Balance // debit - credit, cumulative to `to`
		switch r.AccountType {
		case "REVENUE":
			if mv := bal.Sub(prior[r.AccountCode]).Neg(); !mv.IsZero() {
				out.Revenue = append(out.Revenue, ReportLine{r.AccountCode, r.AccountName, mv})
				out.TotalRevenue = out.TotalRevenue.Add(mv)
			}
			out.UnclosedEarnings = out.UnclosedEarnings.Sub(bal)
		case "EXPENSE":
			if mv := bal.Sub(prior[r.AccountCode]); !mv.IsZero() {
				out.Expenses = append(out.Expenses, ReportLine{r.AccountCode, r.AccountName, mv})
				out.TotalExpense = out.TotalExpense.Add(mv)
			}
			out.UnclosedEarnings = out.UnclosedEarnings.Sub(bal)
		case "ASSET":
			if !bal.IsZero() {
				out.Assets = append(out.Assets, ReportLine{r.AccountCode, r.AccountName, bal})
				out.TotalAssets = out.TotalAssets.Add(bal)
			}
		case "LIABILITY":
			if !bal.IsZero() {
				out.Liabilities = append(out.Liabilities, ReportLine{r.AccountCode, r.AccountName, bal.Neg()})
				out.TotalLiabilities = out.TotalLiabilities.Add(bal.Neg())
			}
		case "EQUITY":
			if !bal.IsZero() {
				out.Equity = append(out.Equity, ReportLine{r.AccountCode, r.AccountName, bal.Neg()})
				out.TotalEquity = out.TotalEquity.Add(bal.Neg())
			}
		default:
			return out, fmt.Errorf("kernel: account %s has an unknown type %q", r.AccountCode, r.AccountType)
		}
	}
	out.NetProfit = out.TotalRevenue.Sub(out.TotalExpense)
	out.Difference = out.TotalAssets.Sub(out.TotalLiabilities.Add(out.TotalEquity).Add(out.UnclosedEarnings))
	out.Balanced = out.Difference.IsZero()
	return out, nil
}
