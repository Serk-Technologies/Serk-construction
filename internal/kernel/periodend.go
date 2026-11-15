// Period-end (WP-2.13; SRS FIN-10, FIN-11, FIN-12, FIN-13). Migration:
// 0058_period_end.sql.
//
// Fiscal years are generated as monthly OPEN periods, on the Gregorian or the
// Ethiopian calendar (Hamle to Sene; Pagume, the short thirteenth month, is
// part of Nehase). Recurring journals create the month's DRAFT journal once
// per template and date; accruals are reversed on their date once posted;
// overhead is allocated to projects by posted direct cost. Every generated
// journal then follows the ordinary approval and posting.
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
	"github.com/serk-group/serk-construction/pkg/ledger"
	"github.com/serk-group/serk-construction/pkg/money"
)

// FiscalPeriodSpec is one generated period.
type FiscalPeriodSpec struct {
	Name  string `json:"name"`
	Start string `json:"starts_on"`
	End   string `json:"ends_on"`
}

func gregorianLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// meskerem1 is the Ethiopian new year (1 Meskerem) of Ethiopian year ec, in
// the Gregorian calendar: 11 September, or 12 September before a Gregorian
// leap year (valid 1901-2099).
func meskerem1(ec int) time.Time {
	g := ec + 7
	d := 11
	if gregorianLeap(g + 1) {
		d = 12
	}
	return time.Date(g, 9, d, 0, 0, 0, 0, time.UTC)
}

// EthiopianFiscalYear returns the twelve monthly periods of Ethiopian fiscal
// year efy, from 1 Hamle of year efy-1 to 30 Sene of year efy (Pagume is
// included in Nehase). EFY 2019 runs 8 July 2026 to 7 July 2027.
func EthiopianFiscalYear(efy int) []FiscalPeriodSpec {
	names := []string{"Hamle", "Nehase", "Meskerem", "Tikimt", "Hidar", "Tahsas", "Tir", "Yekatit", "Megabit", "Miyazya", "Ginbot", "Sene"}
	start := meskerem1(efy-1).AddDate(0, 0, 300) // 1 Hamle
	next := meskerem1(efy)
	out := make([]FiscalPeriodSpec, 0, 12)
	add := func(i int, s, e time.Time) {
		out = append(out, FiscalPeriodSpec{Name: fmt.Sprintf("EFY%d-%02d %s", efy, i+1, names[i]), Start: s.Format("2006-01-02"), End: e.Format("2006-01-02")})
	}
	add(0, start, start.AddDate(0, 0, 29))
	add(1, start.AddDate(0, 0, 30), next.AddDate(0, 0, -1))
	for m := 0; m < 10; m++ {
		s := next.AddDate(0, 0, 30*m)
		add(m+2, s, s.AddDate(0, 0, 29))
	}
	return out
}

// GregorianFiscalYear returns the twelve calendar months of year y.
func GregorianFiscalYear(y int) []FiscalPeriodSpec {
	out := make([]FiscalPeriodSpec, 0, 12)
	for m := 1; m <= 12; m++ {
		s := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
		out = append(out, FiscalPeriodSpec{Name: fmt.Sprintf("FY%d-%02d", y, m), Start: s.Format("2006-01-02"), End: s.AddDate(0, 1, -1).Format("2006-01-02")})
	}
	return out
}

// GenerateFiscalYear creates a year of OPEN monthly periods for an entity.
func (s *Service) GenerateFiscalYear(ctx context.Context, p auth.Principal, entity, calendar string, year int) ([]FiscalPeriodSpec, error) {
	var specs []FiscalPeriodSpec
	switch strings.ToUpper(calendar) {
	case "GREGORIAN":
		specs = GregorianFiscalYear(year)
	case "ETHIOPIAN":
		specs = EthiopianFiscalYear(year)
	default:
		return nil, fmt.Errorf("%w: calendar must be GREGORIAN or ETHIOPIAN", ErrInvalidInput)
	}
	if year < 1950 || year > 2150 {
		return nil, fmt.Errorf("%w: year out of range", ErrInvalidInput)
	}
	if !s.tenantWide(p) {
		return nil, auth.ErrForbidden
	}
	err := s.p2pTx(ctx, p, auth.PermPeriodClose, func(ctx context.Context, tx pgx.Tx) error {
		var overlap string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(string_agg(name, ', ' ORDER BY starts_on), '') FROM fiscal_periods WHERE legal_entity_id = $1::uuid AND daterange(starts_on, ends_on, '[]') && daterange($2::date, $3::date, '[]')`,
			entity, specs[0].Start, specs[len(specs)-1].End).Scan(&overlap); err != nil {
			return err
		}
		if overlap != "" {
			return fmt.Errorf("%w: the year overlaps existing periods: %s", ErrInvalidInput, overlap)
		}
		for _, sp := range specs {
			if _, err := tx.Exec(ctx, `INSERT INTO fiscal_periods (tenant_id, legal_entity_id, name, starts_on, ends_on) VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, p.TenantID, entity, sp.Name, sp.Start, sp.End); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "period.generate", "legal_entity", entity, "fiscal_year.generated", map[string]any{"calendar": calendar, "year": year, "periods": len(specs)})
	})
	return specs, err
}

