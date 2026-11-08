// Package auth implements the Enterprise Identity and Privilege Engine's
// deterministic authorization core (Pillar 3): role-based permissions,
// explicit data scope (tenant-wide vs project-limited), segregation-of-duties
// (SoD) rules and step-up (MFA) requirements.
//
// Authorization decisions never involve AI (LAW 01) and are evaluated on the
// server for every command.
package auth

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Permission is a fine-grained capability string "<resource>.<action>".
type Permission string

const (
	PermJournalCreate  Permission = "journal.create"
	PermJournalApprove Permission = "journal.approve"
	PermJournalPost    Permission = "journal.post"
	PermJournalReverse Permission = "journal.reverse"
	PermJournalRead    Permission = "journal.read"

	PermFXRead          Permission = "fx.read"
	PermFXManage        Permission = "fx.manage"            // maker: record rates, register sources, propose policies
	PermFXApprove       Permission = "fx.approve"           // checker: approve/reject/withdraw rates and sources
	PermFXApproveExcept Permission = "fx.approve_exception" // checker for rates outside the variance tolerance (MFA)
	PermFXPolicyApprove Permission = "fx.policy.approve"    // checker for FX policies

	PermAccountManage Permission = "account.manage"
	PermPeriodClose   Permission = "period.close"
	PermPeriodReopen  Permission = "period.reopen"
	PermAuditRead     Permission = "audit.read"

	// PermScopeTenantWide grants access to records that are not assigned to a
	// project (tenant-wide finance) and to every project. Without it a
	// principal sees only the projects listed in its ProjectIDs (SC-05).
	PermScopeTenantWide Permission = "scope.tenant_wide"

	// Project controls.
	PermWBSManage  Permission = "wbs.manage"  // propose WBS / cost-code versions
	PermWBSApprove Permission = "wbs.approve" // approve them (different person)

	// Workflow engine.
	PermWorkflowDefine  Permission = "workflow.define"  // propose a workflow definition
	PermWorkflowPublish Permission = "workflow.publish" // approve a definition (different person)
	PermWorkflowAdmin   Permission = "workflow.admin"   // console operations: retry effects, cancel instances
	PermWorkflowRead    Permission = "workflow.read"

	// PermApprovalHighValue is the executive sign-off used by workflow
	// threshold steps (e.g. journals at or above a materiality limit).
	PermApprovalHighValue Permission = "approval.high_value"

	// PermIdentityRevoke revokes other people's sessions (termination, role
	// removal, suspected compromise). Everyone may revoke their own.
	PermIdentityRevoke Permission = "identity.revoke_sessions"

	// Procure-to-pay (Journey A). Every pair maker/checker below is held by
	// different seed roles (TestDefaultRolesSeparateDuties).
	PermP2PRead             Permission = "p2p.read"
	PermP2PPolicyManage     Permission = "p2p.policy.manage"
	PermP2PPolicyApprove    Permission = "p2p.policy.approve"
	PermSupplierManage      Permission = "supplier.manage"
	PermSupplierApprove     Permission = "supplier.approve"
	PermSupplierBankApprove Permission = "supplier.bank.approve"
	PermBankAcctManage      Permission = "bankacct.manage"
	PermBankAcctApprove     Permission = "bankacct.approve"
	PermBOQManage           Permission = "boq.manage"
	PermBOQApprove          Permission = "boq.approve"
	PermPRCreate            Permission = "pr.create"
	PermPRApprove           Permission = "pr.approve"
	PermRFQManage           Permission = "rfq.manage"
	PermRFQEvaluate         Permission = "rfq.evaluate"
	PermRFQAward            Permission = "rfq.award"
	PermPOManage            Permission = "po.manage"
	PermPOApprove           Permission = "po.approve"
	PermBudgetOverride      Permission = "budget.override"
	PermBudgetRevise        Permission = "budget.revise"
	PermBudgetApprove       Permission = "budget.approve"
	PermBudgetTransfer      Permission = "budget.transfer"
	// Authority matrix, delegation and duty-conflict exceptions (0040).
	PermStockReserve       Permission = "stock.reserve"       // set stock aside for a project (0041)
	PermOrgManage          Permission = "org.manage"          // regions, business units, calendar exceptions (0042)
	PermProjectCreate      Permission = "project.create"      // create a project (starts PLANNED, phase BID)
	PermProjectUpdate      Permission = "project.update"      // edit a project's details
	PermProjectGateSubmit  Permission = "project.gate.submit" // prepare and submit a stage gate
	PermProjectGateDecide  Permission = "project.gate.decide" // approve or reject a stage gate (fresh MFA)
	PermForecastPrepare    Permission = "forecast.prepare"    // prepare and submit the monthly cost forecast (0044)
	PermForecastApprove    Permission = "forecast.approve"    // approve or reject a cost forecast (fresh MFA)
	PermChangeManage       Permission = "change.manage"       // raise and submit change events (0045)
	PermChangeApprove      Permission = "change.approve"      // approve or reject a change event (fresh MFA)
	PermClaimManage        Permission = "claim.manage"        // raise notices; prepare claims; record the client's answer (0046)
	PermClaimApprove       Permission = "claim.approve"       // approve or reject a claim for sending (fresh MFA)
	PermBondManage         Permission = "bond.manage"         // register guarantees; reduce or extend them (0050)
	PermBondApprove        Permission = "bond.approve"        // release a guarantee to the bank or record its call (fresh MFA)
	PermEscalationManage   Permission = "escalation.manage"   // indices, index values, formulas, price adjustments (0050)
	PermEscalationApprove  Permission = "escalation.approve"  // approve index values, formulas and price adjustments (fresh MFA)
	PermAuthorityManage    Permission = "authority.manage"    // maker: propose matrices and SoD exceptions
	PermAuthorityApprove   Permission = "authority.approve"   // checker (fresh MFA)
	PermDelegationCreate   Permission = "delegation.create"   // lend part of your own approval authority
	PermDelegationApprove  Permission = "delegation.approve"  // checker for delegations (fresh MFA)
	PermGRNCreate          Permission = "grn.create"
	PermGRNConfirm         Permission = "grn.confirm"
	PermAPEnter            Permission = "ap.enter"
	PermAPApprove          Permission = "ap.approve"
	PermAPApproveException Permission = "ap.approve_exception"
	PermPaymentPrepare     Permission = "payment.prepare"
	PermPaymentApprove     Permission = "payment.approve"
	PermPaymentRelease     Permission = "payment.release"
	PermBankImport         Permission = "bank.import"
	PermBankReconcile      Permission = "bank.reconcile"
	// PermBankDetailsReveal shows a full bank account number (with fresh MFA,
	// a stated purpose and an audit record). Everyone else sees it masked.
	PermBankDetailsReveal Permission = "bank.details.reveal"

	// Contract to cash (Journey B). Maker/checker pairs are held by
	// different seed roles (TestDefaultRolesSeparateDuties).
	PermCustomerManage        Permission = "customer.manage"
	PermCustomerApprove       Permission = "customer.approve"
	PermContractPolicyManage  Permission = "contract.policy.manage"
	PermContractPolicyApprove Permission = "contract.policy.approve"
	PermContractManage        Permission = "contract.manage"
	PermContractApprove       Permission = "contract.approve"
	PermMeasureCreate         Permission = "measure.create"
	PermMeasureVerify         Permission = "measure.verify"
	PermIPCPrepare            Permission = "ipc.prepare"
	PermIPCCertify            Permission = "ipc.certify"
	PermARInvoiceCreate       Permission = "ar.invoice.create"
	PermARInvoiceApprove      Permission = "ar.invoice.approve"
	PermARReceiptCreate       Permission = "ar.receipt.create"
	PermARReceiptConfirm      Permission = "ar.receipt.confirm"
	PermARCreditCreate        Permission = "ar.credit.create"
	PermARCreditApprove       Permission = "ar.credit.approve"
	PermARRetentionCreate     Permission = "ar.retention.create"
	PermARRetentionApprove    Permission = "ar.retention.approve"

	// Stock valuation (Journey C). Maker/checker pairs are held by different
	// seed roles (TestDefaultRolesSeparateDuties).
	PermStockItemManage      Permission = "stock.item.manage"
	PermStockItemApprove     Permission = "stock.item.approve"
	PermWarehouseManage      Permission = "warehouse.manage"
	PermStockReceive         Permission = "stock.receive"
	PermStockIssue           Permission = "stock.issue"
	PermStockIssueApprove    Permission = "stock.issue.approve"
	PermStockCount           Permission = "stock.count"
	PermStockAdjustApprove   Permission = "stock.adjust.approve"
	PermStockTransfer        Permission = "stock.transfer"
	PermStockTransferApprove Permission = "stock.transfer.approve"
	PermScheduleManage       Permission = "schedule.manage"
	PermScheduleApprove      Permission = "schedule.approve"

	// Site operations and offline capture (GATED-01, migration 0036).
	PermSiteDiaryManage    Permission = "site.diary.manage"
	PermSiteDiaryApprove   Permission = "site.diary.approve"
	PermSiteDeviceRegister Permission = "site.device.register"
	// PermSiteDeviceManage revokes anyone's field device (identity
	// administration); an owner revokes their own with PermSiteDeviceRegister.
	PermSiteDeviceManage Permission = "site.device.manage"

	// Platform (migration 0038): tenant portal settings, automation rules
	// (maker/checker), intelligence. None of these can decide a financial
	// document; they configure, observe and recommend.
	PermTenantSettingsManage Permission = "tenant.settings.manage"
	PermAutomationManage     Permission = "automation.manage"
	PermAutomationActivate   Permission = "automation.activate"
	PermIntelligenceRead     Permission = "intelligence.read"
	// People and access (0048): keep the company directory, give and take
	// roles, compose custom roles from this permission list.
	PermUserManage Permission = "user.manage"
	PermRoleManage Permission = "role.manage"
)

