package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Procure-to-pay data access (migration 0013). The database computes every
// amount, budget position and match result and enforces the state machines;
// these functions insert drafts, request transitions and read results.

// P2PPolicy is an entity's procure-to-pay policy.
type P2PPolicy struct {
	ID                   string          `json:"id"`
	LegalEntityID        string          `json:"legal_entity_id"`
	Version              int             `json:"version"`
	Currency             string          `json:"currency"`
	PriceTolerancePct    decimal.Decimal `json:"price_tolerance_pct"`
	OverReceiptPct       decimal.Decimal `json:"over_receipt_pct"`
	VATRate              decimal.Decimal `json:"vat_rate"`
	HighValuePOThreshold decimal.Decimal `json:"high_value_po_threshold"`
	DualPaymentThreshold decimal.Decimal `json:"dual_payment_threshold"`
	// DualPaymentWindowDays: payments to one supplier whose value dates lie
	// within this many days count together against the threshold (0016).
	// 0 = the database default (30).
	DualPaymentWindowDays int    `json:"dual_payment_window_days,omitempty"`
	GRNIAccountID         string `json:"grni_account_id"`
	APAccountID           string `json:"ap_account_id"`
	VATInputAccountID     string `json:"vat_input_account_id"`
	PPVAccountID          string `json:"ppv_account_id"`
	Status                string `json:"status"`
	CreatedBy             string `json:"created_by"`
	ApprovedBy            string `json:"approved_by,omitempty"`
}

// Doc is the common header of a P2P document as returned to callers.
type Doc struct {
	ID        string          `json:"id"`
	Number    string          `json:"number,omitempty"`
	Status    string          `json:"status"`
	CreatedBy string          `json:"created_by"`
	Detail    json.RawMessage `json:"detail"` // the row (and children) as stored, from the database
}

// P2PTable maps a document type to its table and creator column.
var p2pTables = map[string]struct{ table, creator, children string }{
	"p2p_policy":            {"p2p_policies", "created_by", ""},
	"supplier":              {"suppliers", "created_by", ""},
	"supplier_bank_account": {"supplier_bank_accounts", "created_by", ""},
	"company_bank_account":  {"company_bank_accounts", "created_by", ""},
	"boq_version":           {"boq_versions", "created_by", ""},
	"purchase_requisition":  {"purchase_requisitions", "requested_by", ""},
	"rfq":                   {"rfqs", "created_by", ""},
	"rfq_quote":             {"rfq_quotes", "received_by", ""},
	"purchase_order":        {"purchase_orders", "created_by", ""},
	"grn":                   {"grns", "created_by", ""},
	"supplier_invoice":      {"supplier_invoices", "entered_by", ""},
	"payment_instruction":   {"payment_instructions", "created_by", ""},
	// Journey B (0018).
	"customer":             {"customers", "created_by", ""},
	"contract_policy":      {"contract_policies", "created_by", ""},
	"contract":             {"contracts", "created_by", ""},
	"measurement":          {"measurements", "created_by", ""},
	"ipc":                  {"ipcs", "created_by", ""},
	"customer_invoice":     {"customer_invoices", "created_by", ""},
	"customer_receipt":     {"customer_receipts", "created_by", ""},
	"customer_credit_note": {"customer_credit_notes", "created_by", ""},
	"retention_release":    {"retention_releases", "created_by", ""},
	"contract_variation":   {"contract_variations", "created_by", ""},
	// Journey C (0021).
	"stock_item":        {"stock_items", "created_by", ""},
	"warehouse":         {"warehouses", "created_by", ""},
	"stock_receipt":     {"stock_receipts", "created_by", ""},
	"stock_issue":       {"stock_issues", "created_by", ""},
	"stock_count":       {"stock_counts", "created_by", ""},
	"stock_transfer":    {"stock_transfers", "created_by", ""},
	"schedule_version":  {"schedule_versions", "created_by", ""},
	"schedule_progress": {"schedule_progress", "created_by", ""},
	// Site operations (0037).
	"site_diary": {"site_diaries", "created_by", ""},
	// Budget revisions and movements (0039).
	"budget_version":       {"budget_versions", "created_by", ""},
	"budget_transfer":      {"budget_transfers", "requested_by", ""},
	"contingency_drawdown": {"contingency_drawdowns", "requested_by", ""},
	// Governance (0040).
	"authority_matrix": {"authority_matrices", "created_by", ""},
	"delegation":       {"delegations", "created_by", ""},
	"sod_exception":    {"sod_exceptions", "created_by", ""},
	// Units of measure (0041).
	"uom_conversion": {"uom_conversions", "created_by", ""},
	// Project master (0042).
	"project_stage_gate": {"project_stage_gates", "created_by", ""},
	// Cost forecasting (0044).
	"forecast_cycle": {"forecast_cycles", "created_by", ""},
	// Change events (0045).
	"change_event": {"change_events", "created_by", ""},
	// Notices and claims (0046).
	"contract_notice": {"contract_notices", "created_by", ""},
	"contract_claim":  {"contract_claims", "created_by", ""},
	// Guarantees and price adjustment (0050).
	"guarantee":          {"guarantees", "created_by", ""},
	"price_index_value":  {"price_index_values", "created_by", ""},
	"escalation_formula": {"escalation_formulas", "created_by", ""},
	"price_adjustment":   {"price_adjustments", "created_by", ""},
	// Liquidated damages terms (0051).
	"ld_terms": {"contract_ld_terms", "created_by", ""},
}