// FiscalPeriods lists an entity's periods.
func (s *Service) FiscalPeriods(ctx context.Context, p auth.Principal, entity string) ([]map[string]any, error) {
	out := []map[string]any{}
	if !s.authz.Has(p, auth.PermJournalRead) && !s.authz.Has(p, auth.PermPeriodClose) {
		return out, auth.ErrForbidden
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (SELECT id, name, starts_on, ends_on, status, closed_by FROM fiscal_periods WHERE legal_entity_id = $1::uuid ORDER BY starts_on) r`, entity)
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
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// JournalLineInput is a line of a recurring journal or an accrual.
type JournalLineInput struct {
	AccountID  string          `json:"account_id"`
	Side       string          `json:"side"`
	Amount     decimal.Decimal `json:"amount"`
	CostCodeID string          `json:"cost_code_id,omitempty"`
	Memo       string          `json:"memo,omitempty"`
}

// RecurringInput defines a recurring journal.
type RecurringInput struct {
	LegalEntityID string             `json:"legal_entity_id"`
	Name          string             `json:"name"`
	ProjectID     string             `json:"project_id,omitempty"`
	FirstRunOn    string             `json:"first_run_on"`
	EndsOn        string             `json:"ends_on,omitempty"`
	Lines         []JournalLineInput `json:"lines"`
}

func balanced(lines []JournalLineInput) error {
	if len(lines) < 2 {
		return fmt.Errorf("%w: a journal needs at least two lines", ErrInvalidInput)
	}
	var dr, cr decimal.Decimal
	for _, l := range lines {
		switch strings.ToUpper(l.Side) {
		case "DEBIT":
			dr = dr.Add(l.Amount)
		case "CREDIT":
			cr = cr.Add(l.Amount)
		default:
			return fmt.Errorf("%w: side must be DEBIT or CREDIT", ErrInvalidInput)
		}
		if !l.Amount.IsPositive() {
			return fmt.Errorf("%w: every amount must be positive", ErrInvalidInput)
		}
	}
	if !dr.Equal(cr) {
		return fmt.Errorf("%w: debits (%s) and credits (%s) must be equal", ErrInvalidInput, dr, cr)
	}
	return nil
}

func (s *Service) journalOf(ctx context.Context, p auth.Principal, entity, project, desc, source, ref, key string, at time.Time, lines []JournalLineInput) (ledger.Journal, error) {
	var ccy string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT functional_currency FROM legal_entities WHERE id = $1::uuid`, entity).Scan(&ccy)
	}); err != nil {
		return ledger.Journal{}, mapNoRows(err)
	}
	j := ledger.Journal{LegalEntityID: entity, ProjectID: project, FunctionalCurrency: money.Currency(ccy), BusinessEventAt: at, Description: desc, SourceType: source, SourceRef: ref, IdempotencyKey: key}
	for i, l := range lines {
		amt, err := money.New(l.Amount, money.Currency(ccy), money.DefaultRounding)
		if err != nil {
			return j, err
		}
		j.Lines = append(j.Lines, ledger.Line{LineNo: i + 1, AccountID: l.AccountID, Side: ledger.Side(strings.ToUpper(l.Side)), Amount: amt, ProjectID: project, CostCodeID: l.CostCodeID, Memo: l.Memo})
	}
	return j, nil
}