// Role is a named permission bundle. Roles are tenant-scoped data in
// production; the defaults below are the seed set.
type Role struct {
	Name        string
	Permissions []Permission
}

// DefaultRoles is the seed role catalogue. Separation baselines enforced by
// tests: no role holds two of journal create/approve/post; no role holds both
// fx.manage and fx.approve; no role both defines and publishes workflows.
var DefaultRoles = map[string]Role{
	"accountant": {Name: "accountant", Permissions: []Permission{PermIntelligenceRead, PermJournalCreate, PermJournalRead, PermFXRead, PermScopeTenantWide, PermWorkflowRead,
		PermBankReconcile, PermP2PRead}},
	// Treasury prepares and releases payments (two different officers: the
	// database refuses release by the preparer). It does not import bank
	// statements: the evidence that settles a payment comes from someone
	// independent of the payment (review 3, H2).
	"treasury_officer": {Name: "treasury_officer", Permissions: []Permission{PermIntelligenceRead, PermFXManage, PermFXRead, PermScopeTenantWide, PermWorkflowRead,
		PermPaymentPrepare, PermPaymentRelease, PermBankAcctManage, PermBondManage, PermP2PRead}},
	"bank_statement_officer": {Name: "bank_statement_officer", Permissions: []Permission{PermBankImport, PermScopeTenantWide, PermP2PRead}},
	"finance_manager": {Name: "finance_manager", Permissions: []Permission{PermDelegationCreate, PermIntelligenceRead, PermJournalApprove, PermJournalRead, PermFXRead, PermFXApprove,
		PermFXPolicyApprove, PermWBSApprove, PermScopeTenantWide, PermWorkflowRead, PermAPApproveException, PermPaymentApprove,
		PermSupplierBankApprove, PermBankDetailsReveal, PermCustomerApprove, PermStockItemApprove, PermP2PRead}},
	"controller": {Name: "controller", Permissions: []Permission{PermOrgManage, PermDelegationCreate, PermIntelligenceRead, PermJournalPost, PermJournalReverse, PermJournalRead, PermPeriodClose,
		PermAccountManage, PermFXRead, PermScopeTenantWide, PermWorkflowRead, PermP2PPolicyManage, PermContractPolicyManage, PermP2PRead}},
	"cfo": {Name: "cfo", Permissions: []Permission{PermProjectGateDecide, PermForecastApprove, PermIntelligenceRead, PermJournalRead, PermFXRead, PermFXApprove, PermFXApproveExcept, PermFXPolicyApprove,
		PermPeriodReopen, PermAuditRead, PermScopeTenantWide, PermWorkflowRead, PermWorkflowPublish, PermApprovalHighValue,
		PermPaymentApprove, PermP2PPolicyApprove, PermBudgetOverride, PermBudgetApprove, PermAuthorityApprove, PermDelegationCreate, PermBankAcctApprove, PermBankDetailsReveal, PermContractApprove, PermContractPolicyApprove, PermP2PRead}},
	"auditor":            {Name: "auditor", Permissions: []Permission{PermIntelligenceRead, PermJournalRead, PermAuditRead, PermFXRead, PermScopeTenantWide, PermWorkflowRead, PermP2PRead}},
	"project_manager":    {Name: "project_manager", Permissions: []Permission{PermProjectUpdate, PermProjectGateSubmit, PermForecastPrepare, PermChangeManage, PermClaimManage, PermStockReserve, PermDelegationCreate, PermIntelligenceRead, PermJournalRead, PermFXRead, PermWBSManage, PermWorkflowRead, PermPRApprove, PermStockIssueApprove, PermScheduleApprove, PermSiteDiaryApprove, PermBudgetRevise, PermBudgetTransfer, PermP2PRead}},
	"project_accountant": {Name: "project_accountant", Permissions: []Permission{PermJournalCreate, PermJournalRead, PermFXRead, PermWorkflowRead}},
	"process_owner":      {Name: "process_owner", Permissions: []Permission{PermAuthorityManage, PermWorkflowDefine, PermWorkflowRead, PermAutomationManage, PermIntelligenceRead, PermScopeTenantWide}},
	"workflow_admin":     {Name: "workflow_admin", Permissions: []Permission{PermWorkflowAdmin, PermWorkflowRead, PermAutomationActivate, PermIntelligenceRead, PermScopeTenantWide}},
	// Identity administration holds no financial permission at all.
	"security_admin": {Name: "security_admin", Permissions: []Permission{PermUserManage, PermRoleManage, PermDelegationApprove, PermIdentityRevoke, PermAuditRead, PermSiteDeviceManage, PermTenantSettingsManage, PermScopeTenantWide}},
	// Journey A operational roles.
	"estimator":           {Name: "estimator", Permissions: []Permission{PermBOQManage, PermP2PRead}},
	"commercial_manager":  {Name: "commercial_manager", Permissions: []Permission{PermProjectCreate, PermForecastApprove, PermChangeApprove, PermClaimApprove, PermBondApprove, PermEscalationApprove, PermDelegationCreate, PermIntelligenceRead, PermBOQApprove, PermBudgetApprove, PermContractManage, PermIPCCertify, PermARCreditApprove, PermARRetentionApprove, PermP2PRead}},
	"site_engineer":       {Name: "site_engineer", Permissions: []Permission{PermStockReserve, PermPRCreate, PermGRNConfirm, PermRFQEvaluate, PermMeasureVerify, PermStockIssueApprove, PermSiteDiaryManage, PermSiteDeviceRegister, PermP2PRead}},
	"storekeeper":         {Name: "storekeeper", Permissions: []Permission{PermStockReserve, PermGRNCreate, PermStockReceive, PermStockIssue, PermStockCount, PermStockTransfer, PermP2PRead}},
	"procurement_officer": {Name: "procurement_officer", Permissions: []Permission{PermSupplierManage, PermRFQManage, PermPOManage, PermStockItemManage, PermP2PRead}},
	"procurement_manager": {Name: "procurement_manager", Permissions: []Permission{PermDelegationCreate, PermIntelligenceRead, PermSupplierApprove, PermRFQAward, PermPOApprove, PermWarehouseManage, PermP2PRead}},
	"ap_clerk":            {Name: "ap_clerk", Permissions: []Permission{PermAPEnter, PermP2PRead}},
	"ap_supervisor":       {Name: "ap_supervisor", Permissions: []Permission{PermAPApprove, PermP2PRead}},
	// Journey B operational roles.
	"quantity_surveyor": {Name: "quantity_surveyor", Permissions: []Permission{PermChangeManage, PermClaimManage, PermEscalationManage, PermIntelligenceRead, PermMeasureCreate, PermIPCPrepare, PermP2PRead}},
	"ar_clerk": {Name: "ar_clerk", Permissions: []Permission{PermCustomerManage, PermARInvoiceCreate, PermARReceiptCreate, PermARCreditCreate, PermARRetentionCreate, PermScopeTenantWide,
		PermP2PRead}},
	// Approves stock count adjustments. Separate from the journal chain: the
	// adjustment journal it generates is approved by finance and posted by a
	// controller, and its preparer may do neither.
	"inventory_controller": {Name: "inventory_controller", Permissions: []Permission{PermStockAdjustApprove, PermStockTransferApprove, PermScopeTenantWide, PermP2PRead}},
	// Plans and reports progress; a project manager approves (GATED-02).
	"planner":       {Name: "planner", Permissions: []Permission{PermIntelligenceRead, PermScheduleManage, PermP2PRead}},
	"ar_supervisor": {Name: "ar_supervisor", Permissions: []Permission{PermARInvoiceApprove, PermARReceiptConfirm, PermScopeTenantWide, PermP2PRead}},
}

