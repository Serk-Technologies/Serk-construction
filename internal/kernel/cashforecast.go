// Cash forecasting (WP-2.12; SRS TRS-02). Migration: 0059_cash_plan.sql.
//
// A 13-week forecast for a legal entity, by week from the Monday of the
// current week. It starts from the posted bank cash and adds, with the date
// each is expected and whether it is CONFIRMED (a document fixes the amount
// and date) or FORECAST (expected but not yet invoiced):
//
//	in,  confirmed  approved customer invoices still open, by due date
//	in,  forecast   certified client certificates not yet invoiced (period end + 30 days)
//	out, confirmed  approved supplier invoices still open, by due date;
//	                released payments not yet settled, by value date
//	out, forecast   certified subcontract certificates not yet paid (period + 30 days);
//	                open purchase-order balances (date needed, or 30 days after approval)
//	either          planned items entered by treasury (loans, tax, payroll, capital spend)
//
// Anything already overdue is shown in the first week.
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

// CashItem is one expected cash movement.
type CashItem struct {
	Date       string          `json:"date"`
	Direction  string          `json:"direction"`  // IN | OUT
	Confidence string          `json:"confidence"` // CONFIRMED | FORECAST
	Source     string          `json:"source"`
	Label      string          `json:"label"`
	Amount     decimal.Decimal `json:"amount"`
	Href       string          `json:"href,omitempty"`
	Week       int             `json:"week"` // 0-based; -1 beyond the horizon
}

// CashWeek is one week of the forecast.
type CashWeek struct {
	Start        string          `json:"start"`
	End          string          `json:"end"`
	InConfirmed  decimal.Decimal `json:"in_confirmed"`
	InForecast   decimal.Decimal `json:"in_forecast"`
	OutConfirmed decimal.Decimal `json:"out_confirmed"`
	OutForecast  decimal.Decimal `json:"out_forecast"`
	Net          decimal.Decimal `json:"net"`
	Closing      decimal.Decimal `json:"closing"`
}

// CashForecast is the entity's 13-week view.
type CashForecast struct {
	EntityID   string           `json:"entity_id"`
	Currency   string           `json:"currency"`
	AsOf       string           `json:"as_of"`
	Opening    decimal.Decimal  `json:"opening_cash"`
	Weeks      []CashWeek       `json:"weeks"`
	Items      []CashItem       `json:"items"`
	Lowest     decimal.Decimal  `json:"lowest_closing"`
	LowestWeek int              `json:"lowest_week"`
	Later      decimal.Decimal  `json:"net_after_horizon"`
	Plan       []map[string]any `json:"plan"`
}

const cashWeeks = 13