// CreateRecurring defines a recurring journal (journal.create).
func (s *Service) CreateRecurring(ctx context.Context, p auth.Principal, in RecurringInput) (string, error) {
	first, err := parseDay("first_run_on", in.FirstRunOn)
	if err != nil {
		return "", err
	}
	if first.Day() > 28 {
		return "", fmt.Errorf("%w: choose a day of the month from 1 to 28", ErrInvalidInput)
	}
	if err := balanced(in.Lines); err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermJournalCreate, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "REC")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO recurring_journals (legal_entity_id, number, name, project_id, day_of_month, next_run_on, ends_on, created_by)
			VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, $5, $6, NULLIF($7, '')::date, $8) RETURNING id::text`,
			in.LegalEntityID, num, strings.TrimSpace(in.Name), in.ProjectID, first.Day(), first, strings.TrimSpace(in.EndsOn), p.UserID)
		if err != nil {
			return err
		}
		for i, l := range in.Lines {
			if _, err := tx.Exec(ctx, `INSERT INTO recurring_journal_lines (template_id, line_no, account_id, side, amount, cost_code_id, memo) VALUES ($1::uuid, $2, $3::uuid, $4, $5, NULLIF($6, '')::uuid, NULLIF($7, ''))`,
				id, i+1, l.AccountID, strings.ToUpper(l.Side), l.Amount, l.CostCodeID, l.Memo); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "recurring.create", "recurring_journal", id, "recurring_journal.created", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// RunRecurring creates the DRAFT journals of every active template due on or
// before asOf (catching up at most 24 months per template), once per date.
func (s *Service) RunRecurring(ctx context.Context, p auth.Principal, asOf string) ([]string, error) {
	until, err := parseDay("as_of", asOf)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Require(p, auth.PermJournalCreate, p.TenantID); err != nil {
		return nil, err
	}
	type tpl struct {
		id, entity, project, name, number string
		next                              time.Time
		ends                              *time.Time
		lines                             []JournalLineInput
	}
	var due []tpl
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, legal_entity_id::text, COALESCE(project_id::text, ''), name, number, next_run_on, ends_on FROM recurring_journals
			WHERE status = 'ACTIVE' AND next_run_on <= $1 ORDER BY number`, until)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t tpl
			if err := rows.Scan(&t.id, &t.entity, &t.project, &t.name, &t.number, &t.next, &t.ends); err != nil {
				rows.Close()
				return err
			}
			due = append(due, t)
		}
		rows.Close()
		for i := range due {
			lr, err := tx.Query(ctx, `SELECT account_id::text, side, amount, COALESCE(cost_code_id::text, ''), COALESCE(memo, '') FROM recurring_journal_lines WHERE template_id = $1::uuid ORDER BY line_no`, due[i].id)
			if err != nil {
				return err
			}
			for lr.Next() {
				var l JournalLineInput
				if err := lr.Scan(&l.AccountID, &l.Side, &l.Amount, &l.CostCodeID, &l.Memo); err != nil {
					lr.Close()
					return err
				}
				due[i].lines = append(due[i].lines, l)
			}
			lr.Close()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	created := []string{}
	for _, t := range due {
		for n := 0; n < 24 && !t.next.After(until); n++ {
			if t.ends != nil && t.next.After(*t.ends) {
				break
			}
			if err := s.authz.RequireProject(p, auth.PermJournalCreate, p.TenantID, t.project); err != nil {
				return created, err
			}
			j, err := s.journalOf(ctx, p, t.entity, t.project, t.name+" ("+t.number+")", "RECURRING", t.id, "recurring:"+t.id+":"+t.next.Format("2006-01-02"), t.next, t.lines)
			if err != nil {
				return created, err
			}
			res, err := s.CreateJournal(ctx, p, j)
			if err != nil {
				return created, err
			}
			next := t.next.AddDate(0, 1, 0)
			if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO recurring_journal_runs (template_id, run_on, journal_id, created_by) VALUES ($1::uuid, $2, $3::uuid, $4) ON CONFLICT DO NOTHING`, t.id, t.next, res.JournalID, p.UserID); err != nil {
					return err
				}
				status := "ACTIVE"
				if t.ends != nil && next.After(*t.ends) {
					status = "ENDED"
				}
				return postgres.Exec1(ctx, tx, `UPDATE recurring_journals SET next_run_on = $2, status = $3 WHERE id = $1::uuid`, t.id, next, status)
			}); err != nil {
				return created, err
			}
			created = append(created, res.JournalID)
			t.next = next
		}
	}
	return created, nil
}

// AccrualInput records an accrual and its reversal date.
type AccrualInput struct {
	LegalEntityID string             `json:"legal_entity_id"`
	ProjectID     string             `json:"project_id,omitempty"`
	Description   string             `json:"description"`
	Date          string             `json:"date"`
	ReverseOn     string             `json:"reverse_on"`
	Lines         []JournalLineInput `json:"lines"`
}

// CreateAccrual creates a DRAFT ACCRUAL journal that will reverse on ReverseOn.
func (s *Service) CreateAccrual(ctx context.Context, p auth.Principal, in AccrualInput) (string, error) {
	at, err := parseDay("date", in.Date)
	if err != nil {
		return "", err
	}
	rev, err := parseDay("reverse_on", in.ReverseOn)
	if err != nil {
		return "", err
	}
	if !rev.After(at) {
		return "", fmt.Errorf("%w: an accrual reverses after its own date", ErrInvalidInput)
	}
	if err := balanced(in.Lines); err != nil {
		return "", err
	}
	key := fmt.Sprintf("accrual:%s:%s:%d", p.UserID, at.Format("2006-01-02"), time.Now().UnixNano())
	j, err := s.journalOf(ctx, p, in.LegalEntityID, in.ProjectID, "Accrual: "+strings.TrimSpace(in.Description), "ACCRUAL", "", key, at, in.Lines)
	if err != nil {
		return "", err
	}
	res, err := s.CreateJournal(ctx, p, j)
	if err != nil {
		return "", err
	}
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO accruals (journal_id, legal_entity_id, reverse_on, created_by) VALUES ($1::uuid, $2::uuid, $3, $4)`, res.JournalID, in.LegalEntityID, rev, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "accrual.create", "journal", res.JournalID, "accrual.created", map[string]any{"journal_id": res.JournalID, "reverse_on": in.ReverseOn})
	})
	return res.JournalID, err
}

