// Report catalogue (WP-1.5; SRS RPT-01, RPT-02, RPT-06).
//
// One registry describes every catalogue report: its id, title, what it is
// for, who may run it, which filters it takes, its columns and the single
// read-only SQL query that produces its rows. One generic runner executes any
// of them, so every report gets the same behaviour for free:
//
//   - permission and project scope (project reports show only the caller's
//     projects; company-wide reports need tenant-wide scope), plus row-level
//     security in the database;
//   - a hard row limit and statement timeout;
//   - totals for the money columns, computed here in exact decimals;
//   - the data-as-of time and the filters used, printed with the result
//     (RPT-06), and PDF / Word / Excel export through /v1/export (RPT-02),
//     which re-issues the same read as the caller.
//
// Adding a report = adding one entry to reportDefs below and a case to
// TestReportCatalogue_EveryReportRuns. Queries use the shared parameters:
//
//	$1 entity (uuid)         $2 project (uuid or null)    $3 from (date or null)
//	$4 to (date or null)     $5 subject (uuid or null; account, supplier or customer)
//	$6 projects in scope     $7 tenant-wide (bool)
package kernel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ReportMaxRows bounds any report; the result says when it was cut.
const ReportMaxRows = 5000

// ReportCol describes one column. Type: text | money | qty | date.
type ReportCol struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

// ReportDef is one catalogue report.
type ReportDef struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Group       string      `json:"group"`
	Perm        string      `json:"permission"`
	TenantWide  bool        `json:"tenant_wide"` // needs tenant-wide scope (company-wide data)
	Filters     []string    `json:"filters"`     // entity | project | from | to | subject | as_of
	Subject     string      `json:"subject,omitempty"`
	Columns     []ReportCol `json:"columns"`
	Totals      []string    `json:"totals,omitempty"` // money/qty column keys to sum
	SQL         string      `json:"-"`
}

func col(k, l, t string) ReportCol { return ReportCol{Key: k, Label: l, Type: t} }

const reportParamPrefix = `WITH prm AS (SELECT $1::uuid AS entity, $2::uuid AS project, $3::date AS dfrom, $4::date AS dto, $5::uuid AS subject, $6::uuid[] AS projects, $7::boolean AS tenant_wide) `

const (
	scopeFilter  = `($7 OR %s = ANY($6::uuid[]))`
	periodFilter = `($3::date IS NULL OR %[1]s >= $3::date) AND ($4::date IS NULL OR %[1]s <= $4::date)`
)

func pf(expr string) string { return fmt.Sprintf(periodFilter, expr) }
func sf(expr string) string { return fmt.Sprintf(scopeFilter, expr) }