// Principal kinds, as asserted by the identity provider (token claim
// "pkind"). Only KindHuman principals may approve or release payments; an
// absent or unknown kind is never treated as human.
const (
	KindHuman   = "human"
	KindService = "service"
	KindAI      = "ai"
)

// DraftActions are the attested actions a principal that is not asserted
// human (an AI assistant, an integration service, or an unknown kind) may
// perform: preparing and submitting drafts, recording received quotes,
// importing bank statements and starting workflows. Every other action
// approves, certifies, verifies, confirms, posts, releases, closes or
// configures something and is reserved to people; new actions are therefore
// human-only by default. The database applies the same rule to every
// attestation (migration 0019, approval_is_draft_action).
func IsDraftAction(action string) bool {
	switch action {
	case "rfq.quote.record", "bank.import", "workflow.start":
		return true
	case "stock.receipt.create":
		// A put-away is immediately effective valuation with no checker
		// step (R7-F5): a human decision despite its name.
		return false
	}
	return strings.HasSuffix(action, ".create") || strings.HasSuffix(action, ".submit")
}

// ReservedPermissionPrefix marks permissions that only the platform derives
// from verified identity attributes (e.g. "principal.human"); a role can never
// grant them.
const ReservedPermissionPrefix = "principal."

// Principal is the authenticated caller bound to exactly one tenant.
type Principal struct {
	// Kind is the identity provider's assertion of what the principal is
	// (KindHuman, KindService, KindAI); "" means unknown.
	Kind     string
	UserID   string
	TenantID string
	Roles    []string
	// ProjectIDs lists the projects the principal may act on. Combined with
	// the role entitlement scope.tenant_wide it yields the effective Scope.
	ProjectIDs []string
	// MFA is true when the identity provider asserted a multi-factor
	// authentication method for this session (OIDC "amr").
	MFA bool
	// AuthTime is when the user last actively authenticated (OIDC "auth_time").
	AuthTime time.Time
	// Delegated holds authority lent to this person by colleagues (delegation
	// of authority, 0040): permission -> the delegators who lent it. It is
	// filled by the kernel from approved, current delegations, never from the
	// token, and it never contains a permission the delegator could not use.
	Delegated map[Permission][]string
	// Custom holds the permissions of the company's own roles (0048), filled
	// by the kernel from the people directory, never from the token:
	// permission -> the custom role codes that grant it.
	Custom map[Permission][]string
	// SessionID is the token identifier (JWT "jti"); IssuedAt is the token's
	// "iat". Both are checked against the shared revocation list on every
	// request and inside every approval transaction.
	SessionID string
	IssuedAt  time.Time
}