// RunAccrualReversals creates the reversal of every POSTED accrual due on or
// before asOf (journal.reverse), once.
func (s *Service) RunAccrualReversals(ctx context.Context, p auth.Principal, asOf string) ([]string, error) {
	until, err := parseDay("as_of", asOf)
	if err != nil {
		return nil, err
	}
	type due struct {
		journal string
		on      time.Time
	}
	var list []due
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.journal_id::text, a.reverse_on FROM accruals a JOIN journals j ON j.id = a.journal_id
			WHERE a.reversal_journal_id IS NULL AND j.status = 'POSTED' AND a.reverse_on <= $1 ORDER BY a.reverse_on`, until)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.journal, &d.on); err != nil {
				return err
			}
			list = append(list, d)
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	out := []string{}
	for _, d := range list {
		res, err := s.ReverseJournal(ctx, p, d.journal, "accrual-reversal:"+d.journal, "automatic reversal of an accrual", d.on)
		if err != nil {
			return out, err
		}
		if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
			return postgres.Exec1(ctx, tx, `UPDATE accruals SET reversal_journal_id = $2::uuid WHERE journal_id = $1::uuid AND reversal_journal_id IS NULL`, d.journal, res.JournalID)
		}); err != nil {
			return out, err
		}
		out = append(out, res.JournalID)
	}
	return out, nil
}

// OverheadInput allocates an overhead pool over a date range.
type OverheadInput struct {
	LegalEntityID string `json:"legal_entity_id"`
	From          string `json:"from"`
	To            string `json:"to"`
	PoolAccount   string `json:"pool_account_id"`
	TargetAccount string `json:"target_account_id"`
}

// OverheadShare is one project's share.
type OverheadShare struct {
	ProjectID string          `json:"project_id"`
	Project   string          `json:"project"`
	Driver    decimal.Decimal `json:"direct_cost"`
	Share     decimal.Decimal `json:"share"`
	JournalID string          `json:"journal_id,omitempty"`
}

func overheadShares(ctx context.Context, tx pgx.Tx, in OverheadInput) ([]OverheadShare, error) {
	rows, err := tx.Query(ctx, `SELECT c.project_id::text, pr.code || ' ' || pr.name, c.driver, c.share FROM overhead_compute($1::uuid, $2::date, $3::date, $4::uuid, $5::uuid) c
		JOIN projects pr ON pr.id = c.project_id ORDER BY pr.code`, in.LegalEntityID, in.From, in.To, in.PoolAccount, in.TargetAccount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OverheadShare{}
	for rows.Next() {
		var x OverheadShare
		if err := rows.Scan(&x.ProjectID, &x.Project, &x.Driver, &x.Share); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// PreviewOverhead computes the shares without saving.
func (s *Service) PreviewOverhead(ctx context.Context, p auth.Principal, in OverheadInput) ([]OverheadShare, error) {
	var out []OverheadShare
	err := s.assetTx(ctx, p, auth.PermJournalRead, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = overheadShares(ctx, tx, in)
		return err
	})
	return out, err
}

// AllocateOverhead records the allocation and creates one DRAFT journal per project.
func (s *Service) AllocateOverhead(ctx context.Context, p auth.Principal, in OverheadInput) (string, error) {
	to, err := parseDay("to", in.To)
	if err != nil {
		return "", err
	}
	if !s.tenantWide(p) {
		return "", auth.ErrForbidden
	}
	if err := s.authz.Require(p, auth.PermJournalCreate, p.TenantID); err != nil {
		return "", err
	}
	var id string
	var shares []OverheadShare
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if shares, err = overheadShares(ctx, tx, in); err != nil {
			return err
		}
		if len(shares) == 0 {
			return fmt.Errorf("%w: nothing to allocate: the pool has no posted balance, or no project has posted direct cost, in the range", ErrInvalidInput)
		}
		total := decimal.Zero
		for _, x := range shares {
			total = total.Add(x.Share)
		}
		num, err := postgres.NextNumber(ctx, tx, "OVH")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO overhead_runs (legal_entity_id, number, period_from, period_to, pool_account_id, target_account_id, amount, created_by)
			VALUES ($1::uuid, $2, $3::date, $4::date, $5::uuid, $6::uuid, $7, $8) RETURNING id::text`, in.LegalEntityID, num, in.From, in.To, in.PoolAccount, in.TargetAccount, total, p.UserID)
		if err != nil {
			return err
		}
		for _, x := range shares {
			if _, err := tx.Exec(ctx, `INSERT INTO overhead_lines (run_id, project_id, driver, share) VALUES ($1::uuid, $2::uuid, $3, $4)`, id, x.ProjectID, x.Driver, x.Share); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "overhead.allocate", "overhead_run", id, "overhead.allocated", map[string]any{"id": id, "amount": total.String()})
	})
	if err != nil {
		return "", err
	}
	for _, x := range shares {
		j, err := s.journalOf(ctx, p, in.LegalEntityID, x.ProjectID, "Overhead allocation "+in.From+" to "+in.To, "OVERHEAD", id, "overhead:"+id+":"+x.ProjectID, to,
			[]JournalLineInput{{AccountID: in.TargetAccount, Side: "DEBIT", Amount: x.Share}, {AccountID: in.PoolAccount, Side: "CREDIT", Amount: x.Share}})
		if err != nil {
			return id, err
		}
		res, err := s.CreateJournal(ctx, p, j)
		if err != nil {
			return id, err
		}
		if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
			return postgres.Exec1(ctx, tx, `UPDATE overhead_lines SET journal_id = $3::uuid WHERE run_id = $1::uuid AND project_id = $2::uuid`, id, x.ProjectID, res.JournalID)
		}); err != nil {
			return id, err
		}
	}
	return id, nil
}