// DocTypes reports whether a document type is known (for callers that must
// validate a type before touching data).
func DocTypes() map[string]bool {
	out := make(map[string]bool, len(p2pTables))
	for k := range p2pTables {
		out[k] = true
	}
	return out
}

// ErrUnknownDocType is returned for an unsupported document type.
var ErrUnknownDocType = errors.New("p2p: unknown document type")

// GetDoc returns a document with its children, as canonical JSON produced by
// the database (the same content the approval subject hash covers).
func GetDoc(ctx context.Context, tx pgx.Tx, docType, id string, lock bool) (Doc, error) {
	t, ok := p2pTables[docType]
	if !ok {
		return Doc{}, ErrUnknownDocType
	}
	q := `SELECT id::text, COALESCE(to_jsonb(x)->>'number', ''), COALESCE(to_jsonb(x)->>'status', to_jsonb(x)->>'technical_status'), ` + t.creator + `, p2p_subject_text($1, $2)
	        FROM ` + t.table + ` x WHERE id = $2::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	var d Doc
	var detail *string
	err := tx.QueryRow(ctx, q, docType, id).Scan(&d.ID, &d.Number, &d.Status, &d.CreatedBy, &detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, ErrNotFound
	}
	if err != nil {
		return Doc{}, err
	}
	if detail != nil {
		d.Detail = json.RawMessage(*detail)
	}
	return d, nil
}

// NextNumber returns the next document number (PREFIX-YYYY-000001).
func NextNumber(ctx context.Context, tx pgx.Tx, prefix string) (string, error) {
	var n string
	err := tx.QueryRow(ctx, `SELECT p2p_next_number($1)`, prefix).Scan(&n)
	return n, err
}

// InsertReturningID runs an INSERT ... RETURNING id::text.
func InsertReturningID(ctx context.Context, tx pgx.Tx, sql string, args ...any) (string, error) {
	var id string
	err := tx.QueryRow(ctx, sql, args...).Scan(&id)
	return id, err
}

// Exec1 runs a statement that must affect exactly one row.
func Exec1(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}

// ExpectedLine is one line of a document's accounting as the database
// computes it (p2p_expected_lines).
type ExpectedLine struct {
	AccountID  string
	Side       string
	Amount     decimal.Decimal
	ProjectID  string
	CostCodeID string
}

// ExpectedLines returns the accounting the database requires for a document.
func ExpectedLines(ctx context.Context, tx pgx.Tx, doc, id string) ([]ExpectedLine, error) {
	rows, err := tx.Query(ctx, `SELECT account_id::text, side, amount, COALESCE(project_id::text, ''), COALESCE(cost_code_id::text, '')
		FROM subledger_expected_lines($1, $2::uuid) ORDER BY side DESC, account_id, project_id, cost_code_id`, doc, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExpectedLine
	for rows.Next() {
		var l ExpectedLine
		if err := rows.Scan(&l.AccountID, &l.Side, &l.Amount, &l.ProjectID, &l.CostCodeID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CostPosition is the budget position of one cost code.
type CostPosition struct {
	CostCodeID   string          `json:"cost_code_id"`
	CostCode     string          `json:"cost_code"`
	Budget       decimal.Decimal `json:"budget"`
	Committed    decimal.Decimal `json:"committed"`
	Reserved     decimal.Decimal `json:"reserved"`
	Received     decimal.Decimal `json:"received"`
	Invoiced     decimal.Decimal `json:"invoiced"`
	ActualPosted decimal.Decimal `json:"actual_posted"`
	ActualOther  decimal.Decimal `json:"actual_other"`
	Available    decimal.Decimal `json:"available"`
	Original     decimal.Decimal `json:"original"`
	Current      decimal.Decimal `json:"current"`
}

// ProjectCostReport returns the per-cost-code budget/commitment/actual report.
func ProjectCostReport(ctx context.Context, tx pgx.Tx, projectID string) ([]CostPosition, error) {
	rows, err := tx.Query(ctx, `SELECT cost_code_id::text, cost_code, budget, committed, reserved, received, invoiced, actual_posted, actual_other, available, original, current
		FROM p2p_project_cost_report($1::uuid)`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CostPosition
	for rows.Next() {
		var c CostPosition
		if err := rows.Scan(&c.CostCodeID, &c.CostCode, &c.Budget, &c.Committed, &c.Reserved, &c.Received, &c.Invoiced, &c.ActualPosted, &c.ActualOther, &c.Available, &c.Original, &c.Current); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReconCheck is one subledger-to-GL reconciliation line.
type ReconCheck struct {
	Check      string          `json:"check"`
	Subledger  decimal.Decimal `json:"subledger"`
	GL         decimal.Decimal `json:"gl"`
	Difference decimal.Decimal `json:"difference"`
	Unposted   int64           `json:"unposted_or_unmatched"`
}

// GLReconciliation returns the subledger-to-GL checks of an entity.
func GLReconciliation(ctx context.Context, tx pgx.Tx, entityID string) ([]ReconCheck, error) {
	rows, err := tx.Query(ctx, `SELECT check_name, subledger, gl, difference, unposted_documents FROM p2p_gl_reconciliation($1::uuid)`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReconCheck
	for rows.Next() {
		var c ReconCheck
		if err := rows.Scan(&c.Check, &c.Subledger, &c.GL, &c.Difference, &c.Unposted); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LockProjectBudget locks the project of a requisition or purchase order
// (budget_lock_project, 0039) for the rest of the transaction. Budget
// transfers and drawdowns take the same lock, so a budget check and a budget
// movement of one project never interleave.
func LockProjectBudget(ctx context.Context, tx pgx.Tx, docType, id string) error {
	q := map[string]string{
		"purchase_requisition": `SELECT budget_lock_project(project_id) FROM purchase_requisitions WHERE id = $1::uuid`,
		"purchase_order":       `SELECT budget_lock_project(project_id) FROM purchase_orders WHERE id = $1::uuid`,
	}[docType]
	if q == "" {
		return fmt.Errorf("LockProjectBudget: unsupported document type %q", docType)
	}
	_, err := tx.Exec(ctx, q, id)
	return err
}

// BudgetShortfall returns "" when the document fits the available budget,
// otherwise the database's description of the first shortfall.
func BudgetShortfall(ctx context.Context, tx pgx.Tx, prID, poID string) (string, error) {
	var s *string
	var pr, po *string
	if prID != "" {
		pr = &prID
	}
	if poID != "" {
		po = &poID
	}
	if err := tx.QueryRow(ctx, `SELECT p2p_assert_budget($1::uuid, $2::uuid)`, pr, po).Scan(&s); err != nil {
		return "", err
	}
	if s == nil {
		return "", nil
	}
	return *s, nil
}

// DBDate returns the database's current date (documents are dated by the
// database clock, never the client's).
func DBDate(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var d time.Time
	err := tx.QueryRow(ctx, `SELECT current_date::timestamptz`).Scan(&d)
	return d, err
}

// DocSummary is a list row: enough for a worklist, scope-filterable.
type DocSummary struct {
	ID        string          `json:"id"`
	Number    string          `json:"number,omitempty"`
	Status    string          `json:"status"`
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
	ProjectID string          `json:"project_id,omitempty"`
	Amount    decimal.Decimal `json:"amount"`
	Label     string          `json:"label"`
}

var listSQL = map[string]string{
	"p2p_policy":            `SELECT id::text, 'v' || version, status, created_by, created_at, '', 0::numeric, 'P2P policy' FROM p2p_policies`,
	"supplier":              `SELECT id::text, code, status, created_by, created_at, '', 0::numeric, name FROM suppliers`,
	"supplier_bank_account": `SELECT b.id::text, '', b.status, b.created_by, b.created_at, '', 0::numeric, s.code || ' ' || b.bank_name || ' ****' || COALESCE(b.account_last4, right(b.account_number, 4)) FROM supplier_bank_accounts b JOIN suppliers s ON s.id = b.supplier_id`,
	"company_bank_account":  `SELECT id::text, '', status, created_by, created_at, '', 0::numeric, name || ' ****' || COALESCE(account_last4, right(account_number, 4)) FROM company_bank_accounts`,
	"boq_version":           `SELECT v.id::text, 'v' || v.version, v.status, v.created_by, v.created_at, v.project_id::text, COALESCE((SELECT sum(amount) FROM boq_items i WHERE i.version_id = v.id), 0), v.description FROM boq_versions v`,
	"purchase_requisition":  `SELECT r.id::text, r.number, r.status, r.requested_by, r.created_at, r.project_id::text, COALESCE((SELECT sum(est_amount) FROM pr_lines l WHERE l.pr_id = r.id), 0), r.justification FROM purchase_requisitions r`,
	"rfq":                   `SELECT q.id::text, q.number, q.status, q.created_by, q.created_at, r.project_id::text, 0::numeric, r.justification FROM rfqs q JOIN purchase_requisitions r ON r.id = q.pr_id`,
	"rfq_quote":             `SELECT x.id::text, s.code, x.technical_status, x.received_by, x.received_at, r.project_id::text, x.total, q.number FROM rfq_quotes x JOIN rfqs q ON q.id = x.rfq_id JOIN purchase_requisitions r ON r.id = q.pr_id JOIN suppliers s ON s.id = x.supplier_id`,
	"purchase_order":        `SELECT o.id::text, o.number, o.status, o.created_by, o.created_at, o.project_id::text, o.total, s.name FROM purchase_orders o JOIN suppliers s ON s.id = o.supplier_id`,
	"grn":                   `SELECT g.id::text, g.number, g.status, g.created_by, g.created_at, o.project_id::text, 0::numeric, o.number || ' / ' || g.delivery_note_ref FROM grns g JOIN purchase_orders o ON o.id = g.po_id`,
	"supplier_invoice":      `SELECT i.id::text, i.invoice_number, i.status || CASE WHEN i.status = 'SUBMITTED' THEN '/' || i.match_status ELSE '' END, i.entered_by, i.created_at, o.project_id::text, i.total, s.name FROM supplier_invoices i JOIN purchase_orders o ON o.id = i.po_id JOIN suppliers s ON s.id = i.supplier_id`,
	"payment_instruction":   `SELECT p.id::text, p.number, p.status, p.created_by, p.created_at, '', p.amount, s.name FROM payment_instructions p JOIN suppliers s ON s.id = p.supplier_id`,
	"customer":              `SELECT id::text, code, status, created_by, created_at, '', 0::numeric, name FROM customers`,
	"contract_policy":       `SELECT id::text, 'v' || version, status, created_by, created_at, '', 0::numeric, 'Contract policy' FROM contract_policies`,
	"contract":              `SELECT c.id::text, c.number, c.status, c.created_by, c.created_at, c.project_id::text, c.contract_value, c.title || ' — ' || u.name FROM contracts c JOIN customers u ON u.id = c.customer_id`,
	"measurement":           `SELECT m.id::text, m.number, m.status, m.created_by, m.created_at, c.project_id::text, 0::numeric, c.number || ' to ' || m.period_end FROM measurements m JOIN contracts c ON c.id = m.contract_id`,
	"ipc":                   `SELECT i.id::text, i.number, i.status, i.created_by, i.created_at, c.project_id::text, i.amount_due, c.number || ' IPC ' || i.seq || ' to ' || i.period_end FROM ipcs i JOIN contracts c ON c.id = i.contract_id`,
	"customer_invoice":      `SELECT i.id::text, i.number, i.status, i.created_by, i.created_at, i.project_id::text, i.total, u.name FROM customer_invoices i JOIN customers u ON u.id = i.customer_id`,
	"customer_receipt":      `SELECT r.id::text, r.number, r.status, r.created_by, r.created_at, '', r.amount, l.reference FROM customer_receipts r JOIN bank_statement_lines l ON l.id = r.statement_line_id`,
	"customer_credit_note":  `SELECT c.id::text, c.number, c.status, c.created_by, c.created_at, c.project_id::text, c.total, i.number || ' — ' || c.reason FROM customer_credit_notes c JOIN customer_invoices i ON i.id = c.invoice_id`,
	"contract_variation":    `SELECT v.id::text, v.number, v.status, v.created_by, v.created_at, c.project_id::text, v.value_change, c.number || ' — ' || v.title FROM contract_variations v JOIN contracts c ON c.id = v.contract_id`,
	"retention_release":     `SELECT x.id::text, x.number, x.status, x.created_by, x.created_at, x.project_id::text, x.amount, c.number || ' — ' || x.milestone FROM retention_releases x JOIN contracts c ON c.id = x.contract_id`,
	"stock_item":            `SELECT id::text, code, status, created_by, created_at, '', 0::numeric, name || ' (' || unit || ', ' || valuation_method || ')' FROM stock_items`,
	"warehouse":             `SELECT id::text, code, status, created_by, created_at, COALESCE(project_id::text, ''), 0::numeric, name FROM warehouses`,
	"stock_receipt":         `SELECT r.id::text, r.number, r.status, r.created_by, r.created_at, COALESCE(w.project_id::text, ''), r.amount, i.code || ' ' || r.quantity || ' ' || i.unit || ' into ' || w.code FROM stock_receipts r JOIN stock_items i ON i.id = r.stock_item_id JOIN warehouses w ON w.id = r.warehouse_id`,
	"stock_issue":           `SELECT s.id::text, s.number, s.status, s.created_by, s.created_at, s.project_id::text, COALESCE(s.amount, 0), i.code || ' ' || s.quantity || ' ' || i.unit || ' from ' || w.code FROM stock_issues s JOIN stock_items i ON i.id = s.stock_item_id JOIN warehouses w ON w.id = s.warehouse_id`,
	"stock_count":           `SELECT c.id::text, c.number, c.status, c.created_by, c.created_at, COALESCE(c.project_id::text, ''), COALESCE(c.amount, 0), i.code || ' counted ' || c.counted_qty || ' (book ' || c.system_qty || ') in ' || w.code FROM stock_counts c JOIN stock_items i ON i.id = c.stock_item_id JOIN warehouses w ON w.id = c.warehouse_id`,
	"schedule_version":      `SELECT s.id::text, 'v' || s.version, s.status, s.created_by, s.created_at, s.project_id::text, COALESCE((SELECT sum(budget) FROM schedule_activities a WHERE a.version_id = s.id), 0), s.description || COALESCE(' — finish ' || s.finish_date, '') FROM schedule_versions s`,
	"schedule_progress":     `SELECT p.id::text, p.number, p.status, p.created_by, p.created_at, p.project_id::text, 0::numeric, 'Progress at ' || p.status_date || ' on v' || s.version FROM schedule_progress p JOIN schedule_versions s ON s.id = p.version_id`,
	"site_diary":            `SELECT d.id::text, d.number, d.status, d.created_by, d.created_at, d.project_id::text, 0::numeric, 'Site diary ' || d.diary_date || ' v' || d.version FROM site_diaries d`,
	"stock_transfer":        `SELECT t.id::text, t.number, t.status, t.created_by, t.created_at, COALESCE(wf.project_id::text, ''), COALESCE(t.amount, 0), i.code || ' ' || t.quantity || ' ' || i.unit || ' ' || wf.code || ' to ' || wt.code FROM stock_transfers t JOIN stock_items i ON i.id = t.stock_item_id JOIN warehouses wf ON wf.id = t.from_warehouse_id JOIN warehouses wt ON wt.id = t.to_warehouse_id`,
	"budget_version":        `SELECT b.id::text, b.kind || ' v' || b.seq, b.status, b.created_by, b.created_at, b.project_id::text, COALESCE((SELECT sum(amount) FROM budget_lines l WHERE l.version_id = b.id), 0), b.reason FROM budget_versions b`,
	"budget_transfer":       `SELECT t.id::text, 'TRF-' || substr(t.id::text, 1, 8), t.status, t.requested_by, t.created_at, t.project_id::text, t.amount, (SELECT code FROM cost_codes WHERE id = t.from_cost_code) || ' -> ' || (SELECT code FROM cost_codes WHERE id = t.to_cost_code) || ' — ' || t.reason FROM budget_transfers t`,
	"contingency_drawdown":  `SELECT d.id::text, 'DRW-' || substr(d.id::text, 1, 8), d.status, d.requested_by, d.created_at, d.project_id::text, d.amount, (SELECT code FROM cost_codes WHERE id = d.to_cost_code) || ' — ' || d.reason FROM contingency_drawdowns d`,
	"authority_matrix":      `SELECT m.id::text, 'AUTH-v' || m.version, m.status, m.created_by, m.created_at, '', (SELECT count(*) FROM authority_rules r WHERE r.matrix_id = m.id), m.reason FROM authority_matrices m`,
	"delegation":            `SELECT d.id::text, 'DEL-' || substr(d.id::text, 1, 8), d.status, d.created_by, d.created_at, '', 0::numeric, d.delegator || ' -> ' || d.delegate || ' (' || array_to_string(d.perms, ', ') || ')' FROM delegations d`,
	"sod_exception":         `SELECT e.id::text, 'SOD-' || substr(e.id::text, 1, 8), e.status, e.created_by, e.created_at, '', 0::numeric, e.user_id || ': ' || e.perm_a || ' + ' || e.perm_b FROM sod_exceptions e`,
	"uom_conversion":        `SELECT c.id::text, 'UOM-' || substr(c.id::text, 1, 8), c.status, c.created_by, c.created_at, '', 0::numeric, '1 ' || c.from_uom || ' = ' || c.factor::text || ' ' || c.to_uom || COALESCE(' (' || (SELECT code FROM stock_items i WHERE i.id = c.stock_item_id) || ')', ' (all items)') FROM uom_conversions c`,
	"project_stage_gate":    `SELECT g.id::text, 'GATE-' || substr(g.id::text, 1, 8), g.status, g.created_by, g.created_at, g.project_id::text, 0::numeric, (SELECT code FROM projects WHERE id = g.project_id) || ': ' || g.from_phase || ' -> ' || g.to_phase FROM project_stage_gates g`,
	"forecast_cycle":        `SELECT f.id::text, 'FC-' || to_char(f.period_end, 'YYYY-MM'), f.status, f.created_by, f.created_at, f.project_id::text, COALESCE((SELECT sum(eac) FROM forecast_lines l WHERE l.cycle_id = f.id), 0), (SELECT code FROM projects WHERE id = f.project_id) || ' forecast at completion for ' || f.period_end FROM forecast_cycles f`,
	"change_event":          `SELECT e.id::text, e.number, e.status, e.created_by, e.created_at, e.project_id::text, COALESCE((SELECT sum(cost_impact) FROM change_event_lines l WHERE l.event_id = e.id), 0), e.source || ' — ' || e.title FROM change_events e`,
	"contract_notice":       `SELECT n.id::text, n.number, n.status, n.created_by, n.created_at, c.project_id::text, 0::numeric, n.clause_ref || ' — ' || n.subject || ' (notify by ' || n.due_on || ')' FROM contract_notices n JOIN contracts c ON c.id = n.contract_id`,
	"contract_claim":        `SELECT k.id::text, k.number, k.status, k.created_by, k.created_at, c.project_id::text, k.amount, k.kind || ' — ' || k.title || CASE WHEN k.days > 0 THEN ' (' || k.days || ' days)' ELSE '' END || COALESCE(' — client: ' || lower(k.client_outcome), '') FROM contract_claims k JOIN contracts c ON c.id = k.contract_id`,
	"guarantee":             `SELECT g.id::text, g.number, g.status, g.created_by, g.created_at, c.project_id::text, g.amount, initcap(g.kind) || ' — ' || g.issuer || ' ' || g.reference || ' (expires ' || g.expires_on || ')' FROM guarantees g JOIN contracts c ON c.id = g.contract_id`,
	"price_index_value":     `SELECT v.id::text, v.index_code || ' ' || to_char(v.period, 'YYYY-MM'), v.status, v.created_by, v.created_at, '', v.value, x.name || ' — ' || v.source_ref FROM price_index_values v JOIN price_indices x ON x.code = v.index_code`,
	"escalation_formula":    `SELECT f.id::text, f.number, f.status, f.created_by, f.created_at, c.project_id::text, f.fixed_portion, c.number || ' — fixed ' || f.fixed_portion || ', base ' || to_char(f.base_period, 'YYYY-MM') || ' (' || (SELECT string_agg(t.index_code || ' ' || t.weight, ', ' ORDER BY t.index_code) FROM escalation_terms t WHERE t.formula_id = f.id) || ')' FROM escalation_formulas f JOIN contracts c ON c.id = f.contract_id`,
	"price_adjustment":      `SELECT a.id::text, a.number, a.status, a.created_by, a.created_at, c.project_id::text, a.amount, i.number || ' — factor ' || a.factor || ' on ' || a.base_value || ' (indices ' || to_char(a.current_period, 'YYYY-MM') || ')' FROM price_adjustments a JOIN contracts c ON c.id = a.contract_id JOIN ipcs i ON i.id = a.ipc_id`,
	"ld_terms":              `SELECT t.id::text, t.number, t.status, t.created_by, t.created_at, c.project_id::text, t.rate_per_day, c.number || ' — complete by ' || t.completion_date || ', ' || t.rate_per_day || ' a day, cap ' || t.cap_amount || ' (clause ' || t.clause_ref || ')' FROM contract_ld_terms t JOIN contracts c ON c.id = t.contract_id`,
	// Money-in statement lines not yet recorded as a receipt (AR worklist).
	"money_in": `SELECT l.id::text, s.statement_no || '/' || l.line_no, CASE WHEN EXISTS (SELECT 1 FROM customer_receipts r WHERE r.statement_line_id = l.id AND r.status <> 'CANCELLED') THEN 'RECORDED' ELSE 'UNAPPLIED' END,
	       s.imported_by, s.imported_at, '', l.amount, l.reference || ' — ' || l.counterparty FROM bank_statement_lines l JOIN bank_statements s ON s.id = l.statement_id WHERE l.amount > 0`,
	"bank_statement": `SELECT s.id::text, s.statement_no, CASE WHEN EXISTS (SELECT 1 FROM bank_statement_lines l WHERE l.statement_id = s.id AND l.status = 'UNMATCHED') THEN 'OPEN' ELSE 'RECONCILED' END,
	       s.imported_by, s.imported_at, '', s.closing_balance, b.name FROM bank_statements s JOIN company_bank_accounts b ON b.id = s.company_bank_id`,
}

// ListScope is the project visibility applied inside the list query (before
// LIMIT, so a project-scoped worklist is never truncated by other projects'
// documents).
type ListScope struct {
	TenantWide  bool     // every document
	Projects    []string // project documents of these projects
	EntityLevel bool     // documents without a project
}

// ListDocs lists documents of a type, newest first, optionally by status.
func ListDocs(ctx context.Context, tx pgx.Tx, docType, status string, scope ListScope, limit int) ([]DocSummary, error) {
	q, ok := listSQL[docType]
	if !ok {
		return nil, ErrUnknownDocType
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := tx.Query(ctx, `SELECT * FROM (`+q+`) d(id, number, status, created_by, created_at, project_id, amount, label)
		WHERE ($1 = '' OR d.status = $1 OR d.status LIKE $1 || '/%')
		  AND ($3 OR (d.project_id <> '' AND d.project_id = ANY($4::text[])) OR (d.project_id = '' AND $5))
		ORDER BY created_at DESC LIMIT $2`, status, limit, scope.TenantWide, scope.Projects, scope.EntityLevel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocSummary
	for rows.Next() {
		var d DocSummary
		if err := rows.Scan(&d.ID, &d.Number, &d.Status, &d.CreatedBy, &d.CreatedAt, &d.ProjectID, &d.Amount, &d.Label); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// JournalSummary is a journal worklist row.
type JournalSummary struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	SourceType  string          `json:"source_type"`
	SourceRef   string          `json:"source_ref"`
	Description string          `json:"description"`
	EventDate   time.Time       `json:"business_event_at"`
	CreatedBy   string          `json:"created_by"`
	ApprovedBy  string          `json:"approved_by,omitempty"`
	ProjectID   string          `json:"project_id,omitempty"`
	Debits      decimal.Decimal `json:"debits"`
}

// ListJournals lists journals by status (and source type prefix).
func ListJournals(ctx context.Context, tx pgx.Tx, status, sourcePrefix string, limit int) ([]JournalSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := tx.Query(ctx, `SELECT j.id::text, j.status, j.source_type, COALESCE(j.source_ref, ''), j.description, j.business_event_at, j.created_by,
		COALESCE(j.approved_by, ''), COALESCE(j.project_id::text, ''), COALESCE((SELECT sum(amount) FROM journal_lines l WHERE l.journal_id = j.id AND l.side = 'DEBIT'), 0)
		FROM journals j WHERE ($1 = '' OR j.status = $1) AND j.source_type LIKE $2 || '%' ORDER BY j.created_at DESC LIMIT $3`, status, sourcePrefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalSummary
	for rows.Next() {
		var j JournalSummary
		if err := rows.Scan(&j.ID, &j.Status, &j.SourceType, &j.SourceRef, &j.Description, &j.EventDate, &j.CreatedBy, &j.ApprovedBy, &j.ProjectID, &j.Debits); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// RefItem is a reference-data choice for forms (entity, project, account,
// cost code). It carries no amounts and no personal data.
type RefItem struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`   // account type, cost type
	Parent string `json:"parent,omitempty"` // legal entity of a project/account
}