// Errors.
var (
	ErrUnauthenticated = errors.New("auth: unauthenticated")
	ErrForbidden       = errors.New("auth: forbidden")
	ErrTenantMismatch  = errors.New("auth: resource belongs to another tenant")
	ErrSoD             = errors.New("auth: segregation of duties violation")
	ErrStepUpRequired  = errors.New("auth: recent multi-factor authentication required")
)

// Authorizer evaluates permissions against a role catalogue.
type Authorizer struct {
	roles map[string]Role
	// RequireDistinctPoster requires poster != approver in addition to the
	// always-on approver != preparer rule.
	RequireDistinctPoster bool
	// StepUpMaxAge bounds how old an MFA authentication may be for
	// exceptional approvals. Default 10 minutes.
	StepUpMaxAge time.Duration
	now          func() time.Time
}

// NewAuthorizer builds an authorizer over a role catalogue.
func NewAuthorizer(roles map[string]Role, requireDistinctPoster bool) *Authorizer {
	if roles == nil {
		roles = DefaultRoles
	}
	return &Authorizer{roles: roles, RequireDistinctPoster: requireDistinctPoster, StepUpMaxAge: 10 * time.Minute, now: time.Now}
}

// Catalogue returns each role of this authorizer with its permissions. The
// console uses it to explain pages ("who can approve this"); it reveals no
// user, tenant or data, only the role definitions in force.
func (a *Authorizer) Catalogue() map[string][]Permission {
	out := make(map[string][]Permission, len(a.roles))
	for name, r := range a.roles {
		out[name] = append([]Permission(nil), r.Permissions...)
	}
	return out
}

