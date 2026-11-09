package auth

import "sort"

// PermissionInfo explains one permission in plain words for the people and
// access pages: the business area it belongs to, what it lets a person do,
// and its stage in the flow of work (prepare -> approve -> release -> govern).
type PermissionInfo struct {
	Code  Permission `json:"code"`
	Area  string     `json:"area"`
	Label string     `json:"label"`
	Stage string     `json:"stage"`
}

// Stages of work, in the order a controlled document moves through them.
const (
	StageRead    = "read"    // sees records only
	StagePrepare = "prepare" // creates and submits
	StageApprove = "approve" // checks, approves, certifies, confirms
	StageRelease = "release" // posts, pays, releases, closes
	StageGovern  = "govern"  // sets the rules others work under
)

type pl struct{ area, label, stage string }

// permissionLabels covers every permission of the catalogue (a test checks
// it), so no screen ever shows a bare code without an explanation.
var permissionLabels = map[Permission]pl{
	PermJournalCreate:         {"Accounting", "Prepare journals", StagePrepare},
	PermJournalApprove:        {"Accounting", "Approve journals", StageApprove},
	PermJournalPost:           {"Accounting", "Post journals to the ledger", StageRelease},
	PermJournalReverse:        {"Accounting", "Reverse posted journals", StageRelease},
	PermJournalRead:           {"Accounting", "See journals and the trial balance", StageRead},
	PermAccountManage:         {"Accounting", "Maintain the chart of accounts", StageGovern},
	PermPeriodClose:           {"Accounting", "Close accounting periods", StageRelease},
	PermPeriodReopen:          {"Accounting", "Reopen closed periods", StageGovern},
	PermFXRead:                {"Accounting", "See exchange rates", StageRead},
	PermFXManage:              {"Accounting", "Enter exchange rates", StagePrepare},
	PermFXApprove:             {"Accounting", "Approve exchange rates", StageApprove},
	PermFXApproveExcept:       {"Accounting", "Approve out-of-band exchange rates", StageApprove},
	PermFXPolicyApprove:       {"Accounting", "Approve the exchange-rate policy", StageGovern},
	PermBankImport:            {"Treasury & banking", "Import bank statements", StagePrepare},
	PermBankReconcile:         {"Treasury & banking", "Reconcile bank statements", StageApprove},
	PermBankDetailsReveal:     {"Treasury & banking", "Reveal full bank account numbers", StageApprove},
	PermBankAcctManage:        {"Treasury & banking", "Register company bank accounts", StagePrepare},
	PermBankAcctApprove:       {"Treasury & banking", "Approve company bank accounts", StageApprove},
	PermPaymentPrepare:        {"Payables", "Prepare supplier payments", StagePrepare},
	PermPaymentApprove:        {"Payables", "Approve supplier payments", StageApprove},
	PermPaymentRelease:        {"Payables", "Release payments to the bank", StageRelease},
	PermAPEnter:               {"Payables", "Enter supplier invoices", StagePrepare},
	PermAPApprove:             {"Payables", "Approve supplier invoices", StageApprove},
	PermAPApproveException:    {"Payables", "Approve invoice price exceptions", StageApprove},
	PermP2PRead:               {"Procurement", "See projects, purchasing and site records", StageRead},
	PermP2PPolicyManage:       {"Procurement", "Draft purchasing policies", StagePrepare},
	PermP2PPolicyApprove:      {"Procurement", "Approve purchasing policies", StageGovern},
	PermSupplierManage:        {"Procurement", "Register suppliers", StagePrepare},
	PermSupplierApprove:       {"Procurement", "Approve suppliers", StageApprove},
	PermSupplierBankApprove:   {"Procurement", "Approve supplier bank accounts", StageApprove},
	PermPRCreate:              {"Procurement", "Raise purchase requisitions", StagePrepare},
	PermPRApprove:             {"Procurement", "Approve purchase requisitions", StageApprove},
	PermRFQManage:             {"Procurement", "Run requests for quotation", StagePrepare},
	PermRFQEvaluate:           {"Procurement", "Evaluate quotations", StageApprove},
	PermRFQAward:              {"Procurement", "Award quotations", StageApprove},
	PermPOManage:              {"Procurement", "Prepare purchase orders", StagePrepare},
	PermPOApprove:             {"Procurement", "Approve purchase orders", StageApprove},
	PermBudgetOverride:        {"Projects & planning", "Override a budget check", StageApprove},
	PermBudgetRevise:          {"Projects & planning", "Prepare budget revisions", StagePrepare},
	PermBudgetApprove:         {"Projects & planning", "Approve budgets, transfers and drawdowns", StageApprove},
	PermBudgetTransfer:        {"Projects & planning", "Prepare budget transfers", StagePrepare},
	PermWBSManage:             {"Projects & planning", "Maintain the WBS and cost codes", StagePrepare},
	PermWBSApprove:            {"Projects & planning", "Approve WBS and cost codes", StageApprove},
	PermProjectCreate:         {"Projects & planning", "Create projects", StagePrepare},
	PermProjectUpdate:         {"Projects & planning", "Update project details", StagePrepare},
	PermProjectGateSubmit:     {"Projects & planning", "Submit project stage gates", StagePrepare},
	PermProjectGateDecide:     {"Projects & planning", "Decide project stage gates", StageApprove},
	PermForecastPrepare:       {"Projects & planning", "Prepare cost forecasts", StagePrepare},
	PermForecastApprove:       {"Projects & planning", "Approve cost forecasts", StageApprove},
	PermChangeManage:          {"Projects & planning", "Raise change events", StagePrepare},
	PermChangeApprove:         {"Projects & planning", "Decide change events", StageApprove},
	PermScheduleManage:        {"Projects & planning", "Prepare schedules and progress", StagePrepare},
	PermScheduleApprove:       {"Projects & planning", "Approve schedules and progress", StageApprove},
	PermBOQManage:             {"Projects & planning", "Prepare bills of quantities", StagePrepare},
	PermBOQApprove:            {"Projects & planning", "Approve bills of quantities", StageApprove},
	PermCustomerManage:        {"Commercial & revenue", "Register customers", StagePrepare},
	PermCustomerApprove:       {"Commercial & revenue", "Approve customers", StageApprove},
	PermContractPolicyManage:  {"Commercial & revenue", "Draft contract policies", StagePrepare},
	PermContractPolicyApprove: {"Commercial & revenue", "Approve contract policies", StageGovern},
	PermContractManage:        {"Commercial & revenue", "Prepare contracts and variations", StagePrepare},
	PermContractApprove:       {"Commercial & revenue", "Approve contracts and variations", StageApprove},
	PermClaimManage:           {"Commercial & revenue", "Prepare notices and claims", StagePrepare},
	PermClaimApprove:          {"Commercial & revenue", "Approve claims for sending", StageApprove},
	PermBondManage:            {"Commercial & revenue", "Register, reduce and extend guarantees", StagePrepare},
	PermBondApprove:           {"Commercial & revenue", "Release or call guarantees", StageRelease},
	PermEscalationManage:      {"Commercial & revenue", "Prepare price indices, formulas and price adjustments", StagePrepare},
	PermEscalationApprove:     {"Commercial & revenue", "Approve index values, formulas and price adjustments", StageApprove},
	PermRevenueRun:            {"Accounting", "Prepare the month-end revenue run (WIP adjustment)", StagePrepare},
	PermSubcontractManage:     {"Subcontracts", "Prepare subcontracts and back-charges", StagePrepare},
	PermSubcontractApprove:    {"Subcontracts", "Approve subcontracts and back-charges", StageApprove},
	PermSubcontractApply:      {"Subcontracts", "Enter subcontractor applications", StagePrepare},
	PermSubcontractCertify:    {"Subcontracts", "Certify subcontractor applications", StageApprove},
	PermAssetManage:           {"Accounting", "Register fixed assets, run depreciation, propose disposals", StagePrepare},
	PermAssetApprove:          {"Accounting", "Activate fixed assets and approve disposals", StageApprove},
	PermPayrollPrepare:        {"Payroll", "Keep employees, rule sets and pay runs", StagePrepare},
	PermPayrollApprove:        {"Payroll", "Approve payroll rule sets and pay runs", StageApprove},
	PermConsolidationManage:   {"Consolidation", "Prepare consolidation groups and runs, designate intercompany accounts", StagePrepare},
	PermConsolidationApprove:  {"Consolidation", "Approve consolidation groups and runs", StageApprove},
	PermTaxPrepare:            {"Tax", "Prepare and file tax returns, submit e-invoices", StagePrepare},
	PermTaxApprove:            {"Tax", "Approve tax returns", StageApprove},
	PermTimesheetRecord:       {"Workforce", "Record and submit timesheets", StagePrepare},
	PermTimesheetApprove:      {"Workforce", "Approve timesheets", StageApprove},
	PermEquipmentManage:       {"Equipment", "Keep the equipment register and work orders", StagePrepare},
	PermEquipmentRecord:       {"Equipment", "Record and submit equipment usage", StagePrepare},
	PermEquipmentApprove:      {"Equipment", "Approve equipment usage", StageApprove},
	PermEquipmentCharge:       {"Equipment", "Prepare the monthly equipment hire charge", StagePrepare},
	PermHSEReport:             {"Health and safety", "Report incidents, request and accept permits to work", StagePrepare},
	PermQualityRequest:        {"Quality", "Request inspections and raise non-conformances", StagePrepare},
	PermQualityManage:         {"Quality", "Prepare inspection plans, decide inspections, record tests", StagePrepare},
	PermQualityApprove:        {"Quality", "Approve inspection plans and close non-conformances", StageApprove},
	PermSiteRecord:            {"Site", "Record punch items, instructions acknowledged and meeting minutes", StagePrepare},
	PermSiteInstruct:          {"Site", "Issue and close site instructions", StageApprove},
	PermHSEManage:             {"Health and safety", "Investigate incidents, issue permits, inductions and inspections", StageApprove},
	PermMeasureCreate:         {"Commercial & revenue", "Record measurements", StagePrepare},
	PermMeasureVerify:         {"Commercial & revenue", "Verify measurements", StageApprove},
	PermIPCPrepare:            {"Commercial & revenue", "Prepare payment certificates", StagePrepare},
	PermIPCCertify:            {"Commercial & revenue", "Certify payment certificates", StageApprove},
	PermARInvoiceCreate:       {"Commercial & revenue", "Prepare customer invoices", StagePrepare},
	PermARInvoiceApprove:      {"Commercial & revenue", "Approve customer invoices", StageApprove},
	PermARReceiptCreate:       {"Commercial & revenue", "Apply money received", StagePrepare},
	PermARReceiptConfirm:      {"Commercial & revenue", "Confirm customer receipts", StageApprove},
	PermARCreditCreate:        {"Commercial & revenue", "Prepare credit notes", StagePrepare},
	PermARCreditApprove:       {"Commercial & revenue", "Approve credit notes", StageApprove},
	PermARRetentionCreate:     {"Commercial & revenue", "Prepare retention releases", StagePrepare},
	PermARRetentionApprove:    {"Commercial & revenue", "Approve retention releases", StageApprove},
	PermStockItemManage:       {"Stores", "Register stock items", StagePrepare},
	PermStockItemApprove:      {"Stores", "Approve stock items", StageApprove},
	PermWarehouseManage:       {"Stores", "Maintain warehouses", StagePrepare},
	PermStockReceive:          {"Stores", "Put received goods away", StagePrepare},
	PermStockIssue:            {"Stores", "Issue stock to work", StagePrepare},
	PermStockIssueApprove:     {"Stores", "Approve stock issues", StageApprove},
	PermStockCount:            {"Stores", "Count stock", StagePrepare},
	PermStockAdjustApprove:    {"Stores", "Approve stock count adjustments", StageApprove},
	PermStockTransfer:         {"Stores", "Transfer stock between stores", StagePrepare},
	PermStockTransferApprove:  {"Stores", "Approve stock transfers", StageApprove},
	PermStockReserve:          {"Stores", "Reserve stock for work", StagePrepare},
	PermGRNCreate:             {"Site", "Record goods received", StagePrepare},
	PermGRNConfirm:            {"Site", "Confirm goods received", StageApprove},
	PermSiteDiaryManage:       {"Site", "Write site diaries", StagePrepare},
	PermSiteDiaryApprove:      {"Site", "Approve site diaries", StageApprove},
	PermSiteDeviceRegister:    {"Site", "Register field devices", StagePrepare},
	PermSiteDeviceManage:      {"Site", "Approve and retire field devices", StageApprove},
	PermOrgManage:             {"Governance & access", "Maintain entities, units and the calendar", StageGovern},
	PermAuthorityManage:       {"Governance & access", "Draft the authority matrix and duty exceptions", StagePrepare},
	PermAuthorityApprove:      {"Governance & access", "Approve the authority matrix and duty exceptions", StageGovern},
	PermDelegationCreate:      {"Governance & access", "Lend own authority to a colleague", StagePrepare},
	PermDelegationApprove:     {"Governance & access", "Approve delegations of authority", StageGovern},
	PermApprovalHighValue:     {"Governance & access", "Approve high-value items", StageApprove},
	PermAuditRead:             {"Governance & access", "Read the audit history", StageRead},
	PermScopeTenantWide:       {"Governance & access", "Work on every project (not only assigned ones)", StageRead},
	PermIdentityRevoke:        {"Governance & access", "Sign other people out everywhere", StageGovern},
	PermTenantSettingsManage:  {"Governance & access", "Change company settings", StageGovern},
	PermUserManage:            {"Governance & access", "Add people, give and take their roles", StageGovern},
	PermRoleManage:            {"Governance & access", "Compose the company's own roles", StageGovern},
	PermWorkflowRead:          {"Automation & intelligence", "See workflows and automation", StageRead},
	PermWorkflowDefine:        {"Automation & intelligence", "Draft workflow definitions", StagePrepare},
	PermWorkflowPublish:       {"Automation & intelligence", "Publish workflow definitions", StageGovern},
	PermWorkflowAdmin:         {"Automation & intelligence", "Administer running workflows", StageGovern},
	PermAutomationManage:      {"Automation & intelligence", "Propose automation rules", StagePrepare},
	PermAutomationActivate:    {"Automation & intelligence", "Activate automation rules", StageGovern},
	PermIntelligenceRead:      {"Automation & intelligence", "Use the dashboard, Ask and Risks", StageRead},
}

