// Company setup checklist (WP-1.8; SRS ADM-01). A new company needs a series
// of things in place before it can run a project: policies approved, a chart
// of accounts, banks, a project with an approved WBS and BOQ, suppliers,
// customers and stock. This reads the state of each from the records
// themselves (nothing is ticked by hand), says who does it and why, and links
// to the page that does it. It reads only and changes nothing.
package kernel

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// SetupStep is one item of the checklist.
type SetupStep struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Title    string `json:"title"`
	Why      string `json:"why"`
	Who      string `json:"who"`
	Href     string `json:"href"`
	Count    int    `json:"count"`
	Done     bool   `json:"done"`
	Optional bool   `json:"optional,omitempty"`
}

type setupDef struct {
	id, group, title, why, who, href, sql string
	optional                              bool
}

var setupDefs = []setupDef{
	{"settings", "Company", "Company settings", "Name, letterhead on exports, language and time zone.", "Security administrator", "#/settings",
		`SELECT count(*) FROM tenant_settings WHERE updated_by IS NOT NULL`, false},
	{"accounts", "Finance", "Chart of accounts", "Every journal posts to accounts; stock, payables and receivables need their control accounts.", "Operator at setup, controller after", "#/journals",
		`SELECT count(*) FROM accounts WHERE active`, false},
	{"p2p_policy", "Finance", "Procure-to-pay policy approved", "Tolerances, VAT rate, approval thresholds and control accounts for buying.", "Controller proposes, CFO approves", "#/list/p2p_policy",
		`SELECT count(*) FROM p2p_policies WHERE status = 'APPROVED'`, false},
	{"contract_policy", "Finance", "Contract policy approved", "Retention, advance and VAT rules for client certificates and invoices.", "Controller proposes, CFO approves", "#/list/contract_policy",
		`SELECT count(*) FROM contract_policies WHERE status = 'APPROVED'`, false},
	{"fx_policy", "Finance", "FX policy approved", "Needed only if you transact in more than one currency.", "Treasury proposes, finance manager approves", "#/journals",
		`SELECT count(*) FROM fx_policies WHERE status = 'APPROVED'`, true},
	{"bank", "Finance", "Company bank account", "Payments are prepared against it and statements are imported for it.", "Treasury registers, CFO approves", "#/list/company_bank_account",
		`SELECT count(*) FROM company_bank_accounts WHERE status = 'ACTIVE'`, false},
	{"authority", "Governance", "Authority matrix", "Who may approve how much. Without one, approvals are limited only by roles.", "Process owner proposes, CFO approves", "#/authority",
		`SELECT count(*) FROM authority_matrices WHERE status = 'APPROVED'`, true},
	{"regions", "Structure", "Regions and business units", "Used to roll results up on the dashboard.", "Controller", "#/org",
		`SELECT (SELECT count(*) FROM regions) + (SELECT count(*) FROM business_units)`, true},
	{"calendar", "Structure", "Company calendar", "Holidays and exceptional working days (movable holidays differ every year).", "Controller", "#/org",
		`SELECT count(*) FROM calendar_exceptions WHERE removed_at IS NULL`, true},
	{"project", "Projects", "First project", "Everything is costed to a project.", "Commercial manager creates, CFO approves its gates", "#/projects",
		`SELECT count(*) FROM projects`, false},
	{"wbs", "Projects", "Approved WBS and cost codes", "Costs can only be charged to cost codes of an approved WBS.", "Project manager proposes, finance manager approves", "#/cost",
		`SELECT count(*) FROM wbs_versions WHERE status = 'APPROVED'`, false},
	{"boq", "Projects", "Approved BOQ or budget", "The budget that requisitions and orders are checked against.", "Estimator prepares, commercial manager approves", "#/list/boq_version",
		`SELECT count(*) FROM boq_versions WHERE status = 'APPROVED'`, false},
	{"suppliers", "Master data", "Approved suppliers", "Nobody can be asked to quote or be paid before they are approved. Import many at once from a file.", "Procurement officer, approved by the procurement manager", "#/imports",
		`SELECT count(*) FROM suppliers WHERE status = 'ACTIVE'`, false},
	{"customers", "Master data", "Approved customers (clients)", "Contracts and invoices need an approved customer. Import many at once from a file.", "AR clerk, approved by the finance manager", "#/imports",
		`SELECT count(*) FROM customers WHERE status = 'ACTIVE'`, false},
	{"warehouses", "Master data", "Warehouse or site store", "Where stock is kept.", "Procurement manager", "#/list/warehouse",
		`SELECT count(*) FROM warehouses WHERE status = 'ACTIVE'`, true},
	{"stock_items", "Master data", "Stock items", "Materials kept in store. Import many at once from a file.", "Procurement officer, approved by the finance manager", "#/imports",
		`SELECT count(*) FROM stock_items WHERE status = 'ACTIVE'`, true},
}

// SetupChecklist computes every step's state. Any signed-in person who reads
// procurement documents, or manages the company, may read it.
func (s *Service) SetupChecklist(ctx context.Context, p auth.Principal) ([]SetupStep, error) {
	if !s.authz.Has(p, auth.PermP2PRead) && !s.authz.Has(p, auth.PermTenantSettingsManage) && !s.authz.Has(p, auth.PermOrgManage) {
		return nil, auth.ErrForbidden
	}
	out := make([]SetupStep, 0, len(setupDefs))
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		for _, d := range setupDefs {
			var n int
			if err := tx.QueryRow(ctx, d.sql).Scan(&n); err != nil {
				return fmt.Errorf("setup step %s: %w", d.id, err)
			}
			out = append(out, SetupStep{ID: d.id, Group: d.group, Title: d.title, Why: d.why, Who: d.who, Href: d.href, Count: n, Done: n > 0, Optional: d.optional})
		}
		return nil
	})
	return out, err
}