// CashForecastOf builds the forecast (tenant-wide readers of intelligence or treasury).
func (s *Service) CashForecastOf(ctx context.Context, p auth.Principal, entity string) (CashForecast, error) {
	out := CashForecast{EntityID: entity, Weeks: []CashWeek{}, Items: []CashItem{}, Plan: []map[string]any{}}
	if !s.tenantWide(p) || (!s.authz.Has(p, auth.PermIntelligenceRead) && !s.authz.Has(p, auth.PermPaymentPrepare)) {
		return out, auth.ErrForbidden
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		now, err := postgres.DBNow(ctx, tx)
		if err != nil {
			return err
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		start := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7)) // Monday
		out.AsOf = today.Format("2006-01-02")
		if err := tx.QueryRow(ctx, `SELECT functional_currency FROM legal_entities WHERE id = $1::uuid`, entity).Scan(&out.Currency); err != nil {
			return mapNoRows(err)
		}
		cash, _, err := postgres.BankCash(ctx, tx, entity)
		if err != nil {
			return err
		}
		out.Opening = cash
		add := func(date time.Time, dir, conf, src, label, href string, amt decimal.Decimal) {
			if !amt.IsPositive() {
				return
			}
			w := int(date.Sub(start).Hours() / 24 / 7)
			if date.Before(start) {
				w = 0
			}
			if w >= cashWeeks {
				w = -1
			}
			out.Items = append(out.Items, CashItem{Date: date.Format("2006-01-02"), Direction: dir, Confidence: conf, Source: src, Label: label, Amount: amt, Href: href, Week: w})
		}
		ar, err := postgres.ReceivablesOpen(ctx, tx, entity, today)
		if err != nil {
			return err
		}
		for _, it := range ar {
			add(it.DueDate, "IN", "CONFIRMED", "Customer invoice", it.Document+" — "+it.Party, "#/aging/ar", it.Open)
		}
		ap, err := postgres.PayablesOpen(ctx, tx, entity, today)
		if err != nil {
			return err
		}
		for _, it := range ap {
			add(it.DueDate, "OUT", "CONFIRMED", "Supplier invoice", it.Document+" — "+it.Party, "#/aging/ap", it.Open)
		}
		type row struct {
			id, label string
			date      time.Time
			amt       decimal.Decimal
		}
		query := func(sql string) ([]row, error) {
			rows, err := tx.Query(ctx, sql, entity)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var list []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.id, &r.label, &r.date, &r.amt); err != nil {
					return nil, err
				}
				list = append(list, r)
			}
			return list, rows.Err()
		}
		sources := []struct {
			dir, conf, src, href, sql string
		}{
			{"OUT", "CONFIRMED", "Payment released, not yet settled", "#/doc/payment_instruction/",
				`SELECT p.id::text, p.number || ' — ' || s.name, p.value_date, p.amount FROM payment_instructions p JOIN suppliers s ON s.id = p.supplier_id
				 WHERE p.legal_entity_id = $1::uuid AND p.status = 'RELEASED'`},
			{"OUT", "CONFIRMED", "Subcontract payment released", "#/doc/subcontract_payment/",
				`SELECT p.id::text, p.number, p.value_date, p.amount FROM subcontract_payments p WHERE p.legal_entity_id = $1::uuid AND p.status = 'RELEASED' AND p.value_date >= current_date - 7`},
			{"IN", "FORECAST", "Client certificate not yet invoiced", "#/doc/ipc/",
				`SELECT i.id::text, i.number || ' — ' || c.number, i.period_end + 30, i.amount_due FROM ipcs i JOIN contracts c ON c.id = i.contract_id
				 WHERE c.legal_entity_id = $1::uuid AND i.status = 'CERTIFIED'`},
			{"OUT", "FORECAST", "Subcontract certificate not yet paid", "#/doc/subcontract_application/",
				`SELECT a.id::text, a.number || ' — ' || s.number, a.period_to + 30, subcontract_certificate_open(a.id, NULL) FROM subcontract_applications a JOIN subcontracts s ON s.id = a.subcontract_id
				 WHERE s.legal_entity_id = $1::uuid AND a.status = 'CERTIFIED'`},
			{"OUT", "FORECAST", "Purchase order not yet invoiced", "#/doc/purchase_order/",
				`SELECT o.id::text, o.number || ' — ' || s.name, COALESCE(r.needed_by, (o.created_at AT TIME ZONE 'UTC')::date + 30),
				        o.total - COALESCE((SELECT sum(i.total) FROM supplier_invoices i WHERE i.po_id = o.id AND i.status IN ('SUBMITTED', 'APPROVED')), 0)
				   FROM purchase_orders o JOIN suppliers s ON s.id = o.supplier_id LEFT JOIN rfqs q ON q.id = o.rfq_id LEFT JOIN purchase_requisitions r ON r.id = q.pr_id
				  WHERE o.legal_entity_id = $1::uuid AND o.status = 'APPROVED'`},
		}
		for _, src := range sources {
			list, err := query(src.sql)
			if err != nil {
				return fmt.Errorf("%s: %w", src.src, err)
			}
			for _, r := range list {
				add(r.date, src.dir, src.conf, src.src, r.label, src.href+r.id, r.amt)
			}
		}
		rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (SELECT id, due_on, direction, category, amount, description, confidence, status, created_by
			FROM cash_plan_items WHERE legal_entity_id = $1::uuid ORDER BY due_on) r`, entity)
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
			out.Plan = append(out.Plan, m)
			if m["status"] != "ACTIVE" {
				continue
			}
			d, _ := time.Parse("2006-01-02", fmt.Sprint(m["due_on"]))
			amt, _ := decimal.NewFromString(fmt.Sprint(m["amount"]))
			add(d, fmt.Sprint(m["direction"]), fmt.Sprint(m["confidence"]), "Planned: "+strings.ToLower(fmt.Sprint(m["category"])), fmt.Sprint(m["description"]), "", amt)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	// Weeks and the running balance.
	monday, _ := time.Parse("2006-01-02", out.AsOf)
	monday = monday.AddDate(0, 0, -((int(monday.Weekday()) + 6) % 7))
	for i := 0; i < cashWeeks; i++ {
		ws := monday.AddDate(0, 0, 7*i)
		out.Weeks = append(out.Weeks, CashWeek{Start: ws.Format("2006-01-02"), End: ws.AddDate(0, 0, 6).Format("2006-01-02")})
	}
	for _, it := range out.Items {
		if it.Week < 0 {
			if it.Direction == "IN" {
				out.Later = out.Later.Add(it.Amount)
			} else {
				out.Later = out.Later.Sub(it.Amount)
			}
			continue
		}
		w := &out.Weeks[it.Week]
		switch {
		case it.Direction == "IN" && it.Confidence == "CONFIRMED":
			w.InConfirmed = w.InConfirmed.Add(it.Amount)
		case it.Direction == "IN":
			w.InForecast = w.InForecast.Add(it.Amount)
		case it.Confidence == "CONFIRMED":
			w.OutConfirmed = w.OutConfirmed.Add(it.Amount)
		default:
			w.OutForecast = w.OutForecast.Add(it.Amount)
		}
	}
	bal := out.Opening
	for i := range out.Weeks {
		w := &out.Weeks[i]
		w.Net = w.InConfirmed.Add(w.InForecast).Sub(w.OutConfirmed).Sub(w.OutForecast)
		bal = bal.Add(w.Net)
		w.Closing = bal
		if i == 0 || bal.LessThan(out.Lowest) {
			out.Lowest, out.LowestWeek = bal, i
		}
	}
	return out, nil
}

// CashPlanInput is a planned cash item.
type CashPlanInput struct {
	LegalEntityID string          `json:"legal_entity_id"`
	DueOn         string          `json:"due_on"`
	Direction     string          `json:"direction"`
	Category      string          `json:"category"`
	Amount        decimal.Decimal `json:"amount"`
	Description   string          `json:"description"`
	Confidence    string          `json:"confidence"`
}

// AddCashPlanItem records a planned item (treasury: payment.prepare).
func (s *Service) AddCashPlanItem(ctx context.Context, p auth.Principal, in CashPlanInput) (string, error) {
	due, err := parseDay("due_on", in.DueOn)
	if err != nil {
		return "", err
	}
	if in.Confidence == "" {
		in.Confidence = "FORECAST"
	}
	if !s.tenantWide(p) {
		return "", auth.ErrForbidden
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermPaymentPrepare, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO cash_plan_items (legal_entity_id, due_on, direction, category, amount, description, confidence, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, in.LegalEntityID, due, strings.ToUpper(in.Direction), strings.ToUpper(in.Category), in.Amount,
			strings.TrimSpace(in.Description), strings.ToUpper(in.Confidence), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "cashplan.create", "cash_plan_item", id, "cash_plan_item.added", map[string]any{"id": id, "amount": in.Amount.String(), "direction": in.Direction})
	})
	return id, err
}

// CancelCashPlanItem cancels a planned item with a reason.
func (s *Service) CancelCashPlanItem(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	if !s.tenantWide(p) {
		return auth.ErrForbidden
	}
	return s.p2pTx(ctx, p, auth.PermPaymentPrepare, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE cash_plan_items SET status = 'CANCELLED', cancel_reason = $2 WHERE id = $1::uuid AND status = 'ACTIVE'`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "cashplan.cancel", "cash_plan_item", id, "cash_plan_item.cancelled", map[string]any{"id": id, "reason": reason})
	})
}