// Label returns the plain-words description of a permission; an unknown
// permission is returned as its own code in the "Other" area.
func Label(p Permission) PermissionInfo {
	if l, ok := permissionLabels[p]; ok {
		return PermissionInfo{Code: p, Area: l.area, Label: l.label, Stage: l.stage}
	}
	return PermissionInfo{Code: p, Area: "Other", Label: string(p), Stage: StagePrepare}
}

// PermissionCatalog lists every grantable permission with its explanation,
// sorted by area then label. Custom roles are composed from this list only.
func (a *Authorizer) PermissionCatalog() []PermissionInfo {
	out := []PermissionInfo{}
	for p := range a.KnownPermissions() {
		out = append(out, Label(p))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Area != out[j].Area {
			return out[i].Area < out[j].Area
		}
		return out[i].Label < out[j].Label
	})
	return out
}

var stageRank = map[string]int{StageRead: 0, StagePrepare: 1, StageApprove: 2, StageRelease: 3, StageGovern: 4}

// HighestStage is the furthest stage of work a permission set reaches; the
// people map places each person there. Empty means no permission at all.
func HighestStage(perms []Permission) string {
	best := ""
	for _, p := range perms {
		st := Label(p).Stage
		if best == "" || stageRank[st] > stageRank[best] {
			best = st
		}
	}
	return best
}