// PeriodEndView lists recurring journals, accruals and overhead runs.
func (s *Service) PeriodEndView(ctx context.Context, p auth.Principal) (map[string]any, error) {
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
		if err := q("recurring", `SELECT r.id, r.number, r.name, r.status, r.next_run_on, r.ends_on, (SELECT code FROM projects WHERE id = r.project_id) AS project,
			(SELECT sum(amount) FROM recurring_journal_lines l WHERE l.template_id = r.id AND l.side = 'DEBIT') AS amount, (SELECT count(*) FROM recurring_journal_runs x WHERE x.template_id = r.id) AS runs
			FROM recurring_journals r ORDER BY r.number`); err != nil {
			return err
		}
		if err := q("accruals", `SELECT a.journal_id, j.description, (j.business_event_at AT TIME ZONE 'UTC')::date AS date, a.reverse_on, j.status AS journal_status,
			(SELECT sum(amount) FROM journal_lines l WHERE l.journal_id = j.id AND l.side = 'DEBIT') AS amount,
			a.reversal_journal_id, (SELECT status FROM journals WHERE id = a.reversal_journal_id) AS reversal_status
			FROM accruals a JOIN journals j ON j.id = a.journal_id ORDER BY a.reverse_on DESC`); err != nil {
			return err
		}
		return q("overhead", `SELECT o.id, o.number, o.period_from, o.period_to, o.amount, (SELECT code || ' ' || name FROM accounts WHERE id = o.pool_account_id) AS pool,
			(SELECT count(*) FROM overhead_lines l WHERE l.run_id = o.id) AS projects FROM overhead_runs o ORDER BY o.created_at DESC`)
	})
	return out, err
}