// KnownPermissions is every permission some role of the catalogue grants.
// Workflow definitions may only reference these.
func (a *Authorizer) KnownPermissions() map[Permission]bool {
	out := map[Permission]bool{}
	for _, r := range a.roles {
		for _, p := range r.Permissions {
			out[p] = true
		}
	}
	return out
}

// Permissions returns the sorted, de-duplicated permission set of a principal.
func (a *Authorizer) Permissions(p Principal) []Permission {
	set := map[Permission]struct{}{}
	for _, r := range p.Roles {
		if role, ok := a.roles[r]; ok {
			for _, perm := range role.Permissions {
				if strings.HasPrefix(string(perm), ReservedPermissionPrefix) {
					continue // never grantable through a role
				}
				set[perm] = struct{}{}
			}
		}
	}
	for perm := range p.Delegated {
		if !strings.HasPrefix(string(perm), ReservedPermissionPrefix) {
			set[perm] = struct{}{}
		}
	}
	for perm := range p.Custom {
		if !strings.HasPrefix(string(perm), ReservedPermissionPrefix) {
			set[perm] = struct{}{}
		}
	}
	out := make([]Permission, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Has reports whether the principal holds the permission.
func (a *Authorizer) Has(p Principal, perm Permission) bool {
	if (len(p.Delegated[perm]) > 0 || len(p.Custom[perm]) > 0) && !strings.HasPrefix(string(perm), ReservedPermissionPrefix) {
		return true
	}
	for _, r := range p.Roles {
		role, ok := a.roles[r]
		if !ok {
			continue
		}
		for _, x := range role.Permissions {
			if x == perm && !strings.HasPrefix(string(x), ReservedPermissionPrefix) {
				return true
			}
		}
	}
	return false
}

// Require returns ErrForbidden unless the principal holds the permission and
// ErrTenantMismatch if the resource tenant differs from the principal's.
func (a *Authorizer) Require(p Principal, perm Permission, resourceTenant string) error {
	if p.UserID == "" || p.TenantID == "" {
		return ErrUnauthenticated
	}
	if resourceTenant != "" && resourceTenant != p.TenantID {
		// Deliberately indistinguishable from "not found" at the API layer;
		// here we keep the precise cause for audit logging.
		return ErrTenantMismatch
	}
	if !a.Has(p, perm) {
		return fmt.Errorf("%w: %s", ErrForbidden, perm)
	}
	return nil
}

// Scope is a principal's effective data scope.
type Scope struct {
	// TenantWide principals may access unassigned (project-less) records and
	// every project of the tenant.
	TenantWide bool
	// Projects lists the projects a non-tenant-wide principal may access. An
	// empty list with TenantWide=false means no project data at all.
	Projects []string
}

// ScopeOf derives the effective scope. Tenant-wide access is never implied by
// an empty project list: it requires the explicit scope.tenant_wide
// entitlement AND no project restriction on the session. A token that lists
// projects is always restricted to them, whatever its roles (SC-05).
func (a *Authorizer) ScopeOf(p Principal) Scope {
	if len(p.ProjectIDs) == 0 && a.Has(p, PermScopeTenantWide) {
		return Scope{TenantWide: true}
	}
	return Scope{Projects: append([]string(nil), p.ProjectIDs...)}
}

// Allows reports whether the scope admits a record with the given project
// ("" = unassigned, tenant-wide record).
func (s Scope) Allows(projectID string) bool {
	if s.TenantWide {
		return true
	}
	if projectID == "" {
		return false
	}
	for _, id := range s.Projects {
		if id == projectID {
			return true
		}
	}
	return false
}

// RequireProject enforces the permission and the principal's data scope for a
// record belonging to projectID ("" = unassigned).
func (a *Authorizer) RequireProject(p Principal, perm Permission, resourceTenant, projectID string) error {
	if err := a.Require(p, perm, resourceTenant); err != nil {
		return err
	}
	if !a.ScopeOf(p).Allows(projectID) {
		if projectID == "" {
			return fmt.Errorf("%w: tenant-wide (unassigned) records require %s", ErrForbidden, PermScopeTenantWide)
		}
		return fmt.Errorf("%w: project %s is outside the principal's scope", ErrForbidden, projectID)
	}
	return nil
}

// RequireStepUp demands a recent multi-factor authentication.
func (a *Authorizer) RequireStepUp(p Principal) error {
	if !p.MFA || p.AuthTime.IsZero() || a.now().Sub(p.AuthTime) > a.StepUpMaxAge {
		return fmt.Errorf("%w (within %s)", ErrStepUpRequired, a.StepUpMaxAge)
	}
	return nil
}

// JournalActors are the recorded workflow participants of a journal.
type JournalActors struct {
	CreatedBy  string
	ApprovedBy string
}

// CheckSoDApprove enforces that the approver is not the preparer.
func (a *Authorizer) CheckSoDApprove(p Principal, actors JournalActors) error {
	if actors.CreatedBy == p.UserID {
		return fmt.Errorf("%w: preparer cannot approve their own journal", ErrSoD)
	}
	return nil
}

// CheckSoDPost enforces the posting separation rules.
func (a *Authorizer) CheckSoDPost(p Principal, actors JournalActors) error {
	if actors.ApprovedBy == "" {
		return fmt.Errorf("%w: journal has no approver", ErrSoD)
	}
	if a.RequireDistinctPoster && actors.ApprovedBy == p.UserID {
		return fmt.Errorf("%w: approver cannot also post", ErrSoD)
	}
	if actors.CreatedBy == p.UserID && a.RequireDistinctPoster {
		return fmt.Errorf("%w: preparer cannot post their own journal", ErrSoD)
	}
	return nil
}

// CheckMakerChecker enforces that a checker is not the maker of a record.
func CheckMakerChecker(checker Principal, maker, what string) error {
	if checker.UserID == maker {
		return fmt.Errorf("%w: the creator of a %s cannot approve or reject it", ErrSoD, what)
	}
	// Acting on a delegation is acting for the delegator: a delegate never
	// decides what their delegator prepared (that would be self-approval by proxy).
	for _, from := range checker.Delegated {
		for _, d := range from {
			if d == maker {
				return fmt.Errorf("%w: you hold delegated authority from the creator of this %s, who therefore cannot be approved through you", ErrSoD, what)
			}
		}
	}
	return nil
}

// ScopeMarkers are the platform-derived permission markers that bind an
// approval attestation to the principal's data scope and session (R10-F1,
// R10-F2). The kernel adds them to every attestation; the isolated signer
// signs only markers the person's token supports; the database refuses an
// attestation whose session is revoked or whose subject's project is not in
// the attested scope. They are never grantable by a role (ReservedPermissionPrefix).
func (a *Authorizer) ScopeMarkers(p Principal) ([]string, error) {
	var out []string
	sc := a.ScopeOf(p)
	if sc.TenantWide {
		out = append(out, ReservedPermissionPrefix+"scope:tenant")
	}
	for _, id := range sc.Projects {
		if id == "" || strings.ContainsAny(id, ",\x1f") {
			return nil, fmt.Errorf("%w: malformed project id in the session", ErrForbidden)
		}
		out = append(out, ReservedPermissionPrefix+"project:"+id)
	}
	sid := p.SessionID
	if sid == "" {
		sid = "-"
	}
	if strings.ContainsAny(sid, ",\x1f") || len(sid) > 200 {
		return nil, fmt.Errorf("%w: malformed session id", ErrForbidden)
	}
	var iat int64
	if !p.IssuedAt.IsZero() {
		iat = p.IssuedAt.Unix()
	}
	out = append(out, ReservedPermissionPrefix+"session:"+sid, fmt.Sprintf("%siat:%d", ReservedPermissionPrefix, iat))
	return out, nil
}

// ---------------------------------------------------------------------------
// Delegation of authority and segregation-of-duties analysis (0040)
// ---------------------------------------------------------------------------

// NonDelegable are permissions that are never lent: they control who may do
// what, or are the last resort against the controls themselves.
var NonDelegable = map[Permission]bool{
	PermScopeTenantWide: true, PermIdentityRevoke: true, PermDelegationApprove: true,
	PermAuthorityManage: true, PermAuthorityApprove: true, PermBudgetOverride: true, PermTenantSettingsManage: true, PermProjectGateDecide: true, PermForecastApprove: true,
}

// SoDConflict is a set of permissions one person should not hold together:
// holding two or more of Perms breaks the maker/checker (or checker/poster)
// separation of the control.
type SoDConflict struct {
	Control string
	Perms   []Permission
}

// DefaultSoDConflicts is the company-neutral conflict catalogue. The role
// catalogue is tested against it (no seed role holds a conflicting set), the
// kernel strips delegated permissions that would create a conflict, and the
// conflict report lists live conflicts. A company may accept a specific one
// for a named person with an approved, expiring exception (sod_exceptions).
var DefaultSoDConflicts = []SoDConflict{
	{"Journals: prepare, approve, post", []Permission{PermJournalCreate, PermJournalApprove, PermJournalPost}},
	{"FX rates", []Permission{PermFXManage, PermFXApprove}},
	{"FX policy", []Permission{PermFXManage, PermFXPolicyApprove}},
	{"Workflow definitions", []Permission{PermWorkflowDefine, PermWorkflowPublish}},
	{"Period close and reopen", []Permission{PermPeriodClose, PermPeriodReopen}},
	{"WBS and cost codes", []Permission{PermWBSManage, PermWBSApprove}},
	{"Supplier master", []Permission{PermSupplierManage, PermSupplierApprove}},
	{"Supplier bank accounts", []Permission{PermSupplierManage, PermSupplierBankApprove}},
	{"Company bank accounts", []Permission{PermBankAcctManage, PermBankAcctApprove}},
	{"BOQ", []Permission{PermBOQManage, PermBOQApprove}},
	{"Requisitions", []Permission{PermPRCreate, PermPRApprove}},
	{"RFQ: manage, award", []Permission{PermRFQManage, PermRFQAward}},
	{"RFQ: manage, evaluate", []Permission{PermRFQManage, PermRFQEvaluate}},
	{"Purchase orders", []Permission{PermPOManage, PermPOApprove}},
	{"Goods receipt", []Permission{PermGRNCreate, PermGRNConfirm}},
	{"Supplier invoices", []Permission{PermAPEnter, PermAPApprove}},
	{"Invoice price exceptions", []Permission{PermAPEnter, PermAPApproveException}},
	{"Payments: prepare, approve", []Permission{PermPaymentPrepare, PermPaymentApprove}},
	{"Payments: approve, release", []Permission{PermPaymentApprove, PermPaymentRelease}},
	{"Payments: release, reconcile", []Permission{PermPaymentRelease, PermBankReconcile}},
	{"P2P policy", []Permission{PermP2PPolicyManage, PermP2PPolicyApprove}},
	{"Bank statements and payments", []Permission{PermBankImport, PermBankReconcile}},
	{"Bank import and payment prepare", []Permission{PermBankImport, PermPaymentPrepare}},
	{"Bank import and payment approve", []Permission{PermBankImport, PermPaymentApprove}},
	{"Bank import and payment release", []Permission{PermBankImport, PermPaymentRelease}},
	{"Customers", []Permission{PermCustomerManage, PermCustomerApprove}},
	{"Contract policy", []Permission{PermContractPolicyManage, PermContractPolicyApprove}},
	{"Contracts", []Permission{PermContractManage, PermContractApprove}},
	{"Measurements", []Permission{PermMeasureCreate, PermMeasureVerify}},
	{"Payment certificates", []Permission{PermIPCPrepare, PermIPCCertify}},
	{"Customer invoices", []Permission{PermARInvoiceCreate, PermARInvoiceApprove}},
	{"Customer receipts", []Permission{PermARReceiptCreate, PermARReceiptConfirm}},
	{"Credit notes", []Permission{PermARCreditCreate, PermARCreditApprove}},
	{"Retention", []Permission{PermARRetentionCreate, PermARRetentionApprove}},
	{"Stock transfers", []Permission{PermStockTransfer, PermStockTransferApprove}},
	{"Schedules", []Permission{PermScheduleManage, PermScheduleApprove}},
	{"Bank import and receipts", []Permission{PermBankImport, PermARReceiptConfirm}},
	{"Bank import and receipt creation", []Permission{PermBankImport, PermARReceiptCreate}},
	{"Stock items", []Permission{PermStockItemManage, PermStockItemApprove}},
	{"Stock issues", []Permission{PermStockIssue, PermStockIssueApprove}},
	{"Stock counts", []Permission{PermStockCount, PermStockAdjustApprove}},
	{"Receive goods, approve issues", []Permission{PermGRNCreate, PermStockIssueApprove}},
	{"Site diaries", []Permission{PermSiteDiaryManage, PermSiteDiaryApprove}},
	{"Site devices", []Permission{PermSiteDeviceRegister, PermSiteDeviceManage}},
	{"Budget revisions", []Permission{PermBudgetRevise, PermBudgetApprove}},
	{"Budget transfers", []Permission{PermBudgetTransfer, PermBudgetApprove}},
	{"Authority matrix and exceptions", []Permission{PermAuthorityManage, PermAuthorityApprove}},
	{"Delegations", []Permission{PermDelegationCreate, PermDelegationApprove}},
	{"Project stage gates", []Permission{PermProjectGateSubmit, PermProjectGateDecide}},
	{"Cost forecasts", []Permission{PermForecastPrepare, PermForecastApprove}},
	{"Change events", []Permission{PermChangeManage, PermChangeApprove}},
	{"Claims", []Permission{PermClaimManage, PermClaimApprove}},
	{"Guarantees", []Permission{PermBondManage, PermBondApprove}},
	{"Price adjustment", []Permission{PermEscalationManage, PermEscalationApprove}},
}

// SoDPair is one violated conflict for one person.
type SoDPair struct {
	Control string
	A, B    Permission // the two permissions held (sorted, A < B)
}

// SoDViolations lists the conflicting pairs present in a permission set,
// except those a person holds under an approved exception (by sorted pair).
func SoDViolations(held map[Permission]bool, accepted map[[2]Permission]bool) []SoDPair {
	var out []SoDPair
	for _, c := range DefaultSoDConflicts {
		var have []Permission
		for _, p := range c.Perms {
			if held[p] {
				have = append(have, p)
			}
		}
		for i := 0; i < len(have); i++ {
			for j := i + 1; j < len(have); j++ {
				a, b := have[i], have[j]
				if a > b {
					a, b = b, a
				}
				if accepted[[2]Permission{a, b}] {
					continue
				}
				out = append(out, SoDPair{Control: c.Control, A: a, B: b})
			}
		}
	}
	return out
}

// DelegationAllowed reports whether delegator may lend perm: they must hold
// it themselves (by role, not by an earlier delegation) and it must be
// delegable. A person never re-delegates what was lent to them.
func (a *Authorizer) DelegationAllowed(delegator Principal, perm Permission) error {
	if NonDelegable[perm] || strings.HasPrefix(string(perm), ReservedPermissionPrefix) {
		return fmt.Errorf("%w: %s can never be delegated", ErrForbidden, perm)
	}
	own := delegator
	own.Delegated = nil
	if !a.Has(own, perm) {
		return fmt.Errorf("%w: you cannot delegate %s: you do not hold it yourself", ErrForbidden, perm)
	}
	return nil
}

// RoleMatrixMarkdown renders docs/security/ROLE_PERMISSION_MATRIX.md from the
// role catalogue, so the document is the code: a test fails if they differ.
func RoleMatrixMarkdown() string {
	roles := make([]string, 0, len(DefaultRoles))
	perms := map[Permission]bool{}
	for n, r := range DefaultRoles {
		roles = append(roles, n)
		for _, p := range r.Permissions {
			perms[p] = true
		}
	}
	sort.Strings(roles)
	plist := make([]string, 0, len(perms))
	for p := range perms {
		plist = append(plist, string(p))
	}
	sort.Strings(plist)
	var b strings.Builder
	b.WriteString("# Role and permission matrix\n\nGenerated from `pkg/auth/auth.go` (`DefaultRoles`); do not edit by hand. Regenerate with\n" +
		"`UPDATE_ROLE_DOC=1 go test ./pkg/auth -run TestRoleMatrixDocumentIsCurrent`.\n\n" +
		"A tenant may define its own roles from the same permission list; these are the seed roles. A role never holds both halves of a control:\n" +
		"the conflict catalogue below is tested against every role.\n\n## Roles and what they may do\n\n")
	for _, n := range roles {
		ps := make([]string, 0, len(DefaultRoles[n].Permissions))
		for _, p := range DefaultRoles[n].Permissions {
			ps = append(ps, "`"+string(p)+"`")
		}
		sort.Strings(ps)
		b.WriteString("### " + n + "\n\n" + strings.Join(ps, ", ") + "\n\n")
	}
	b.WriteString("## Who holds each permission\n\n| Permission | Roles |\n|---|---|\n")
	for _, p := range plist {
		var who []string
		for _, n := range roles {
			for _, rp := range DefaultRoles[n].Permissions {
				if string(rp) == p {
					who = append(who, n)
				}
			}
		}
		b.WriteString("| `" + p + "` | " + strings.Join(who, ", ") + " |\n")
	}
	b.WriteString("\n## Duties that must be kept apart (no role, and no person, may hold both)\n\n| Control | Permissions |\n|---|---|\n")
	for _, c := range DefaultSoDConflicts {
		ps := make([]string, 0, len(c.Perms))
		for _, p := range c.Perms {
			ps = append(ps, "`"+string(p)+"`")
		}
		b.WriteString("| " + c.Control + " | " + strings.Join(ps, " + ") + " |\n")
	}
	b.WriteString("\n## Never delegable\n\n")
	nd := make([]string, 0, len(NonDelegable))
	for p := range NonDelegable {
		nd = append(nd, "`"+string(p)+"`")
	}
	sort.Strings(nd)
	b.WriteString(strings.Join(nd, ", ") + "\n")
	return b.String()
}