var reportDefs = []ReportDef{
	{ID: "gl-detail", Title: "General ledger detail", Group: "Ledger", Perm: "journal.read", TenantWide: true, Filters: []string{"entity", "subject", "from", "to"}, Subject: "account",
		Description: "Every posted journal line of the company, optionally for one account and a period.",
		Columns: []ReportCol{col("date", "Date", "date"), col("account_code", "Account", "text"), col("account_name", "Account name", "text"), col("source_type", "Source", "text"),
			col("reference", "Reference", "text"), col("description", "Description", "text"), col("cost_code", "Cost code", "text"), col("debit", "Debit", "money"), col("credit", "Credit", "money")},
		Totals: []string{"debit", "credit"},
		SQL: `SELECT j.business_event_at::date::text AS date, a.code AS account_code, a.name AS account_name, j.source_type, COALESCE(j.source_ref, '') AS reference,
		        COALESCE(j.description, '') AS description, COALESCE(c.code, '') AS cost_code,
		        CASE WHEN l.side = 'DEBIT' THEN l.amount::text ELSE '0' END AS debit, CASE WHEN l.side = 'CREDIT' THEN l.amount::text ELSE '0' END AS credit
		   FROM journal_lines l JOIN journals j ON j.id = l.journal_id JOIN accounts a ON a.id = l.account_id LEFT JOIN cost_codes c ON c.id = l.cost_code_id
		  WHERE j.legal_entity_id = $1::uuid AND j.status IN ('POSTED', 'REVERSED') AND ($5::uuid IS NULL OR l.account_id = $5::uuid) AND ` + pf("j.business_event_at::date") + `
		  ORDER BY j.business_event_at, j.id, l.line_no`},

	{ID: "journal-register", Title: "Journal register", Group: "Ledger", Perm: "journal.read", TenantWide: true, Filters: []string{"entity", "from", "to"},
		Description: "Every journal of the company with its status, total and who prepared, approved and posted it.",
		Columns: []ReportCol{col("date", "Date", "date"), col("reference", "Reference", "text"), col("source_type", "Source", "text"), col("description", "Description", "text"),
			col("status", "Status", "text"), col("total", "Total", "money"), col("prepared_by", "Prepared by", "text"), col("approved_by", "Approved by", "text"), col("posted_by", "Posted by", "text")},
		Totals: []string{"total"},
		SQL: `SELECT j.business_event_at::date::text AS date, substr(j.id::text, 1, 8) AS reference, j.source_type, COALESCE(j.description, '') AS description, j.status,
		        COALESCE((SELECT sum(l.amount) FROM journal_lines l WHERE l.journal_id = j.id AND l.side = 'DEBIT'), 0)::text AS total,
		        j.created_by AS prepared_by, COALESCE(j.approved_by, '') AS approved_by, COALESCE(j.posted_by, '') AS posted_by
		   FROM journals j WHERE j.legal_entity_id = $1::uuid AND ` + pf("j.business_event_at::date") + ` ORDER BY j.business_event_at, j.id`},

	{ID: "vendor-statement", Title: "Supplier statement", Group: "Payables", Perm: "p2p.read", TenantWide: true, Filters: []string{"entity", "subject", "from", "to"}, Subject: "supplier",
		Description: "Approved invoices and released payments of one supplier, with the running balance owed.",
		Columns: []ReportCol{col("date", "Date", "date"), col("kind", "Type", "text"), col("document", "Document", "text"), col("invoiced", "Invoiced", "money"),
			col("paid", "Paid", "money"), col("balance", "Balance owed", "money")},
		Totals: []string{"invoiced", "paid"},
		SQL: `SELECT date, kind, document, invoiced::text, paid::text, (sum(invoiced - paid) OVER (ORDER BY date, ord, document))::text AS balance FROM (
		        SELECT i.invoice_date AS date, 1 AS ord, 'Invoice' AS kind, i.invoice_number AS document, i.total AS invoiced, 0::numeric AS paid
		          FROM supplier_invoices i JOIN suppliers s ON s.id = i.supplier_id
		         WHERE s.legal_entity_id = $1::uuid AND i.supplier_id = $5::uuid AND i.status = 'APPROVED'
		        UNION ALL
		        SELECT p.released_at::date, 2, 'Payment', p.number, 0, p.amount FROM payment_instructions p
		         WHERE p.legal_entity_id = $1::uuid AND p.supplier_id = $5::uuid AND p.status IN ('RELEASED', 'SETTLED')) x
		       WHERE ` + pf("date") + ` ORDER BY date, ord, document`},

	{ID: "customer-statement", Title: "Customer statement", Group: "Receivables", Perm: "p2p.read", TenantWide: true, Filters: []string{"entity", "subject", "from", "to"}, Subject: "customer",
		Description: "Approved invoices and confirmed receipts of one customer, with the running balance owed to you.",
		Columns: []ReportCol{col("date", "Date", "date"), col("kind", "Type", "text"), col("document", "Document", "text"), col("invoiced", "Invoiced", "money"),
			col("received", "Received", "money"), col("balance", "Balance owed", "money")},
		Totals: []string{"invoiced", "received"},
		SQL: `SELECT date, kind, document, invoiced::text, received::text, (sum(invoiced - received) OVER (ORDER BY date, ord, document))::text AS balance FROM (
		        SELECT ci.invoice_date AS date, 1 AS ord, 'Invoice' AS kind, ci.number AS document, ci.total AS invoiced, 0::numeric AS received
		          FROM customer_invoices ci WHERE ci.legal_entity_id = $1::uuid AND ci.customer_id = $5::uuid AND ci.status = 'APPROVED'
		        UNION ALL
		        SELECT r.receipt_date, 2, 'Receipt', r.number, 0, sum(ra.amount) FROM customer_receipts r
		          JOIN receipt_allocations ra ON ra.receipt_id = r.id JOIN customer_invoices ci ON ci.id = ra.invoice_id
		         WHERE r.legal_entity_id = $1::uuid AND ci.customer_id = $5::uuid AND r.status = 'CONFIRMED' GROUP BY r.id) x
		       WHERE ` + pf("date") + ` ORDER BY date, ord, document`},

	{ID: "po-register", Title: "Purchase order register", Group: "Procurement", Perm: "p2p.read", Filters: []string{"entity", "project", "from", "to"},
		Description: "Every purchase order of the projects you may see, with supplier, status and value.",
		Columns: []ReportCol{col("date", "Date", "date"), col("number", "Order", "text"), col("project", "Project", "text"), col("supplier", "Supplier", "text"), col("status", "Status", "text"),
			col("total", "Total", "money"), col("approved_by", "Approved by", "text"), col("over_budget", "Over budget", "text")},
		Totals: []string{"total"},
		SQL: `SELECT o.created_at::date::text AS date, o.number, pr.code AS project, s.name AS supplier, o.status, o.total::text AS total, COALESCE(o.approved_by, '') AS approved_by,
		        CASE WHEN o.over_budget THEN 'yes' ELSE '' END AS over_budget
		   FROM purchase_orders o JOIN projects pr ON pr.id = o.project_id JOIN suppliers s ON s.id = o.supplier_id
		  WHERE o.legal_entity_id = $1::uuid AND ($2::uuid IS NULL OR o.project_id = $2::uuid) AND ` + sf("o.project_id") + ` AND ` + pf("o.created_at::date") + ` ORDER BY o.created_at, o.number`},

	{ID: "open-commitments", Title: "Open commitments", Group: "Procurement", Perm: "p2p.read", Filters: []string{"entity", "project"},
		Description: "Approved purchase orders and how much of each is not yet invoiced: money you are committed to pay.",
		Columns: []ReportCol{col("number", "Order", "text"), col("project", "Project", "text"), col("supplier", "Supplier", "text"), col("total", "Order value", "money"),
			col("invoiced", "Invoiced", "money"), col("open", "Open commitment", "money")},
		Totals: []string{"total", "invoiced", "open"},
		SQL: `SELECT number, project, supplier, total::text, invoiced::text, (total - invoiced)::text AS open FROM (
		        SELECT o.number, pr.code AS project, s.name AS supplier, o.total,
		               COALESCE((SELECT sum(i.subtotal) FROM supplier_invoices i WHERE i.po_id = o.id AND i.status = 'APPROVED'), 0) AS invoiced
		          FROM purchase_orders o JOIN projects pr ON pr.id = o.project_id JOIN suppliers s ON s.id = o.supplier_id
		         WHERE o.legal_entity_id = $1::uuid AND o.status = 'APPROVED' AND ($2::uuid IS NULL OR o.project_id = $2::uuid) AND ` + sf("o.project_id") + `) x
		       WHERE total > invoiced ORDER BY project, number`},

	{ID: "grni", Title: "Goods received not invoiced", Group: "Procurement", Perm: "p2p.read", Filters: []string{"entity", "project"},
		Description: "Value of goods confirmed as received on each order that has not yet been invoiced (the GRNI accrual).",
		Columns: []ReportCol{col("number", "Order", "text"), col("project", "Project", "text"), col("supplier", "Supplier", "text"), col("received", "Received value", "money"),
			col("invoiced", "Invoiced", "money"), col("grni", "Received, not invoiced", "money")},
		Totals: []string{"received", "invoiced", "grni"},
		SQL: `SELECT number, project, supplier, received::text, invoiced::text, (received - invoiced)::text AS grni FROM (
		        SELECT o.number, pr.code AS project, s.name AS supplier,
		               COALESCE((SELECT sum(round(gl.qty_received * pl.unit_price, 2)) FROM grn_lines gl JOIN grns g ON g.id = gl.grn_id
		                          JOIN po_lines pl ON pl.po_id = g.po_id AND pl.line_no = gl.po_line_no WHERE g.po_id = o.id AND g.status = 'CONFIRMED'), 0) AS received,
		               COALESCE((SELECT sum(i.subtotal) FROM supplier_invoices i WHERE i.po_id = o.id AND i.status = 'APPROVED'), 0) AS invoiced
		          FROM purchase_orders o JOIN projects pr ON pr.id = o.project_id JOIN suppliers s ON s.id = o.supplier_id
		         WHERE o.legal_entity_id = $1::uuid AND o.status IN ('APPROVED', 'CLOSED') AND ($2::uuid IS NULL OR o.project_id = $2::uuid) AND ` + sf("o.project_id") + `) x
		       WHERE received > invoiced ORDER BY project, number`},

	{ID: "match-exceptions", Title: "Three-way match exceptions", Group: "Payables", Perm: "p2p.read", Filters: []string{"entity", "project", "from", "to"},
		Description: "Supplier invoices whose price or quantity did not match the order and receipts, with the reason recorded.",
		Columns: []ReportCol{col("date", "Invoice date", "date"), col("invoice", "Invoice", "text"), col("supplier", "Supplier", "text"), col("order", "Order", "text"),
			col("total", "Total", "money"), col("status", "Status", "text"), col("exception_reason", "Exception reason", "text")},
		Totals: []string{"total"},
		SQL: `SELECT i.invoice_date::text AS date, i.invoice_number AS invoice, s.name AS supplier, o.number AS "order", i.total::text AS total, i.status, COALESCE(i.exception_reason, '') AS exception_reason
		   FROM supplier_invoices i JOIN purchase_orders o ON o.id = i.po_id JOIN suppliers s ON s.id = i.supplier_id
		  WHERE o.legal_entity_id = $1::uuid AND i.match_status = 'EXCEPTION' AND ($2::uuid IS NULL OR o.project_id = $2::uuid) AND ` + sf("o.project_id") + ` AND ` + pf("i.invoice_date") + ` ORDER BY i.invoice_date, i.invoice_number`},

	{ID: "ipc-register", Title: "Payment certificate register", Group: "Revenue", Perm: "p2p.read", Filters: []string{"entity", "project", "from", "to"},
		Description: "Interim payment certificates by contract: value of work, retention, advance recovery, VAT and the amount due.",
		Columns: []ReportCol{col("period_end", "Period end", "date"), col("number", "Certificate", "text"), col("contract", "Contract", "text"), col("project", "Project", "text"),
			col("this_gross", "Work this period", "money"), col("retention", "Retention", "money"), col("advance_recovery", "Advance recovery", "money"), col("vat", "VAT", "money"),
			col("amount_due", "Amount due", "money"), col("status", "Status", "text")},
		Totals: []string{"this_gross", "retention", "advance_recovery", "vat", "amount_due"},
		SQL: `SELECT i.period_end::text AS period_end, i.number, c.number AS contract, pr.code AS project, i.this_gross::text AS this_gross, i.retention_amount::text AS retention,
		        i.advance_recovery::text AS advance_recovery, i.vat_amount::text AS vat, i.amount_due::text AS amount_due, i.status
		   FROM ipcs i JOIN contracts c ON c.id = i.contract_id JOIN projects pr ON pr.id = c.project_id
		  WHERE c.legal_entity_id = $1::uuid AND ($2::uuid IS NULL OR c.project_id = $2::uuid) AND ` + sf("c.project_id") + ` AND ` + pf("i.period_end") + ` ORDER BY i.period_end, i.number`},

	{ID: "retention-ledger", Title: "Retention ledger", Group: "Revenue", Perm: "p2p.read", Filters: []string{"entity", "project"},
		Description: "Retention withheld on approved customer invoices, released to date and still held, per contract.",
		Columns: []ReportCol{col("contract", "Contract", "text"), col("project", "Project", "text"), col("customer", "Customer", "text"), col("withheld", "Withheld", "money"),
			col("released", "Released", "money"), col("held", "Held", "money")},
		Totals: []string{"withheld", "released", "held"},
		SQL: `SELECT contract, project, customer, withheld::text, released::text, (withheld - released)::text AS held FROM (
		        SELECT c.number AS contract, pr.code AS project, cu.name AS customer,
		               COALESCE((SELECT sum(ci.retention_amount) FROM customer_invoices ci WHERE ci.contract_id = c.id AND ci.status = 'APPROVED'), 0) AS withheld,
		               COALESCE((SELECT sum(rr.amount) FROM retention_releases rr WHERE rr.contract_id = c.id AND rr.status = 'APPROVED'), 0) AS released
		          FROM contracts c JOIN projects pr ON pr.id = c.project_id JOIN customers cu ON cu.id = c.customer_id
		         WHERE c.legal_entity_id = $1::uuid AND ($2::uuid IS NULL OR c.project_id = $2::uuid) AND ` + sf("c.project_id") + `) x
		       WHERE withheld <> 0 OR released <> 0 ORDER BY project, contract`},

	{ID: "stock-valuation", Title: "Stock valuation by store", Group: "Stores", Perm: "p2p.read", Filters: []string{"entity", "project"},
		Description: "Quantity and value of stock on hand per store and item (FIFO or weighted average, as set for each item).",
		Columns: []ReportCol{col("warehouse", "Store", "text"), col("item_code", "Item", "text"), col("item_name", "Description", "text"), col("unit", "Unit", "text"),
			col("quantity", "Quantity", "qty"), col("value", "Value", "money")},
		Totals: []string{"value"},
		// stock_on_hand() is the one definition of stock on hand (FIFO layers or weighted
		// average per item); this report must never recompute it differently.
		SQL: `SELECT s.warehouse_code AS warehouse, s.item_code, s.item_name, s.unit, s.qty::text AS quantity, s.value::text AS value
		   FROM stock_on_hand($1::uuid) s WHERE s.qty <> 0 AND ($2::uuid IS NULL OR s.project_id = $2::uuid)
		    AND ($7 OR (s.project_id IS NOT NULL AND s.project_id = ANY($6::uuid[]))) ORDER BY s.warehouse_code, s.item_code`},

	{ID: "low-stock", Title: "Low stock and reorder suggestions", Group: "Stores", Perm: "p2p.read", Filters: []string{"entity", "project"},
		Description: "Items whose available stock (on hand minus reserved) is below the minimum set in the reorder rules, with the quantity to order to reach the maximum. A suggestion only: nothing orders automatically.",
		Columns: []ReportCol{col("warehouse", "Store", "text"), col("item_code", "Item", "text"), col("item_name", "Description", "text"), col("unit", "Unit", "text"),
			col("on_hand", "On hand", "qty"), col("reserved", "Reserved", "qty"), col("available", "Available", "qty"), col("min_qty", "Minimum", "qty"), col("max_qty", "Maximum", "qty"), col("suggested", "Suggested order", "qty")},
		SQL: `SELECT warehouse, item_code, item_name, unit, on_hand::text, reserved::text, (on_hand - reserved)::text AS available, min_qty::text, max_qty::text,
		        greatest(reorder_qty, max_qty - (on_hand - reserved))::text AS suggested FROM (
		        SELECT w.code AS warehouse, i.code AS item_code, i.name AS item_name, i.unit, COALESCE(b.qty, 0) AS on_hand, stock_reserved(i.id, w.id) AS reserved,
		               r.min_qty, r.max_qty, r.reorder_qty, w.project_id
		          FROM stock_reorder_rules r JOIN stock_items i ON i.id = r.stock_item_id JOIN warehouses w ON w.id = r.warehouse_id
		          LEFT JOIN stock_balances b ON b.stock_item_id = i.id AND b.warehouse_id = w.id
		         WHERE i.legal_entity_id = $1::uuid AND i.status = 'ACTIVE') x
		       WHERE on_hand - reserved < min_qty AND ($2::uuid IS NULL OR project_id = $2::uuid)
		         AND ($7 OR (project_id IS NOT NULL AND project_id = ANY($6::uuid[]))) ORDER BY warehouse, item_code`},
	{ID: "stock-movement", Title: "Stock movement history", Group: "Stores", Perm: "p2p.read", Filters: []string{"entity", "project", "from", "to"},
		Description: "Receipts, issues, transfers and count differences of stock in date order: what moved, where and at what value.",
		Columns: []ReportCol{col("date", "Date", "date"), col("kind", "Type", "text"), col("number", "Document", "text"), col("warehouse", "Store", "text"), col("item_code", "Item", "text"),
			col("quantity", "Quantity", "qty"), col("value", "Value", "money")},
		Totals: []string{"value"},
		SQL: `SELECT date::text AS date, kind, number, warehouse, item_code, quantity::text AS quantity, value::text AS value FROM (
		        SELECT r.created_at::date AS date, 'Put-away' AS kind, r.number, w.code AS warehouse, i.code AS item_code, r.quantity, r.amount AS value, w.project_id
		          FROM stock_receipts r JOIN warehouses w ON w.id = r.warehouse_id JOIN stock_items i ON i.id = r.stock_item_id WHERE r.legal_entity_id = $1::uuid
		        UNION ALL
		        SELECT s.issue_date, 'Issue', s.number, w.code, i.code, -s.quantity, -s.amount, w.project_id
		          FROM stock_issues s JOIN warehouses w ON w.id = s.warehouse_id JOIN stock_items i ON i.id = s.stock_item_id WHERE s.legal_entity_id = $1::uuid AND s.status = 'APPROVED'
		        UNION ALL
		        SELECT t.transfer_date, 'Transfer out', t.number, w.code, i.code, -t.quantity, -t.amount, w.project_id
		          FROM stock_transfers t JOIN warehouses w ON w.id = t.from_warehouse_id JOIN stock_items i ON i.id = t.stock_item_id WHERE t.legal_entity_id = $1::uuid AND t.status = 'APPROVED'
		        UNION ALL
		        SELECT t.transfer_date, 'Transfer in', t.number, w.code, i.code, t.quantity, t.amount, w.project_id
		          FROM stock_transfers t JOIN warehouses w ON w.id = t.to_warehouse_id JOIN stock_items i ON i.id = t.stock_item_id WHERE t.legal_entity_id = $1::uuid AND t.status = 'APPROVED'
		        UNION ALL
		        SELECT c.count_date, 'Count difference', c.number, w.code, i.code, c.difference, c.amount, w.project_id
		          FROM stock_counts c JOIN warehouses w ON w.id = c.warehouse_id JOIN stock_items i ON i.id = c.stock_item_id WHERE c.legal_entity_id = $1::uuid AND c.status = 'APPROVED') m
		       WHERE ($2::uuid IS NULL OR m.project_id = $2::uuid) AND ($7 OR (m.project_id IS NOT NULL AND m.project_id = ANY($6::uuid[]))) AND ` + pf("m.date") + ` ORDER BY m.date, m.number`},

	{ID: "payment-register", Title: "Payment register", Group: "Payables", Perm: "p2p.read", TenantWide: true, Filters: []string{"entity", "from", "to"},
		Description: "Every payment instruction with its status and who prepared, approved and released it. Bank details are never shown.",
		Columns: []ReportCol{col("value_date", "Value date", "date"), col("number", "Payment", "text"), col("supplier", "Supplier", "text"), col("status", "Status", "text"),
			col("amount", "Amount", "money"), col("prepared_by", "Prepared by", "text"), col("approved_by", "Approved by", "text"), col("second_approver", "Second approver", "text"), col("released_by", "Released by", "text")},
		Totals: []string{"amount"},
		SQL: `SELECT p.value_date::text AS value_date, p.number, s.name AS supplier, p.status, p.amount::text AS amount, p.created_by AS prepared_by, COALESCE(p.approved_by, '') AS approved_by,
		        COALESCE(p.second_approved_by, '') AS second_approver, COALESCE(p.released_by, '') AS released_by
		   FROM payment_instructions p JOIN suppliers s ON s.id = p.supplier_id
		  WHERE p.legal_entity_id = $1::uuid AND ` + pf("p.value_date") + ` ORDER BY p.value_date, p.number`},

	{ID: "budget-vs-actual", Title: "Budget against actual by month", Group: "Project controls", Perm: "p2p.read", Filters: []string{"entity", "project", "from", "to"},
		Description: "Posted cost per cost code and month, cumulative cost, and what is left of the current budget. Needs one project.",
		Columns: []ReportCol{col("cost_code", "Cost code", "text"), col("month", "Month", "text"), col("period_cost", "Cost this month", "money"), col("cumulative_cost", "Cumulative cost", "money"),
			col("current_budget", "Current budget", "money"), col("remaining", "Remaining budget", "money")},
		SQL: `SELECT cost_code, month, period_cost::text, cumulative::text AS cumulative_cost, budget::text AS current_budget, (budget - cumulative)::text AS remaining FROM (
		        SELECT c.code AS cost_code, to_char(date_trunc('month', j.business_event_at), 'YYYY-MM') AS month,
		               sum(CASE WHEN l.side = 'DEBIT' THEN l.amount ELSE -l.amount END) AS period_cost,
		               sum(sum(CASE WHEN l.side = 'DEBIT' THEN l.amount ELSE -l.amount END)) OVER (PARTITION BY c.id ORDER BY date_trunc('month', j.business_event_at)) AS cumulative,
		               COALESCE((SELECT pbc.current FROM project_budget_current pbc WHERE pbc.cost_code_id = c.id), 0) AS budget
		          FROM journal_lines l JOIN journals j ON j.id = l.journal_id JOIN cost_codes c ON c.id = l.cost_code_id
		         WHERE c.project_id = $2::uuid AND j.status IN ('POSTED', 'REVERSED') AND ` + sf("c.project_id") + `
		         GROUP BY c.id, c.code, date_trunc('month', j.business_event_at)) x
		       WHERE ($3::date IS NULL OR month >= to_char($3::date, 'YYYY-MM')) AND ($4::date IS NULL OR month <= to_char($4::date, 'YYYY-MM')) ORDER BY cost_code, month`},
}