func refRows(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]RefItem, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RefItem{}
	for rows.Next() {
		var r RefItem
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Kind, &r.Parent); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReferenceData returns the tenant's legal entities, active projects and
// active GL accounts (RLS confines every query to the current tenant).
func ReferenceData(ctx context.Context, tx pgx.Tx) (entities, projects, accounts []RefItem, err error) {
	if entities, err = refRows(ctx, tx, `SELECT id::text, country::text, name, functional_currency::text, '' FROM legal_entities ORDER BY name`); err != nil {
		return
	}
	if projects, err = refRows(ctx, tx, `SELECT id::text, code, name, COALESCE(contract_currency::text, ''), legal_entity_id::text FROM projects WHERE status = 'ACTIVE' ORDER BY code`); err != nil {
		return
	}
	accounts, err = refRows(ctx, tx, `SELECT id::text, code, name, type, legal_entity_id::text FROM accounts WHERE active ORDER BY code`)
	return
}

// ProjectCostCodes lists the active cost codes of a project's approved WBS.
func ProjectCostCodes(ctx context.Context, tx pgx.Tx, projectID string) ([]RefItem, error) {
	return refRows(ctx, tx, `SELECT c.id::text, c.code, c.name, c.cost_type, c.project_id::text
		  FROM cost_codes c JOIN wbs_nodes n ON n.id = c.wbs_node_id JOIN wbs_versions v ON v.id = n.version_id
		 WHERE c.project_id = $1::uuid AND v.status = 'APPROVED' AND c.status = 'ACTIVE'
		   AND c.valid_from <= current_date AND (c.valid_to IS NULL OR c.valid_to >= current_date)
		 ORDER BY c.code`, projectID)
}

// ContractPosition is the commercial position of a contract (0018).
type ContractPosition struct {
	ContractValue        decimal.Decimal `json:"contract_value"`
	VariationsApproved   decimal.Decimal `json:"variations_approved"`
	RevisedContractValue decimal.Decimal `json:"revised_contract_value"`
	MeasuredValue        decimal.Decimal `json:"measured_value"`
	CertifiedGross       decimal.Decimal `json:"certified_gross"`
	RetentionHeld        decimal.Decimal `json:"retention_held"`
	RetentionReleased    decimal.Decimal `json:"retention_released"`
	AdvanceAmount        decimal.Decimal `json:"advance_amount"`
	AdvanceReceived      decimal.Decimal `json:"advance_received"`
	AdvanceRecovered     decimal.Decimal `json:"advance_recovered"`
	InvoicedTotal        decimal.Decimal `json:"invoiced_total"`
	CreditedTotal        decimal.Decimal `json:"credited_total"`
	Received             decimal.Decimal `json:"received"`
	ReceivableOpen       decimal.Decimal `json:"receivable_open"`
}

// GetContractPosition reads ar_contract_position.
func GetContractPosition(ctx context.Context, tx pgx.Tx, contractID string) (ContractPosition, error) {
	var c ContractPosition
	err := tx.QueryRow(ctx, `SELECT contract_value, variations_approved, revised_contract_value, measured_value, certified_gross, retention_held, retention_released, advance_amount, advance_received,
		advance_recovered, invoiced_total, credited_total, received, receivable_open FROM ar_contract_position($1::uuid)`, contractID).Scan(
		&c.ContractValue, &c.VariationsApproved, &c.RevisedContractValue, &c.MeasuredValue, &c.CertifiedGross, &c.RetentionHeld, &c.RetentionReleased, &c.AdvanceAmount, &c.AdvanceReceived,
		&c.AdvanceRecovered, &c.InvoicedTotal, &c.CreditedTotal, &c.Received, &c.ReceivableOpen)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ARReconciliation reads ar_gl_reconciliation (GL may be null for
// information-only rows).
func ARReconciliation(ctx context.Context, tx pgx.Tx, entity string) ([]ReconCheck, error) {
	rows, err := tx.Query(ctx, `SELECT check_name, subledger, COALESCE(gl, subledger), COALESCE(difference, 0), unposted_documents FROM ar_gl_reconciliation($1::uuid)`, entity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReconCheck{}
	for rows.Next() {
		var c ReconCheck
		if err := rows.Scan(&c.Check, &c.Subledger, &c.GL, &c.Difference, &c.Unposted); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Profitability is posted revenue and cost of a project.
type Profitability struct {
	RevenuePosted decimal.Decimal `json:"revenue_posted"`
	CostPosted    decimal.Decimal `json:"cost_posted"`
	Margin        decimal.Decimal `json:"margin"`
}

// ProjectProfitability reads project_profitability.
func ProjectProfitability(ctx context.Context, tx pgx.Tx, project string) (Profitability, error) {
	var p Profitability
	err := tx.QueryRow(ctx, `SELECT revenue_posted, cost_posted, margin FROM project_profitability($1::uuid)`, project).Scan(&p.RevenuePosted, &p.CostPosted, &p.Margin)
	return p, err
}

// StockReconciliation reads stock_gl_reconciliation (0021).
func StockReconciliation(ctx context.Context, tx pgx.Tx, entity string) ([]ReconCheck, error) {
	rows, err := tx.Query(ctx, `SELECT check_name, subledger, COALESCE(gl, subledger), COALESCE(difference, 0), unposted_documents FROM stock_gl_reconciliation($1::uuid)`, entity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReconCheck{}
	for rows.Next() {
		var c ReconCheck
		if err := rows.Scan(&c.Check, &c.Subledger, &c.GL, &c.Difference, &c.Unposted); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StockLine is one item in one warehouse with its quantity and value.
type StockLine struct {
	ItemID          string          `json:"stock_item_id"`
	ItemCode        string          `json:"item_code"`
	ItemName        string          `json:"item_name"`
	Unit            string          `json:"unit"`
	ValuationMethod string          `json:"valuation_method"`
	WarehouseID     string          `json:"warehouse_id"`
	WarehouseCode   string          `json:"warehouse_code"`
	ProjectID       string          `json:"project_id"`
	Qty             decimal.Decimal `json:"qty"`
	Value           decimal.Decimal `json:"value"`
	UnitCost        decimal.Decimal `json:"unit_cost"`
}

// StockOnHand reads stock_on_hand (0021); the caller filters by visibility.
func StockOnHand(ctx context.Context, tx pgx.Tx, entity string) ([]StockLine, error) {
	rows, err := tx.Query(ctx, `SELECT stock_item_id::text, item_code, item_name, unit, valuation_method, warehouse_id::text, warehouse_code,
		COALESCE(project_id::text, ''), qty, value, unit_cost FROM stock_on_hand($1::uuid)`, entity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StockLine{}
	for rows.Next() {
		var l StockLine
		if err := rows.Scan(&l.ItemID, &l.ItemCode, &l.ItemName, &l.Unit, &l.ValuationMethod, &l.WarehouseID, &l.WarehouseCode, &l.ProjectID,
			&l.Qty, &l.Value, &l.UnitCost); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// EVMReport is earned value at a status date (0027, ADR 0013).
type EVMReport struct {
	VersionID    string           `json:"schedule_version_id"`
	BAC          decimal.Decimal  `json:"bac"`
	PV           decimal.Decimal  `json:"pv"`
	EV           decimal.Decimal  `json:"ev"`
	AC           decimal.Decimal  `json:"ac"`
	SV           decimal.Decimal  `json:"sv"`
	CV           decimal.Decimal  `json:"cv"`
	SPI          *decimal.Decimal `json:"spi"`
	CPI          *decimal.Decimal `json:"cpi"`
	ProgressDate *time.Time       `json:"progress_date"`
}

// ScheduleEVM reads schedule_evm; ErrNotFound when the project has no baseline.
func ScheduleEVM(ctx context.Context, tx pgx.Tx, projectID string, statusDate time.Time) (EVMReport, error) {
	var r EVMReport
	err := tx.QueryRow(ctx, `SELECT version_id::text, bac, pv, ev, ac, sv, cv, spi, cpi, progress_date FROM schedule_evm($1::uuid, $2)`, projectID, statusDate).Scan(
		&r.VersionID, &r.BAC, &r.PV, &r.EV, &r.AC, &r.SV, &r.CV, &r.SPI, &r.CPI, &r.ProgressDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}