// ReportCatalogue lists the reports the caller may run (permission and scope).
func (s *Service) ReportCatalogue(p auth.Principal) []ReportDef {
	out := []ReportDef{}
	for _, d := range reportDefs {
		if !s.authz.Has(p, auth.Permission(d.Perm)) {
			continue
		}
		if d.TenantWide && !s.tenantWide(p) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// ReportFilters are the parameters a catalogue report takes.
type ReportFilters struct {
	EntityID, ProjectID, SubjectID string
	From, To                       string // YYYY-MM-DD or empty
}

// ReportResult is a catalogue report's output.
type ReportResult struct {
	ID       string              `json:"id"`
	Title    string              `json:"title"`
	AsOf     string              `json:"as_of"`
	Filters  map[string]string   `json:"filters"`
	Columns  []ReportCol         `json:"columns"`
	Rows     []map[string]string `json:"rows"`
	Totals   map[string]string   `json:"totals,omitempty"`
	RowCount int                 `json:"row_count"`
	Cut      bool                `json:"cut,omitempty"` // more rows than ReportMaxRows existed
}

func isDate(s string) bool { _, err := time.Parse("2006-01-02", s); return s == "" || err == nil }

// RunReport runs one catalogue report for the caller.
func (s *Service) RunReport(ctx context.Context, p auth.Principal, id string, f ReportFilters) (ReportResult, error) {
	var def *ReportDef
	for i := range reportDefs {
		if reportDefs[i].ID == id {
			def = &reportDefs[i]
		}
	}
	if def == nil {
		return ReportResult{}, ErrNotFound
	}
	if err := s.authz.Require(p, auth.Permission(def.Perm), p.TenantID); err != nil {
		return ReportResult{}, err
	}
	if def.TenantWide && !s.tenantWide(p) {
		return ReportResult{}, fmt.Errorf("%w: this report covers the whole company and needs tenant-wide scope", auth.ErrForbidden)
	}
	if !isDate(f.From) || !isDate(f.To) {
		return ReportResult{}, fmt.Errorf("%w: dates are YYYY-MM-DD", ErrInvalidInput)
	}
	for _, v := range []string{f.EntityID, f.ProjectID, f.SubjectID} {
		if v != "" && !uuidForm(v) {
			return ReportResult{}, ErrNotFound
		}
	}
	if f.EntityID == "" {
		return ReportResult{}, fmt.Errorf("%w: choose a legal entity", ErrInvalidInput)
	}
	has := func(k string) bool {
		for _, x := range def.Filters {
			if x == k {
				return true
			}
		}
		return false
	}
	if def.Subject != "" && f.SubjectID == "" && def.ID != "gl-detail" {
		return ReportResult{}, fmt.Errorf("%w: choose a %s", ErrInvalidInput, def.Subject)
	}
	if def.ID == "budget-vs-actual" && f.ProjectID == "" {
		return ReportResult{}, fmt.Errorf("%w: choose a project", ErrInvalidInput)
	}
	scope := s.authz.ScopeOf(p)
	if f.ProjectID != "" && !scope.Allows(f.ProjectID) {
		return ReportResult{}, ErrNotFound // a project outside the caller's scope is indistinguishable from absent
	}
	projects := scope.Projects
	if projects == nil {
		projects = []string{}
	}
	var out ReportResult
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '15s'`); err != nil {
			return err
		}
		now, err := postgres.DBNow(ctx, tx)
		if err != nil {
			return err
		}
		out = ReportResult{ID: def.ID, Title: def.Title, AsOf: now.Format("2006-01-02 15:04 MST"), Columns: def.Columns, Rows: []map[string]string{}, Filters: map[string]string{}}
		nz := func(v string) any {
			if v == "" {
				return nil
			}
			return v
		}
		// The prefix names every parameter once, so a query that does not use all
		// of them is still valid (PostgreSQL must be able to type each $n).
		rows, err := tx.Query(ctx, reportParamPrefix+def.SQL+fmt.Sprintf(" LIMIT %d", ReportMaxRows+1), f.EntityID, nz(f.ProjectID), nz(f.From), nz(f.To), nz(f.SubjectID), projects, s.tenantWide(p))
		if err != nil {
			return err
		}
		defer rows.Close()
		names := rows.FieldDescriptions()
		sums := map[string]decimal.Decimal{}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				return err
			}
			if len(out.Rows) == ReportMaxRows {
				out.Cut = true
				break
			}
			row := make(map[string]string, len(vals))
			for i, v := range vals {
				row[names[i].Name] = reportText(v)
			}
			for _, k := range def.Totals {
				if d, err := decimal.NewFromString(row[k]); err == nil {
					sums[k] = sums[k].Add(d)
				}
			}
			out.Rows = append(out.Rows, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(def.Totals) > 0 {
			out.Totals = map[string]string{}
			for _, k := range def.Totals {
				out.Totals[k] = sums[k].StringFixed(2)
			}
		}
		out.RowCount = len(out.Rows)
		for _, k := range []struct{ name, v string }{{"entity", f.EntityID}, {"project", f.ProjectID}, {"subject", f.SubjectID}, {"from", f.From}, {"to", f.To}} {
			if k.v != "" && has(k.name) {
				out.Filters[k.name] = k.v
			}
		}
		return nil
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return ReportResult{}, fmt.Errorf("%w: the report took too long; narrow the dates or the project", ErrInvalidInput)
	}
	return out, err
}

func reportText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.Format("2006-01-02")
	default:
		return fmt.Sprint(x)
	}
}

func uuidForm(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'):
		default:
			return false
		}
	}
	return true
}

// reportIDs lists catalogue report ids in order (for tests and documentation).
func reportIDs() []string {
	ids := make([]string, 0, len(reportDefs))
	for _, d := range reportDefs {
		ids = append(ids, d.ID)
	}
	sort.Strings(ids)
	return ids
}

// ReportCatalogueMarkdown renders docs/reports/REPORT_CATALOGUE.md from the
// registry so the document cannot drift from the reports (a test checks it).
func ReportCatalogueMarkdown() string {
	out := "# Report catalogue\n\nGenerated from `internal/kernel/reportcat.go` (`reportDefs`); do not edit by hand. Regenerate with\n" +
		"`UPDATE_KPI_DOC=1 go test ./internal/kernel -run TestReportCatalogueDocumentIsCurrent`.\n\n" +
		"Every report is read-only, runs as the person asking (their permissions, project scope and row-level security),\n" +
		"shows the time its data is as of and the filters used, totals money in exact decimals, is limited to 5,000 rows\n" +
		"and exports to PDF, Word and Excel through `/v1/export` (never more than the screen).\n" +
		"API: `GET /v1/reports/catalogue` and `GET /v1/reports/catalogue/{id}?entity=&project=&subject=&from=&to=`.\n\n" +
		"| Report | Group | Needs | Scope | Filters | What it shows |\n|---|---|---|---|---|---|\n"
	for _, d := range reportDefs {
		scope := "your projects"
		if d.TenantWide {
			scope = "company-wide (tenant-wide scope)"
		}
		fl := ""
		for i, f := range d.Filters {
			if i > 0 {
				fl += ", "
			}
			fl += f
		}
		if d.Subject != "" {
			fl += " (" + d.Subject + ")"
		}
		out += "| `" + d.ID + "` " + d.Title + " | " + d.Group + " | `" + d.Perm + "` | " + scope + " | " + fl + " | " + d.Description + " |\n"
	}
	out += "\nPlus the core reports: trial balance, payables and receivables aging, reconciliation, project cost report, earned value, stock on hand and journals.\n"
	return out
}
