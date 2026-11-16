#!/usr/bin/env python3
"""Requirement register for SERK Construction OS 3.0 (MASTER_PROMPT.md section 1).

Maps the 80 functional domains of spec/module-register.csv to unique IDs,
breaks each into testable requirements and writes

    docs/engineering/requirements.json   (machine-readable)
    docs/engineering/REQUIREMENTS.md     (the same, for people)

Honesty rule, enforced mechanically: a requirement is written as VERIFIED only
if every test named as its evidence appears as PASS in the test-result file
given with --results (one "PASS <TestName>" per line, produced from an executed
`go test -json` run). Otherwise it is downgraded to IMPLEMENTED_UNVERIFIED.
Nothing here is a human sign-off.

Usage:
    python3 tools/requirements_register.py --results docs/engineering/evidence/<run>.tests.txt
"""
import argparse
import csv
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

V, U, I, N, B = "VERIFIED", "IMPLEMENTED_UNVERIFIED", "IMPLEMENTING", "NOT_STARTED", "BLOCKED"

# (prefix, product, register row in spec/module-register.csv, milestone)
PRODUCTS = [
    ("CMD", "COMMAND", "COMMAND (5)"),
    ("EST", "ESTIMATE", "ESTIMATE (7)"),
    ("PLB", "PLAN / BUILD", "PLAN / BUILD (9)"),
    ("DSN", "DESIGN", "DESIGN (3)"),
    ("SUP", "SUPPLY / STOCK", "SUPPLY / STOCK (9)"),
    ("FLT", "FLEET", "FLEET (4)"),
    ("PPL", "PEOPLE", "PEOPLE (5)"),
    ("CON", "CONTRACT", "CONTRACT (4)"),
    ("CAP", "CAPITAL", "CAPITAL (9)"),
    ("ASR", "ASSURE", "ASSURE (4)"),
    ("CNX", "CONNECT", "CONNECT (13)"),
    ("RES", "ESTATE", "ESTATE (4)"),
    ("BRS", "BRAIN / SENTINEL", "BRAIN / SENTINEL (4)"),
]


def r(text, status, *tests, note="", drill="", criteria=()):
    # drill: a recorded drill report (path under the repository) that must end in a PASS verdict.
    # criteria: atomic acceptance criteria (c(...)) that decompose a large requirement. They get IDs
    # <requirement>.<a, b, ...>, are judged by the same evidence rule, and the parent cannot be VERIFIED
    # unless every criterion is. Append criteria; never insert or reorder them (IDs are positional).
    return {"text": text, "status": status, "tests": list(tests), "note": note, "drill": drill, "criteria": list(criteria)}


def c(text, status, *tests, note=""):
    return {"text": text, "status": status, "tests": list(tests), "note": note, "drill": ""}


# domain id -> (milestone, owner role from the Bible's approval table, requirements)
D = {
    # ------------------------------------------------------------ COMMAND
    "CMD-01": ("M1", "Chief architect", [
        r("A tenant's data is reachable only through that tenant's database login and row-level security", V,
          "TestSC04_CompromisedTenantCredentialCannotReachOtherTenant", "TestTenantIsolation", "TestPrivileges_OwnerMaintenancePolicyEverywhere"),
        r("Legal entities, regions and business units are master data with roll-ups", V, "TestOrg_MasterDataRegionsAndRollups"),
        r("Group → entity → book/branch hierarchy with effective-dated joint-venture and intercompany relationships", N),
    ]),
    "CMD-02": ("M2", "Product owner", [
        r("A project moves through phases only by an approved stage gate (maker/checker, fresh MFA)", V, "TestOrg_ProjectLifecycleThroughGates"),
        r("Portfolio roll-up across projects and entities with FX as-of rates (no naive sum of currencies)", N),
    ]),
    "CMD-03": ("M6", "Product owner", [
        r("Programs group projects with a dependency graph and investment gates", N),
    ]),
    "CMD-04": ("M6", "Product owner", [
        r("Rule-based risk assessments are recorded as snapshots with their inputs", V, "TestPlatform_IntelligenceAndAssistant"),
        r("OKRs, decision log and risk register with owners and due dates", N),
    ]),
    "CMD-05": ("M4", "Product owner", [
        r("Every dashboard tile equals the report it drills into; the KPI catalogue documents formula and source", V,
          "TestDashboard_TilesEqualTheirReports", "TestDashboard_ScopeAndPermissions", "TestKPICatalogueDocumentIsCurrent"),
        r("13-week cash projection separating certified, billed and collected amounts", I,
          note="A 90-day outlook from approved invoices and ledger cash exists (docs/BACKLOG.md 5.4); payroll, tax and supplier schedules are missing.",
          criteria=[
            c("Certified, billed and collected amounts are separate lines of the projection", I, note="new-version 13-week forecast (TestCashForecast_ThirteenWeeks); no collected line"),
            c("Supplier payment schedule from approved invoices and open purchase orders by due date", I, note="new-version forecast; due-date week placement not asserted"),
            c("Payroll schedule from approved payroll runs (synthetic rules in test)", N),
            c("Tax payment schedule from posted tax liabilities", N),
            c("Collection schedule from issued customer invoices by due date", I, note="new-version forecast; due-date week placement not asserted"),
            c("A reference dataset with independently calculated weekly cash positions matches the projection to the cent", N)]),
        r("Daily exception digest by e-mail or Telegram", B, note="No authorised messaging adapter or credentials. In-app notifications exist (TestPlatform_RulesLifecycleAndWorker).",
          criteria=[
            c("Messaging-provider interface, templates, delivery queue, idempotency and retry", N),
            c("Delivery audit, user preferences and approved recipients, verified against a local test provider", N),
            c("Real delivery through an authorised e-mail or Telegram provider", B)]),
    ]),
    # ----------------------------------------------------------- ESTIMATE
    "EST-01": ("M7", "Product owner", [r("Lead and opportunity pipeline", N)]),
    "EST-02": ("M7", "Finance controller", [r("Feasibility and discounted cash-flow scenarios with documented assumptions", N)]),
    "EST-03": ("M7", "Product owner", [r("Land and permit records", N)]),
    "EST-04": ("M7", "Product owner", [r("Tender calendar, confidential bid room, eligibility checklist, bid approval and submission record", N)]),
    "EST-05": ("M2", "Construction lead", [
        r("BOQ versions are approved by a second person; the approved version supersedes the previous one and is never overwritten", V,
          "TestJourneyA_BOQToBankReconciliationToGL", "TestJourneyA_SegregationOfDutiesAndBudget"),
        r("Spreadsheet import is staged, reviewed and committed explicitly, with row errors reported", V, "TestImports_StageReviewCommit"),
        r("PDF and IFC take-off arrive as reviewed suggestions, never as authoritative quantities", N),
    ]),
    "EST-06": ("M2", "Construction lead", [
        r("Rate build-up: resources × rates + overhead, contingency and escalation; selling rate = full cost / (1 − margin); markup kept distinct", N,
          note="Golden case from the Bible: direct 7,400 → full 8,140 → 9,576.47 at 15% margin."),
        r("Every rate carries source, region, currency, FX date, validity and approval; rates freeze at bid submission", N),
    ]),
    "EST-07": ("M2", "Construction lead", [r("Bid scenarios and comparison of estimate against actual historical rates", N)]),
    # --------------------------------------------------------- PLAN / BUILD
    "PLB-01": ("M2", "Finance controller", [
        r("Original, revised and current budget; revisions and transfers need a second person and cannot overspend under concurrency", V,
          "TestBudget_ArithmeticAndControls", "TestBudget_DatabaseRefusesBypass", "TestBudget_ConcurrentTransfersCannotOverspend"),
        r("Available budget is checked when a commitment is approved; consumed commitments are not counted twice", V,
          "TestJourneyA_OverBudgetHighValuePOEscalation", "TestR7_BudgetDoubleCount", "TestR8_BudgetNettingByCount"),
    ]),
    "PLB-02": ("M6", "Construction lead", [
        r("CPM with FS/SS/FF/SF links, lags and calendars; cycles and invalid networks are refused", V,
          "TestCPMTextbook", "TestCPMLinkTypesAndLags", "TestCPMRefusesInvalidNetworks", "TestCalendarSixDayWeekWithHoliday"),
        r("Approved baselines, progress at a status date and earned value; undefined ratios are null, not zero", V,
          "TestScheduleBaselineProgressEVM", "TestComputeAndZeroDenominatorPolicy", "TestR9_ScheduleEVMHand"),
        r("Monte Carlo delay forecast (P50/P80) with reproducible seeds and stated distributions", N),
        r("Look-ahead and Last Planner constraint tracking", N),
    ]),
    "PLB-03": ("M6", "Construction lead", [r("Resource- and cost-loaded schedules; levelling proposals never alter an approved schedule silently", N)]),
    "PLB-04": ("M6", "Product owner", [r("Project risk register with quantified exposure feeding contingency", I,
                                         note="Managed register (0080) beside the rule-based snapshots of CMD-04.", criteria=[
            c("Risks with owner, cause, effect and response; assessments kept as history; exposure = probability x cost impact", V, "TestRisks_RegisterAgainstContingency"),
            c("Open exposure compared with the contingency left in the approved budget; a shortfall is shown", V, "TestRisks_RegisterAgainstContingency"),
            c("A contingency revision drafted from the exposure for a person to approve", N)])]),
    "PLB-05": ("M4", "Construction lead", [r("Work packages released only with approved drawings, permits, ITP, resources and predecessor acceptance", N)]),
    "PLB-06": ("M4", "Construction lead", [
        r("Daily site diaries (labour, equipment, notes) are versioned, approved by a project manager and corrected, never overwritten", V,
          "TestOffline_DiaryLifecycleOnline", "TestSync_APIDeviceDiaryApproval", "TestOffline_DatabaseRules"),
        r("Photographs, weather, delays and instructions on the daily report", N),
    ]),
    "PLB-07": ("M4", "Construction lead", [
        r("Measurements are prepared by one person and verified by another; certified quantity cannot be counted twice", V,
          "TestJourneyB_ContractToCashToGL", "TestR6_15_ConcurrentMeasurementVerification", "TestCreditQuantityCorrection_NoDoubleDeduction"),
        r("Measurement book lines carry method, chainage/location, drawing revision, dimensions and evidence", N),
    ]),
    "PLB-08": ("M4", "Construction lead", [
        r("Progress reports against the approved baseline are approved before they count", V, "TestR9_ScheduleProgressAndRaw"),
        r("Installed, measured, certified, billed and collected quantities are reported side by side", I,
          note="Measured/certified/billed/collected exist for contract lines (Journey B); installed quantity by work package does not.",
          criteria=[
            c("Installed quantities come from work packages, not typed onto the report", N, note="new-version rules of credit take installed quantities as typed entries by activity code, which this criterion excludes"),
            c("Measured, certified and billed quantities trace to measurement lines, certificates and invoices", N),
            c("Collected amounts trace to receipts applied to those invoices; physical progress never counts as collected", N)]),
    ]),
    "PLB-09": ("M7", "Construction lead", [r("Engineering deliverables register linked to RFIs and submittals", N)]),
    # ------------------------------------------------------------- DESIGN
    "DSN-01": ("M8", "Chief architect", [r("IFC import in an isolated parser; object identity by model revision + GlobalId; mapping to BOQ/WBS", N)]),
    "DSN-02": ("M8", "Chief architect", [r("Installed-asset records with measured/estimated/reported/certified data classes", N)]),
    "DSN-03": ("M8", "Chief architect", [r("PostGIS spatial objects with explicit coordinate reference system and chainage", N,
                                           note="PostGIS is not installed in the baseline environment.")]),
    # ------------------------------------------------------ SUPPLY / STOCK
    "SUP-01": ("M3", "Construction lead", [
        r("Reorder rules flag items below their level", V, "TestStockPlus_ReservationsIssuesAndReorder"),
        r("Material demand generated from schedule and BOQ, netted against stock", N),
    ]),
    "SUP-02": ("M3", "Finance controller", [
        r("Requisition → RFQ → award → purchase order → goods receipt → three-way match → payment, reconciled to the ledger", V,
          "TestJourneyA_BOQToBankReconciliationToGL", "TestAPIJourneyA_EndToEnd"),
        r("No self-approval, no split-order bypass; authority limits apply to purchase orders", V,
          "TestJourneyA_SegregationOfDutiesAndBudget", "TestJourneyA_ProcurementRules", "TestAuthority_MatrixLimitsPurchaseOrders"),
        r("Receipt and matching tolerances; duplicate supplier invoices are refused", V, "TestJourneyA_ReceiptAndMatchingControls"),
    ]),
    "SUP-03": ("M3", "Finance controller", [
        r("Supplier bank details are dual-controlled, masked, encrypted at rest and revealed only with MFA and a purpose", V,
          "TestBankAtRest_CiphertextOnlyInTheDatabase", "TestIA04001_PrivilegedRevealIsPermissionedFreshAndAudited", "TestR5_02_MaskingAcrossAllDetailTypesAndStores"),
        r("Supplier-level exposure triggers dual payment approval", V, "TestSupplierExposureNeedsDualApproval", "TestSupplierExposureWindowAndConcurrency"),
        r("Supplier qualification, conflict disclosure and performance scoring", V,
          "TestSupplierGovernance_QualificationConflictsScores", "TestConsoleBrowserJourneyA",
          note="0093: qualifications decided by another person (attested); per-RFQ conflict declarations; one score per order after a confirmed receipt. Both award gates are switched on by the P2P policy (default off).",
          criteria=[
            c("A qualification is approved or rejected by someone other than its recorder, through an attestation; a raw status write is refused", V,
              "TestSupplierGovernance_QualificationConflictsScores", "TestConsoleBrowserJourneyA"),
            c("With the policy on, nobody evaluates quotes or awards an RFQ without a declaration on it, and nobody who declared a conflict with a supplier evaluates its quote or awards to it", V,
              "TestSupplierGovernance_QualificationConflictsScores"),
            c("With the policy on, an RFQ is awarded only to a supplier with an approved qualification valid on the award day", V,
              "TestSupplierGovernance_QualificationConflictsScores"),
            c("A supplier is scored once per purchase order, only after a confirmed receipt, by someone other than the order approver; scores are immutable", V,
              "TestSupplierGovernance_QualificationConflictsScores", "TestConsoleBrowserJourneyA")]),
    ]),
    "SUP-04": ("M6", "Construction lead", [r("Subcontracts: scope/BOQ, security, measured work, retention, deductions", I, note="Integrated from new-version (ADR 0025): certificates with retention, advance recovery, back-charges, withholding and journals (TestSubcontract_CertificatesAndJournal). Independent review: no BOQ link, no security (bond); cancellation attested by the person cancelling since 0084 (DEF-017 closed).")]),
    "SUP-05": ("M3", "Finance controller", [
        r("Stock quantity and value move together (FIFO or weighted average) and reconcile to the inventory account", V,
          "TestJourneyC_StockValuationToGL", "TestR7_FIFOManyLayersSmallIssues", "TestR7_WACFractionalToZero", "TestAPIJourneyC_StockValuation"),
        r("Stock cannot go negative, including by raw SQL and under concurrent movements", V,
          "TestJourneyC_Controls", "TestR8_RawSQL_Stock", "TestR7_ConcurrentPutAwayAndCount", "TestR10_StockStateTenantWrites"),
        r("Reservations reduce what can be issued", V, "TestStockPlus_ReservationsIssuesAndReorder"),
        r("Returns, scrap and quarantine", V, "TestStock_QuarantineAndWriteoff", "TestStock_SiteReturns", "TestConsoleBrowserJourneyC",
          note="0088: quarantine stores and scrap write-offs; 0089: site returns at the issue's cost.",
          criteria=[
            c("Quarantine stores: filled and emptied by approved transfers; nothing is issued or reserved from them; the flag is fixed at creation", V,
              "TestStock_QuarantineAndWriteoff", "TestConsoleBrowserJourneyC"),
            c("Scrap write-offs at FIFO or average cost with a reason, approved by another person (attested), with a bound STK_SCRAP journal; the inventory reconciliation counts them", V,
              "TestStock_QuarantineAndWriteoff", "TestConsoleBrowserJourneyC"),
            c("Site returns: material returned from a project to a store at the issue's cost, crediting the project", V,
              "TestStock_SiteReturns", "TestConsoleBrowserJourneyC")]),
    ]),
    "SUP-06": ("M3", "Construction lead", [
        r("Transfers between warehouses pass through in-transit and leave no value residue", V,
          "TestJourneyC_StockTransfer", "TestR8_TransferLifecycleAndRaces", "TestR8_TransferFractionalNoResidue"),
        r("Bins, lots/batches and serial numbers", N),
    ]),
    "SUP-07": ("M6", "Construction lead", [r("Delivery tracking, haulage and inland logistics", N)]),
    "SUP-08": ("M6", "Finance controller", [r("Import file: pro forma, LC, customs, freight and landed-cost allocation by a documented basis", N)]),
    "SUP-09": ("M3", "Construction lead", [
        r("Unit conversions are approved data; incompatible conversions are refused", V, "TestStockPlus_ConversionsAreControlled"),
        r("BOQ theoretical quantity against actual issues and installation (waste)", N),
    ]),
    # -------------------------------------------------------------- FLEET
    "FLT-01": ("M6", "Construction lead", [
        r("Asset register, assignment, utilisation and monotonic meter readings", V,
          "TestFleet_RegisterAndLifecycle", "TestFleet_DailyLogsAndMeter", "TestAPIFleet",
          note="Identity never changes; the same maker and serial number, or registration, is registered once."),
        r("A fourteen-state lifecycle enforced identically in Go and in the database; every move is an attested human decision with a reason; history is written by the database and cannot be edited", V,
          "TestFleet_StateMachineGoAndSQLAgree", "TestLifecycleRules", "TestFleet_RegisterAndLifecycle"),
        r("Equipment on safety hold, broken down, under maintenance or under inspection cannot be put to work or reserved, and returns to service only through a certifier with fresh MFA", V,
          "TestFleet_RegisterAndLifecycle", "TestLifecycleRules", "TestAPIFleet", "TestConsoleBrowserFleet"),
        r("Approved reservations of one machine never overlap, also under concurrent approval; a machine reaches a project only through that project's approved reservation", V,
          "TestFleet_ReservationsNeverDoubleBook", "TestFleet_RegisterAndLifecycle", "TestAPIFleet"),
        r("Daily logs: one per machine, day and shift; an authorised operator; hours fit the meter and the day; the machine is at the project; approved by a second person; an offline repeat is recorded once", V,
          "TestFleet_DailyLogsAndMeter", "TestAPIFleet"),
        r("A meter never runs backwards and moves only through an approved log or an approved, two-person meter reset; replacement and rollover are recorded", V,
          "TestFleet_DailyLogsAndMeter"),
        r("Fleet data is invisible to other tenants; project hours are visible only inside the project scope; the register pages by cursor", V,
          "TestFleet_TenantIsolationAndPaging", "TestFleet_DailyLogsAndMeter", "TestAPIFleet"),
        r("Fleet Command Centre figures (counts by state, hours, utilization, availability, fuel per hour) equal the register and the approved logs; a ratio with no data is absent, not zero", V,
          "TestFleet_DailyLogsAndMeter", "TestHoursRatios", "TestConsoleBrowserFleet"),
        r("Work-package assignment, capacity planning, cross-project requests, availability forecasting, rental agreements, standby periods", N),
        r("Attachments, equipment documents and photographs, fixed-asset linkage, warranty and insurance expiry alerts", N,
          note="Warranty and insurance dates are stored; nothing acts on them yet."),
        r("Daily logs captured offline through the field device command log", I,
          note="The log API is idempotent on a client reference (verified); it is not yet a command kind of /v1/sync, and no field client exists.",
          criteria=[
            c("An equipment daily log is accepted as a signed, idempotent field-device command", N),
            c("A duplicate or replayed command applies once; a conflict is returned, never merged", N)]),
        r("Telematics ingestion (GPS, engine hours, fuel level) with authenticated devices and replay protection, stored apart from accounting data", N),
        r("Deterministic allocation and transfer recommendations with explainable constraints", N),
        r("Equipment costing and charge-out: operating cost per machine (fuel, parts, labour, workshop), internal charge-out and rental to projects as a management allocation kept apart from statutory postings, idle-time and utilisation rules, external rental, equipment profitability; rates and rules as approved, effective-dated policy", I,
          note="Charge-out built in 0079 (rates and runs); operating cost per machine (fuel, parts, labour, workshop) as one report is not built.",
          criteria=[
            c("Internal charge-out rates per machine are effective-dated documents approved by another person with a fresh second factor, attested; none is shipped", V, "TestFleet_ChargeOut"),
            c("A charge run prices a project's approved logs of a month at the rate in force on each day; a log without a rate stops the run; each log is charged once", V, "TestFleet_ChargeOut"),
            c("The run's EQP_CHG journal (Dr project equipment cost per cost code, Cr recovery) equals the run when approved or posted, and is cancelled only with the run while a draft", V, "TestFleet_ChargeOut"),
            c("Operating cost per machine (fuel, parts, labour, workshop) reported against its charge-out recovery", N)]),
    ]),
    "FLT-02": ("M6", "Construction lead", [
        r("Fuel issues linked to asset, operator, meter and stock movement; variance review", I,
          note="The link and the reconciliation are built and verified (FLT-02.2 to FLT-02.5). A variance is listed with both figures for a person to explain; a recorded, approved investigation of a variance is FLT-02.6 and is not built.",
          criteria=[
            c("Each dispense is a stock issue linked to machine, operator and meter", V, "TestFuel_DispenseIsAStockIssue"),
            c("A variance is investigated in a recorded document with findings, reviewed and approved by someone other than the investigator", N),
            c("Tank dip readings are reconciled to the stock ledger and the difference is explained or adjusted through an approved count", N)]),
        r("Fuel is ordinary stock: a dispense is a stock issue of a designated fuel item, prepared with the machine, meter reading and operator, and takes effect (tank quantity, value, journal, project cost) only when a second person approves the issue; there is no second fuel ledger", V,
          "TestFuel_DispenseIsAStockIssue", "TestAPIFuel"),
        r("A dispense is refused for an item not designated as fuel, a machine not at the project, a meter reading behind the last approved one, or more than the tank holds; an offline retry records once; a tenant database login cannot write or alter a dispense around the kernel", V,
          "TestFuel_DispenseIsAStockIssue"),
        r("Fuel items and fuel standards (litres per metered unit, effective-dated, append-only) are set only by a person with fleet.approve, attested", V,
          "TestFuel_DispenseIsAStockIssue", "TestFuel_StandardsAndReconciliation", "TestAPIFuel",
          note="One person sets a standard; it is a reporting benchmark, not a financial figure. Old standards are kept."),
        r("Per machine and period, dispensed, logged and expected fuel are compared and differences beyond a stated threshold are listed with both figures, in the API and the console, limited to the reader's projects", V,
          "TestFuel_StandardsAndReconciliation", "TestFuelFlags", "TestAPIFuel", "TestConsoleBrowserFleet",
          note="Thresholds come from the approved fleet policy (FLT-02.7); expected fuel is computed log by log across standards (FLT-02.8)."),
        r("Recorded investigation and approved closure of a fuel variance; tank dip readings reconciled to the stock ledger; fuel sensors (telematics)", N,
          note="A tank count today is the existing stock count."),
        r("The thresholds for listing a fuel difference and for a service being due soon are an approved, versioned, effective-dated fleet policy (prepared by fleet.manage, approved by another person with fleet.approve and a fresh second factor); reports name the policy applied, and say so when only engineering defaults exist", V,
          "TestFleetPolicy_ApprovedThresholdsApply", "TestAPIMaintenance", "TestFuelFlags", "TestDueState",
          note="ADR 0024. Charge-out rates, workshop allocation and utilisation rules are not part of this policy yet (FLT-01.14)."),
        r("Expected fuel for a period that spans several effective-dated standards is the sum of each approved log's metered units at the standard in force on that log's date; days logged with no standard in force predict nothing and are reported", V,
          "TestFleetPolicy_ApprovedThresholdsApply", "TestFuel_StandardsAndReconciliation", "TestFuelFlags"),
    ]),
    "FLT-03": ("M6", "Construction lead", [
        r("Maintenance plans, work orders, parts and downtime", I,
          note="Work orders, parts, plans and out-of-service hours are built and verified (FLT-03.2 to FLT-03.6). Labour cost, workshop cost centres and maintenance schedules across a fleet calendar are not (FLT-03.7).",
          criteria=[
            c("Plans, work orders, parts as stock issues and downtime", V, "TestMaintenance_WorkOrderAndReturnToService", "TestMaintenance_PlansAndDue"),
            c("Labour hours on a work order priced by an approved labour-rate policy", N),
            c("Workshop cost centres for machines not at a project", N),
            c("Fleet maintenance calendar of due and scheduled work", V, "TestMaintenance_PlansAndDue", "TestConsoleBrowserFleet",
              note="0092: day-based plans on their due day, meter-based plans projected from 30 days of approved logs (labelled estimates), work orders on their planned day (fixed at creation).")]),
        r("A work order is prepared (plant staff; site staff for a breakdown of a machine at their project), authorised by fleet.approve with a fresh second factor, reported complete by plant staff and closed or sent back by a certifier who did not report it; a decided work order never changes; none of these decisions can be taken by an AI or service principal", V,
          "TestMaintenance_WorkOrderAndReturnToService", "TestAPIMaintenance", "TestConsoleBrowserFleet"),
        r("A machine that has been under maintenance returns to service only after a work order for it was closed after it entered maintenance, never while a work order is open, and not by passing through inspection; an earlier work order does not release a later spell", V,
          "TestMaintenance_WorkOrderAndReturnToService", "TestFleet_RegisterAndLifecycle", "TestAPIMaintenance"),
        r("A part used on a work order is an ordinary stock issue linked to the work order: approved by a second person, valued by the stock ledger, charged to the machine's project; a work order is not reported complete while a part awaits approval", V,
          "TestMaintenance_WorkOrderAndReturnToService", "TestAPIMaintenance"),
        r("Preventive plans by metered units and/or days show what is due; a plan is raised as one work order at a time and moves on only when a certifier closes that work order", V,
          "TestMaintenance_PlansAndDue", "TestDueState", "TestAPIMaintenance", "TestConsoleBrowserFleet",
          note="The due-soon rule comes from the approved fleet policy (FLT-02.7). Revision and retirement of plans: FLT-03.9."),
        r("Maintenance history per machine (work orders, labour hours, parts cost from the stock ledger) and hours out of service by state from the lifecycle history; a tenant database login cannot write work orders, parts or plans around the kernel", V,
          "TestMaintenance_WorkOrderAndReturnToService", "TestAPIMaintenance"),
        r("Labour cost of maintenance, workshop cost centres for machines not at a project, parts reservation per work order, retiring or changing a plan, safety inspection checklists", N,
          note="Labour hours are recorded without a rate. Parts for a machine in the yard must name a project to charge until equipment costing (FLT-01.14) exists."),
        r("Separation on a work order as enforced in the kernel and in the database: the authoriser is not the preparer; the certifier is none of the preparer, the authoriser or the reporter; authorising and certifying each need a fresh second factor at the database boundary; an attestation serves one decision on one record once; forged session settings and direct writes are refused; other projects and tenants reach nothing", V,
          "TestMaintenance_SeparationAsEnforced", "TestMaintenance_WorkOrderAndReturnToService", "TestAPIMaintenance",
          note="At least three distinct people (the preparer may also report the work complete), not four. Whether four are required is a policy question for the owner (ADR 0024)."),
        r("A preventive plan is revised by a new version that retires the old one in the same transaction and continues from its last service, or retired with a reason; retired plans are kept, are not due and take no work orders; neither is possible while a work order of the plan is open", V,
          "TestMaintenance_PlanRevisionAndRetirement", "TestAPIMaintenance"),
    ]),
    "FLT-04": ("M6", "Finance controller", [r("Capitalisation, depreciation and disposal posted through the ledger", I, note="Integrated from new-version (ADR 0025), TestFixedAssets_CapitaliseDepreciateDispose. Independent review: capitalisation journal lines not asserted; no link to the fleet register; the asset is disposed of only by its approved disposal in the same transaction since 0084 (DEF-017 closed).")]),
    # ------------------------------------------------------------- PEOPLE
    "PPL-01": ("M6", "Product owner", [r("Employee and contractor records with protected personal data", N)]),
    "PPL-02": ("M6", "Construction lead", [r("Attendance and timesheets with approval, duplicate and overlap detection", I,
          note="Timesheets rebuilt from new-version (0076): attested approval by another person, at most 16 approved hours per person and day across projects, one approved line per person, project and day, induction gate; feeds payroll (TestWorkforce_TimesheetsAndHSE, TestConsoleBrowserField). Clock-in attendance is not built.",
          criteria=[
            c("Daily timesheets recorded by the site and approved by a different person, attested", V, "TestWorkforce_TimesheetsAndHSE", "TestConsoleBrowserField"),
            c("Duplicate and overlapping hours refused (one approved line per person, project and day; at most 16 hours a day across projects)", V, "TestWorkforce_TimesheetsAndHSE"),
            c("Approved hours imported into a pay run once", V, "TestWorkforce_TimesheetsAndHSE"),
            c("Clock-in/clock-out attendance capture, including offline from the field device", N)])]),
    "PPL-03": ("M6", "Finance controller", [r("Payroll preview → approval → locked run → posting → payment, on effective-dated reviewed rules", B,
                                              note="No statutory payroll rules may be shipped without professional approval (MASTER_PROMPT 4.8).",
          criteria=[
            c("Effective-dated, versioned payroll rule sets; an unapproved rule set cannot be activated", I, note="new-version payroll (TestPayroll_RunApprovePay); activation of an unapproved set is not tested"),
            c("Preview, independent approval, locked run, journal and payment record with synthetic test rules", I, note="new-version payroll; the locked run is not tested; employee records approved by a second person since 0074"),
            c("Reversal and reconciliation of a payroll run", N),
            c("Statutory rules supplied and approved by a named professional", B)])]),
    "PPL-04": ("M6", "Construction lead", [r("Crew output and labour cost by work package", N)]),
    "PPL-05": ("M6", "Construction lead", [r("Certifications and safety training with expiry", V, "TestCertifications_VerifyExpireRevoke", "TestConsoleBrowserField",
                                              note="0086: recorded from the document issued; counts only once verified by someone else holding hse.manage (attested); certification_valid is the one rule for validity on a day; expiring and expired shown; revocation with a reason; nothing deleted.")]),
    # ----------------------------------------------------------- CONTRACT
    "CON-01": ("M4", "Finance controller", [
        r("Contracts with approved lines, advance, retention and policy; the signed version is never overwritten", V,
          "TestJourneyB_ContractToCashToGL", "TestJourneyB_Controls", "TestR6_09_ContractPolicySuccession"),
        r("Guarantees register, liquidated-damages terms and contract obligations", V,
          "TestBonds_GuaranteeRegister", "TestContractTime_DamagesExtensionsObligations"),
    ]),
    "CON-02": ("M7", "Finance controller", [
        r("Variations are approved by a second person before they change contract value; pending and approved stay separate", V,
          "TestJourneyB_Variation", "TestR9_VariationRaces", "TestR9_VariationRawSQLAndScope"),
        r("Change events carry cost impact into a draft budget revision", V, "TestChange_EventToBudgetDraft"),
    ]),
    "CON-03": ("M7", "Finance controller", [
        r("Notices with a computed time bar; claims prepared and approved by different people; the client's answer recorded once", V,
          "TestClaims_NoticesAndClaims"),
    ]),
    "CON-04": ("M4", "Finance controller", [
        r("Payment certificate = cumulative certified − previous, less retention and advance recovery; never negative; no duplicate certification", V,
          "TestJourneyB_ContractToCashToGL", "TestR6_01_NegativeAmountDue", "TestR6_03_AdvanceRecoveryEdges", "TestJourneyB_CertificateHashConstant"),
        r("Retention release, credit notes and price adjustment are separate approved documents", V,
          "TestJourneyB_RetentionRelease", "TestJourneyB_CreditNote", "TestBonds_PriceAdjustment"),
        r("Certification, invoice, revenue recognition and collection are distinct states", I,
          note="Certificate, invoice and receipt are distinct; IFRS 15 revenue recognition is not implemented and needs accounting policy approval.",
          criteria=[
            c("Certification, invoicing and collection are distinct, separately evidenced states", N),
            c("Revenue recognition follows a contract-specific, approved policy and is not triggered by invoicing alone", I, note="new-version revenue runs (TestRevenue_RunAndWIPJournal): policy per legal entity, not per contract"),
            c("A recognition change is reviewed by someone other than its preparer", N)]),
    ]),
    # ------------------------------------------------------------ CAPITAL
    "CAP-01": ("M3", "Finance controller", [
        r("A journal posts only if balanced, in an open period, with valid accounts; enforced in Go and in the database", V,
          "TestHappyPathPostsAndBalancesAccounts", "TestUnbalancedRejectedInGoAndInDatabase", "TestClosedPeriodBlocksPosting", "TestSC14_DatabaseEnforcesCurrencyPrecision"),
        r("Posted journals are immutable; correction is by reversal", V,
          "TestPostedJournalIsImmutable", "TestSC01_PostedLineCannotBeReassignedOrMutated", "TestReversalFlow"),
        r("The same source posts once: replay returns the original, a changed payload conflicts", V,
          "TestIdempotentReplayAndConflict", "TestConcurrentIdenticalCreatesAreIdempotent"),
        r("Posting and period close cannot race", V,
          "TestSC08_PostWaitsForInFlightCloseThenRefuses", "TestSC08_CloseWaitsForInFlightPostAndIncludesIt"),
        r("Balance sheet, income statement and cash-flow statement", V, "TestAPIFinancialStatements", "TestCashFlow_DirectMethodReference", "TestConsoleBrowserFinance",
          note="Income statement, balance sheet (CAP-01.5.a), cash-flow statement by the direct method (0091), drill-down to journal lines.",
          criteria=[
            c("Income statement for a period and balance sheet at its end, from posted journals only, balancing against equity and unclosed earnings, checked against a hand-computed reference", V, "TestAPIFinancialStatements"),
            c("Cash-flow statement (needs operating/investing/financing classification of accounts)", V, "TestCashFlow_DirectMethodReference", "TestConsoleBrowserFinance",
              note="0091: account classes proposed and approved by different people (attested); direct method; opening + movement = closing, against a hand-computed reference."),
            c("One-step drill-down from a statement line to its journal lines", V, "TestAPIFinancialStatements", "TestConsoleBrowserFinance",
              note="Each line carries its account; the general-ledger detail report for that account and period adds up to the line.")]),
        r("Period end: fiscal years in the Gregorian or Ethiopian calendar; recurring journals only from templates approved by another person with a fresh second factor; accruals reversed once on their date; overhead spread by posted direct cost; the database refuses an overhead or recurring journal that differs from its computation (DEF-012, DEF-013; 0064, 0075, 0083)", V,
          "TestPeriodEnd_FiscalYearRecurringAccrualOverhead", "TestEthiopianFiscalYear", "TestAPIRecurringJournalApproval"),
    ]),
    "CAP-02": ("M3", "Finance controller", [
        r("Supplier invoices post through bound draft journals; payables reconcile to the control account", V,
          "TestJourneyA_SubledgerJournalIntegrity", "TestJourneyA_BOQToBankReconciliationToGL", "TestR3_01_SubledgerJournalLinesAppendedAfterBinding"),
    ]),
    "CAP-03": ("M4", "Finance controller", [
        r("Customer invoices, receipts and credit notes reconcile to the receivable control account, including under concurrency", V,
          "TestJourneyB_ContractToCashToGL", "TestJourneyB_CreditNoteRacesReceipt", "TestR8_ConcurrentCreditsAndReceipt", "TestR6_10_ManualJournalOnARControl"),
    ]),
    "CAP-04": ("M4", "Finance controller", [
        r("Cost codes are validated against the approved WBS", V, "TestSC10_CostCodesAreValidatedAgainstApprovedWBS"),
        r("Monthly cost forecast (EAC/ETC), WIP schedule and budget-vs-actual tie to their sources", V,
          "TestForecast_MethodsAndGuards", "TestForecast_WIPScheduleAndSummary", "TestReportCatalogue_EveryReportRunsAndTiesOut",
          "TestForecast_ChangeRuleNeedsExplanation"),
    ]),
    "CAP-05": ("M3", "Finance controller", [
        r("Payments are prepared, approved and released by different people and settled only by an independent bank statement", V,
          "TestJourneyA_PaymentControls", "TestR3_09_FabricatedStatementSettlesPayment", "TestR4_04_ReconcilerSeparation"),
        r("FX rates are policy-bound, maker/checker approved and re-checked at posting", V,
          "TestSC02_DatabaseRejectsIneligibleFXAndRechecksAtPost", "TestSC03_FXMakerChecker", "TestConvertIsExactAndBoundToSnapshot"),
        r("Bank transfers through a bank API", B, note="No authorised bank sandbox; payments are instructions released by people.",
          criteria=[
            c("Bank adapter interface with validation, idempotency, status and reconciliation, verified against a sandbox double", N),
            c("Live bank integration under explicit authorisation and banking credentials", B)]),
    ]),
    "CAP-06": ("M11", "Finance controller", [
        r("Tax rules are effective-dated data with review status; ambiguity is an error", V,
          "TestRulesValidate", "TestEffectiveDatingAndReviewStatus", "TestAmbiguityIsAnError"),
        r("Real jurisdiction tax and withholding rules", B, note="Needs professional approval per jurisdiction; no real rates are shipped.",
          criteria=[
            c("Configurable tax engine: effective dates, bases, exemptions, withholding, rounding, source and approval", N),
            c("Real rates and rules from a named tax professional with sources", B)]),
    ]),
    "CAP-07": ("M10", "Finance controller", [r("Intercompany matching, translation and elimination for consolidated statements", I, note="Integrated from new-version (ADR 0025), TestConsolidation_TranslateEliminateApprove: consolidated trial balance with translation, CTA, NCI and eliminations. Independent review: matching is per account pair, not per transaction; no statements; no database maker-checker (DEF-009).")]),
    "CAP-08": ("M6", "Finance controller", [r("Cash forecast scenarios including payroll, tax and supplier schedules", I,
                                              note="See CMD-05.",
          criteria=[
            c("Scenarios are versioned with stated assumptions", N),
            c("Each scenario uses the supplier, payroll, tax and collection schedules of CMD-05.2", N)])]),
    "CAP-09": ("M1", "Security lead", [
        r("Every command writes a hash-chained audit entry and its outbox event in the same transaction", V,
          "TestAuditChainLinearUnderConcurrency", "TestSC07_FXCommandsEmitAtomicOutboxEvents", "TestOutboxAndPeriodsCannotBeTamperedByAppRole"),
        r("A tenant login cannot rewrite history", V, "TestPrivileges_TenantLoginCannotRewriteHistory", "TestR3_02_NoDeleteOrTruncate"),
        r("Audit chain anchored to separately protected storage", N),
    ]),
    # ------------------------------------------------------------- ASSURE
    "ASR-01": ("M6", "Construction lead", [r("ITP with hold/witness points, NCR and corrective action through independent closure", I, note="Integrated from new-version (ADR 0025), TestQuality_HoldPointNCRAndSiteRecords. Independent review: inspection results and NCR closure are unattested (DEF-008), witness points not asserted.")]),
    "ASR-02": ("M6", "Construction lead", [r("Samples with chain of custody, calibrated equipment, results against limits; failed tests preserved", N)]),
    "ASR-03": ("M6", "Construction lead", [r("Permits to work, incidents, toolbox talks; stop-work effective offline; no AI release", I,
          note="HSE rebuilt from new-version (0076) with every decision attested (incident closure, corrective action done and verified, permit issue, acceptance and closure).",
          criteria=[
            c("Incidents investigated and closed by someone other than the reporter; major incidents need a verified corrective action; attested", V, "TestWorkforce_TimesheetsAndHSE", "TestConsoleBrowserField"),
            c("Permits to work requested, issued and accepted by three different people, attested, with bounded validity", V, "TestWorkforce_TimesheetsAndHSE", "TestConsoleBrowserField"),
            c("Toolbox talks, inductions and safety inspections recorded; LTIFR from approved hours", V, "TestWorkforce_TimesheetsAndHSE"),
            c("Stop-work effective offline on the field device", N),
            c("No AI identity can close, issue or accept (decisions reserved to human principals)", I, note="authorize() refuses non-human principals for decisions; no HSE-specific test yet")])]),
    "ASR-04": ("M6", "Product owner", [r("Carbon, waste, water and energy records with versioned factors; measured vs estimated", N)]),
    # ------------------------------------------------------------ CONNECT
    "CNX-01": ("M7", "Product owner", [
        r("A controlled document has immutable revisions; content is hashed by the database; a revision is published by a second person with fresh MFA", V,
          "TestDocuments_RevisionsAndPublication"),
        r("Publishing a revision supersedes the previous one in the same transaction; there is never more than one current revision", V,
          "TestDocuments_RevisionsAndPublication", "TestDocuments_ConcurrentPublicationKeepsOneCurrent"),
        r("Documents are visible only inside the tenant and the reader's project scope; every download is recorded", V,
          "TestDocuments_ScopeAndTenantIsolation", "TestAPIDocumentsAndRFIs"),
        r("Malware scanning and quarantine before a file can be downloaded", V,
          "TestDocSecurity_FailClosedQuarantine", "TestDocSecurity_InfectedAndMaliciousUploads", "TestClamdAgainstARealDaemon", "TestDocSecurity_RealScannerAndObjectStore",
          note="Fail closed: unscanned, failed-scan and infected files cannot be submitted, published or downloaded while scanning is required (the default). "
               "Verified against a real ClamAV daemon: locally with a database holding only the EICAR test hash, and in GitHub Actions from run 22 with the public signature database. Detection coverage beyond the standard test file is not measured (ADR 0019)."),
        r("Object storage with pre-signed, short-lived URLs and resumable upload", I,
          note="Object storage and short-lived links are done (CNX-01.7, CNX-01.9). Links are issued by the API, not pre-signed by the store, because objects are encrypted by the service. Resumable upload from the client is CNX-01.13.",
          criteria=[
            c("Object storage with encryption and short-lived authorised download", N),
            c("Resumable upload: an interrupted upload continues without re-sending completed parts and is scanned before acceptance", N)]),
        r("Transmittals, QR verification of a revision, retention policy and legal hold", I, criteria=[
            c("A transmittal records the approved revisions sent to a party and never changes; items only in the sending transaction", V, "TestDocflow_TransmittalsAndSubmittals", "TestConsoleBrowserDocuments"),
            c("QR verification of a printed revision", N),
            c("Retention policy per document type", N),
            c("Legal hold that blocks deletion and retention expiry", N)]),
        r("Files over the PostgreSQL cap are stored encrypted in an S3-compatible store under a tenant-scoped, immutable key fixed by the database; a tampered, swapped or cross-tenant object is not served", V,
          "TestDocSecurity_ObjectStorageAndMigration", "TestS3AgainstARealServer", "TestSealedRoundTripAndTamperDetection", "TestObjectKeyIsTenantScopedAndStrict", "TestDocumentStorageConfig"),
        r("Migration from PostgreSQL to object storage is incremental and repeatable, verifies each copy by reading it back, and removes no byte until the schema owner releases it", V,
          "TestDocSecurity_ObjectStorageAndMigration"),
        r("Download links are short-lived, bound to tenant, revision and person, re-checked against quarantine when used and audited; downloads and links count against the export budget", V,
          "TestDocSecurity_DownloadLinksAndExportBudget", "TestAPIDocumentSecurity"),
        r("Uploads are streamed with bounded memory and refused when mislabelled, an archive bomb, a path traversal, SVG/HTML, or carrying macros or active PDF content; refusals are audited", V,
          "TestInspect", "TestDocSecurity_InfectedAndMaliciousUploads", "TestAPIDocumentSecurity",
          note="Inspection is a filter, not a malware control: PDF active content can be hidden in compressed streams."),
        r("The scan requirement and storage quota are a per-tenant policy changed only by a person with tenant.settings.manage and fresh MFA, attested", V,
          "TestDocSecurity_FailClosedQuarantine", "TestDocSecurity_ObjectStorageAndMigration", "TestAPIDocumentSecurity",
          note="The quota is checked per upload; concurrent uploads can overshoot it by one file."),
        r("Safe document preview generation", N),
        r("Resumable multipart upload from the client for very large BIM and engineering files", N,
          note="The service streams a single request of any size to the store in parts; a client-side resumable protocol does not exist."),
        r("Storage faults are told apart (missing bucket, missing object, refused credentials, unavailable service); the bucket is provisioned and proven private before use; the API refuses to start without it and never reports a storage fault as a missing file", V,
          "TestS3ErrorClassification", "TestS3FaultClassesAgainstARealServer", "TestStorageFaultsAreNotReportedAsNotFound", "TestDocSecurity_RealScannerAndObjectStore"),
        r("Document encryption keys are versioned: rotation keeps earlier files readable, a missing key is reported as such and is recoverable, scan verdicts survive a rotation", V,
          "TestKeyVersionsRotationAndRecovery", "TestDocSecurity_KeyRotation", "TestDocumentKeyRotationConfig",
          note="ADR 0021. Existing files are not re-encrypted on rotation, so a compromised key cannot yet be retired; custody is an environment secret, not a KMS or HSM."),
    ]),

    "CNX-02": ("M7", "Construction lead", [
        r("Drawings are controlled documents: the current revision is the only one offered as current", V, "TestDocuments_RevisionsAndPublication"),
        r("Mark-ups and 2D review", N),
    ]),
    "CNX-03": ("M7", "Construction lead", [
        r("An RFI is raised against a project (optionally a document revision), answered once by someone other than its author, then closed", V,
          "TestDocuments_RFILifecycle", "TestAPIDocumentsAndRFIs"),
        r("RFI cost/time impact linked to change events", V, "TestRFIImpactAndSubmittalCycle", "TestConsoleBrowserDocuments",
          note="0090: a change event names the answered RFI it arises from (fixed at creation); the RFI's impact is computed from its change events."),
    ]),
    "CNX-04": ("M7", "Construction lead", [r("Submittals with review cycles and consultant responses", I, criteria=[
            c("A submittal is reviewed once, with a code A-D, by someone other than the submitter; attested; C and D need a note", V, "TestDocflow_TransmittalsAndSubmittals", "TestConsoleBrowserDocuments"),
            c("A resubmission is linked to the submittal it answers, forming a review cycle", V, "TestRFIImpactAndSubmittalCycle", "TestConsoleBrowserDocuments"),
            c("Consultant responses through the consultant portal", N, note="Depends on CNX-12.")])]),
    "CNX-05": ("M7", "Product owner", [r("Correspondence register (in/out) linked to contracts", V,
                                         "TestCorrespondence_RegisterRepliesAndOverdue", "TestAPICorrespondence", "TestConsoleBrowserDocuments",
                                         note="0085: items recorded once against the project and optionally a contract; an answer date makes an item await its reply, recorded in the other direction, which marks it answered; overdue shown. Contract notices remain under CON-03.")]),
    "CNX-06": ("M1", "Chief architect", [
        r("Versioned workflow definitions; thresholds, quorum, maker excluded; the database refuses a forged approval trail", V,
          "TestWorkflow_MandatoryJournalWorkflowReplacesDirectPath", "TestWorkflow_HighValueNeedsQuorumOfDistinctExecutivesWithMFA",
          "TestWorkflow_DatabaseRefusesForgedTrail", "TestValidatorRefusesUnsafeDefinitions"),
        r("An approval is bound to the exact document content, step and outcome, and cannot be replayed", V,
          "TestAttestation_ReplaySubstitutionExpiryAndTampering", "TestAttestation_DecisionBoundToStepAndOutcome"),
        r("Authority matrix and time-limited delegation that cannot defeat separation of duties", V,
          "TestAuthority_MatrixGovernanceAndLimits", "TestAuthority_DelegationLifecycle", "TestAuthority_ConflictingDelegationIsDroppedUnlessExcepted"),
    ]),
    "CNX-07": ("M4", "Product owner", [
        r("Alert rules notify the right people; a rule is activated by someone other than its author", V, "TestPlatform_RulesLifecycleAndWorker"),
        r("Assignable tasks with due dates outside document approvals", V, "TestTasks_AssignCompleteCancel", "TestConsoleBrowserForms",
          note="0087: assigned to an active directory person with a due date and a notification; only the assignee completes, only the creator cancels or reassigns, enforced by the database; overdue shown; on My work."),
    ]),
    "CNX-08": ("M7", "Product owner", [r("Meeting minutes with actions and decisions", I, note="Integrated from new-version (ADR 0025): meetings with actions; no decisions recorded.")]),
    "CNX-09": ("M4", "Product owner", [
        r("Every catalogued report runs and ties to its source; exports show no more than the screen and are budgeted", V,
          "TestReportCatalogue_EveryReportRunsAndTiesOut", "TestReportCatalogue_ScopeAndInput", "TestAPIReportsAndExports", "TestPlatform_ExportBudget"),
    ]),
    "CNX-10": ("M7", "Product owner", [r("Client portal limited to the client's own contracts, certificates and documents", N)]),
    "CNX-11": ("M7", "Product owner", [r("Supplier portal limited to the supplier's own RFQs, orders and invoices", N)]),
    "CNX-12": ("M7", "Product owner", [r("Consultant portal for scoped review and certification", N)]),
    "CNX-13": ("M11", "Product owner", [r("Government/regulator portal", B, note="No authorised interface or requirement owner identified.",
          criteria=[
            c("Export formats, connector boundary, validation, retry and audit with contract tests", N),
            c("Official specification and authorised interface", B)])]),
    # ------------------------------------------------------------- ESTATE
    "RES-01": ("M7", "Product owner", [r("Unit inventory, atomic reservation, sale agreements and instalments", N)]),
    "RES-02": ("M7", "Product owner", [r("Service requests, preventive maintenance, leases and utilities", N)]),
    "RES-03": ("M7", "Construction lead", [r("Commissioning checks and final acceptance", N)]),
    "RES-04": ("M7", "Construction lead", [r("Handover package: as-built revisions, manuals, warranties, punch list", N)]),
    # ---------------------------------------------------- BRAIN / SENTINEL
    "BRS-01": ("M9", "Product owner", [r("Lessons learned and authorised document retrieval with ACL before search", N)]),
    "BRS-02": ("M9", "Security lead", [
        r("An AI principal can draft but never approve, post, pay or release; enforced in Go and in the database", V,
          "TestGovernedAI_DraftOnly", "TestR6_11_HumanRuleAndDraftParity", "TestR8_HumanDecisionsAndScope"),
        r("The assistant answers only from permission-filtered computed facts and shows sources", V, "TestPlatform_IntelligenceAndAssistant", "TestAIPhraserAdapter"),
        r("Model-independent gateway, agent registry and evaluation sets", N),
        r("A live model provider", B, note="No model endpoint is reachable or authorised from the build environment.",
          criteria=[
            c("Provider-neutral gateway with policy controls, data filtering, cost limits and safe fallback, verified with a local provider", N),
            c("Approved provider credentials and data-sharing authorisation", B)]),
    ]),
    "BRS-03": ("M1", "Security lead", [
        r("Sessions can be revoked across replicas, including in-flight approvals", V,
          "TestSessions_RevocationKillsOldTokensIncludingInFlightApprovals", "TestAPISessionRevocationAcrossReplicas"),
        r("The approval signing key lives outside the API; a compromised API cannot forge an approval", V,
          "TestSignerIsolation_CompromisedAPICannotForge", "TestSignerIsolation_APIWithoutSecretFailsClosed"),
        r("Pre-authentication abuse limits shared across replicas", V, "TestSharedAbuse_LockoutAndBudgetsSpanReplicas", "TestAPIPreAuthFloodProtection"),
        r("Production configuration fails closed on any demo default", V, "TestProductionRefusesTheOldDemoDefaults", "TestProductionManifestHasNoDemoDefaults"),
        r("Independent penetration test; a real identity provider with step-up; HSM/KMS custody of the signer key", B,
          note="Needs named people and infrastructure (docs/release/GO_NO_GO.md).",
          criteria=[
            c("Real identity provider integration with step-up and revocation", B),
            c("KMS/HSM custody of the approval-signer key", B),
            c("KMS/HSM custody of document keys", B),
            c("External penetration test by an independent tester", B)]),
    ]),
    "BRS-04": ("M1", "Security lead", [
        r("Directory of people, custom roles and grants; identity administrators hold no financial permission", V,
          "TestPeople_DirectoryRolesAndProfile", "TestDefaultRolesSeparateDuties", "TestReservedPermissionsNotGrantableByRoles"),
        r("Tenant settings and a setup checklist that reads real state", V, "TestPlatform_TenantSettings", "TestSetup_ChecklistReadsRealState"),
        r("Access reviews, legal hold and break-glass access", N),
        r("A security administrator withholds or restores permissions of a product role with a fresh second factor; the database refuses any change not attested for that person (DEF-014, 0082)", V,
          "TestPeople_AdjustProductRole"),
    ]),
}

# Cross-cutting platform requirements (MASTER_PROMPT sections 6, 7, 8, 11, 13). Not part of the 80 domains.
X = {
    "XCT-OFFLINE": ("M5", "Offline field operation", [
        r("Offline commands are idempotent: a lost acknowledgement or a retry applies once; conflicts are returned, never merged", V,
          "TestSync_IdempotencyConflictsAndRevocation", "TestSync_ConcurrentRetriesApplyOnce", "TestR12_CommandLogForgeryAndSequenceBound", "TestR12_TransientFailureIsRetried"),
        r("A revoked device is refused", V, "TestSync_IdempotencyConflictsAndRevocation", "TestR12_DeviceCap"),
        r("Native field app with an encrypted local database, restart-safe queue and 7-day offline objective", N,
          note="The sync API exists; no mobile client is in this repository and none can be verified in this environment."),
        r("Offline stock issue through expiring quantity rights; offline stop-work", N),
    ]),
    "XCT-SCALE": ("M10", "Scale and history", [
        r("Tenant routing with a dedicated-database tier", V, "TestSC04_DedicatedDatabaseTier", "TestSC04_SharedLoginRetiredAndMisroutingFailsClosed"),
        r("Partitioned high-volume tables with partition-independent source identity", N),
        r("Archive catalogue, verified export to open formats and reconciled restore", N),
        r("Measured load at tier T1 or above", N, note="No load test at scale has been run; see CAPACITY_PLAN.md."),
    ]),
    "XCT-DR": ("M11", "Deployment and recovery", [
        r("Base backup plus WAL archive restores exactly to a named point, every migration included; data loss on losing the primary is measured", V,
          drill="docs/engineering/evidence/2026-11-15-pitr-drill-0054.txt",
          note="Local disposable clusters, a few hundred rows: all 54 migrations replayed, restore verified in 1.15 s, 5 s of committed writes lost with archive_timeout=10s. Not a production-scale measurement."),
        r("Encrypted, signed offsite PITR backup restores exactly; forged or colliding archives are refused", U,
          note="Drill script and an earlier recorded run exist (docs/operations/BACKUP_PITR_RESTORE_PROOF.md); not re-run in this session.",
          criteria=[
            c("Restore is exact at the current commit (fresh evidence)", N),
            c("A forged or colliding archive is refused", N),
            c("An interrupted restore can be resumed or restarted safely", N),
            c("Object storage, document keys and approval verification material restore with the database", N)]),
        r("Request telemetry, trace context and alert rules fire on induced failures", V,
          "TestAlertDrill_InducedFailuresFireTheRightAlerts", "TestRequestTelemetryAndTraceContext"),
        r("Outbox relay delivers in order, retries, dead-letters and replays; consumers dedupe", V,
          "TestRelayDeliversInOrderExactlyOnceWhenHealthy", "TestRelayCrashAfterPublishRedeliversAndConsumerDedupes", "TestRelayRetryDeadLetterHeadOfLineAndReplay"),
        r("Operated staging and production environments", B, note="Deployment needs the owner's authorisation and infrastructure.",
          criteria=[
            c("Reproducible deployment profiles for internal test and staging", N),
            c("An operated environment deployed under explicit authorisation", B)]),
    ]),
    "XCT-LOC": ("M11", "Localisation", [
        r("Exact decimal money per ISO 4217 minor units; ETB supported", V, "TestParseQuantisesToMinorUnits", "TestAllocateConservesEveryMinorUnit"),
        r("Ethiopic text renders in PDF exports", V, "TestPDFEthiopicFontOnlyWhenNeeded"),
        r("Ethiopian calendar display and input with round-trip tests", I, criteria=[
            c("Exact conversion between the Ethiopian and Gregorian calendars; every day 1900-2100 round-trips; published dates and new years agree", V, "TestEthiopianCalendarRoundTrip", "TestEthiopianCalendarKnownDates"),
            c("The console shows the Ethiopian date, computed in agreement with the server's conversion", V, "TestConsoleBrowserForms"),
            c("Dates can be entered in the Ethiopian calendar in forms", N)]),
        r("Translated interface (Amharic, Tigrinya, Afan Oromo)", N),
        r("Ethiopian statutory tax, payroll and forms", B, note="Needs local legal and accounting approval.",
          criteria=[
            c("Localisation architecture for statutory forms and reporting formats", N),
            c("Current professional legal and accounting review", B)]),
    ]),
    "XCT-API": ("M1", "API and events", [
        r("The OpenAPI contract matches the routes served", V, "TestOpenAPIMatchesRoutes"),
        r("Problem details per RFC 9457 and cursor pagination on every list", I, note="Errors use a consistent {code,message} body; lists are bounded by LIMIT, not cursors.",
          criteria=[
            c("Every error response is application/problem+json with type, title, status and detail", V, "TestProblemDetailsRFC9457"),
            c("Document lists page by an opaque cursor with stable ordering", V, "TestListCursorPagination"),
            c("A cursor cannot cross tenants or change the filters it was issued for", V, "TestListCursorPagination"),
            c("Rows inserted between pages are neither repeated nor skipped for rows that existed at the first page", V, "TestListCursorPagination"),
            c("Lists outside the document framework (journals, stock, people, reports) page by cursor", N)]),
    ]),
    # Added by the final-completion directive (sections 7 and 9). Appended; no earlier ID moves.
    "XCT-FORM": ("M6", "Console form reliability", [
        r("A dependent form applies only the newest answer for each list it loads; an older answer or error never replaces a newer one", V,
          "TestConsoleBrowserForms", "TestConsoleBrowserDocuments",
          note="Barrier-driven (no timers) for six cost-code forms, older success and older failure after a newer answer; the RFI form by its own reproduction. Shown to fail on the previous console."),
        r("Form state (lines, amounts, descriptions) lives in a state object with stable line identities, independent of the DOM", V, "TestConsoleBrowserForms",
          note="lineGrid keeps rows in state; a project change refills choices in place and typed lines survive."),
        r("Changing the project marks lines whose cost code is no longer valid as unresolved; nothing is remapped or deleted silently", V, "TestConsoleBrowserForms"),
        r("Submission is blocked while dependent data is loading and cannot be sent twice", I, "TestConsoleBrowserForms",
          note="Blocking while loading is verified; the guard against a second send exists but has no test of its own."),
        r("Leaving a form with unsaved changes asks first; navigation cancels the form's pending requests", I, "TestConsoleBrowserForms",
          note="Asking first is verified; cancellation on navigation is implemented (AbortController) but not tested."),
        r("Every reloading form has a barrier-driven regression that fails without the fix", I, "TestConsoleBrowserForms",
          note="Nine of eleven: requisition, budget revision, change event, BOQ, schedule, budget transfer, contingency drawdown, stock issue (barrier) and RFI (held answer). Not yet: fuel dispense and work-order part (equipment-driven; the journey has no equipment at two projects)."),
    ]),
    "XCT-UI": ("M6", "SERK AURA experience", [
        r("AURA light and dark token sets meet their contrast targets and the console stylesheet matches them", V,
          "TestAURATokensMatchStylesheet", note="Runs tools/design/aura_contrast.py --check-css."),
        r("AURA warm and high-contrast themes, selectable per user", N),
        r("Sign-in page: accessible labels, password-manager friendly, MFA and SSO when configured, no unsupported claims", N),
        r("Personal workspace: tasks, approvals, deadlines, recent records; layout preferences saved per user and tenant", N),
        r("Application shell: project switcher, command palette, notification centre, permission-aware search", N),
        r("Role workspaces for the roles in the directive, each with its own working queue", N),
        r("Every screen family reviewed by screenshot at the listed widths, with defects recorded and fixed", N),
        r("Automated accessibility checks on every screen family; manual review recorded separately", N),
    ]),
}


# Domains whose requirement list a named owner has reviewed as complete against
# the Bible. Only these can be VERIFIED as a domain. None has been reviewed.
DECOMPOSITION_REVIEWED = set()


def load_pass(path):
    ok = set()
    if not path:
        return ok
    for line in pathlib.Path(path).read_text().splitlines():
        parts = line.split()
        if len(parts) >= 2 and parts[0] == "PASS":
            ok.add(parts[1])
    return ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results", help="file with 'PASS <TestName>' lines from an executed run")
    ap.add_argument("--commit", default="")
    a = ap.parse_args()
    passed = load_pass(a.results)

    names = {}
    with open(ROOT / "spec" / "module-register.csv", encoding="utf-8-sig") as fh:
        for row in csv.DictReader(fh):
            names[row["Product / scope"]] = [m.strip() for m in row["Modules"].split(",")]

    domains, downgraded = [], []
    for prefix, product, key in PRODUCTS:
        for n, module in enumerate(names[key], 1):
            did = f"{prefix}-{n:02d}"
            if did not in D:
                sys.exit(f"{did} ({module}) has no entry")
            milestone, owner, reqs = D[did]
            domains.append(build(did, product, module[:1].upper() + module[1:], milestone, owner, reqs, passed, downgraded))
    if len(domains) != 80:
        sys.exit(f"expected 80 domains, got {len(domains)}")
    cross = [build(k, "PLATFORM", title, ms, "Chief architect", reqs, passed, downgraded) for k, (ms, title, reqs) in X.items()]

    counts, ccounts = {}, {}
    for d in domains + cross:
        for q in d["requirements"]:
            counts[q["status"]] = counts.get(q["status"], 0) + 1
            for k in q["criteria"]:
                ccounts[k["status"]] = ccounts.get(k["status"], 0) + 1
    dcounts = {}
    for d in domains:
        dcounts[d["status"]] = dcounts.get(d["status"], 0) + 1
    out = {
        "source": "spec/module-register.csv; MASTER_PROMPT.md; spec/BIBLE.md",
        "results_file": a.results or "",
        "commit": a.commit,
        "status_vocabulary": [N, I, U, V, B],
        "rule": "VERIFIED means every named test passed in results_file. It is not a human acceptance.",
        "requirement_counts": counts,
        "criteria_counts": ccounts,
        "domain_counts": dcounts,
        "domains": domains,
        "cross_cutting": cross,
    }
    eng = ROOT / "docs" / "engineering"
    eng.mkdir(parents=True, exist_ok=True)
    (eng / "requirements.json").write_text(json.dumps(out, indent=1, ensure_ascii=False) + "\n")
    (eng / "REQUIREMENTS.md").write_text(markdown(out))
    print(json.dumps(counts, sort_keys=True))
    for x in downgraded:
        print("downgraded (evidence test not in results):", x, file=sys.stderr)


def build(did, product, name, milestone, owner, reqs, passed, downgraded):
    items = []
    for i, q in enumerate(reqs, 1):
        crit = []
        for j, k in enumerate(q.get("criteria", [])):
            cid = f"{did}.{i}.{chr(ord('a') + j)}"
            cst = k["status"]
            if cst == V and (not k["tests"] or any(t not in passed for t in k["tests"])):
                cst = U
                downgraded.append(f"{cid}: " + ", ".join(t for t in k["tests"] if t not in passed))
            crit.append({"id": cid, "text": k["text"], "status": cst, "evidence_tests": k["tests"], "note": k["note"]})
        status = q["status"]
        drill_ok = bool(q["drill"]) and (ROOT / q["drill"]).is_file() and "DRILL VERDICT: PASS" in (ROOT / q["drill"]).read_text()
        if status == V and q["drill"] and not drill_ok:
            status = U
            downgraded.append(f"{did}.{i}: no PASS verdict in {q['drill']}")
        elif status == V and not q["drill"] and (not q["tests"] or any(t not in passed for t in q["tests"])):
            status = U
            downgraded.append(f"{did}.{i}: " + ", ".join(t for t in q["tests"] if t not in passed))
        if status == V and any(k["status"] != V for k in crit):
            # A parent is verified only when every acceptance criterion under it is.
            status = U if all(k["status"] in (V, U) for k in crit) else I
            downgraded.append(f"{did}.{i}: acceptance criteria not all verified")
        items.append({"id": f"{did}.{i}", "text": q["text"], "status": status, "evidence_tests": q["tests"], "evidence_file": q["drill"],
                      "note": q["note"], "criteria": crit})
    st = {x["status"] for x in items}
    all_listed = st == {V}
    if all_listed and did in DECOMPOSITION_REVIEWED:
        dstatus = V
    elif all_listed:
        # Every listed requirement is verified, but nobody accountable has
        # confirmed that the list covers the Bible's scope for this domain.
        dstatus = I
    elif st <= {N}:
        dstatus = N
    elif st <= {B} or st <= {B, N} and B in st and len(items) == 1:
        dstatus = B
    elif st <= {V, U}:
        dstatus = U
    else:
        dstatus = I
    return {"id": did, "product": product, "domain": name, "milestone": milestone, "owner": owner, "status": dstatus,
            "all_listed_requirements_verified": all_listed, "requirements": items}


def markdown(out):
    L = ["# Requirement register", "",
         "Generated by `tools/requirements_register.py`; do not edit by hand. Source of the 80 domains: `spec/module-register.csv`.",
         "",
         f"Test results used: `{out['results_file']}`" + (f" at commit `{out['commit']}`" if out["commit"] else "") + ".",
         "",
         "**VERIFIED** on a requirement means every test named as evidence passed in that executed run. It is not a human acceptance. "
         "A domain is VERIFIED only when every listed requirement is verified **and** its owner has reviewed the list as complete "
         "against the Bible; no domain has had that review, so no domain is VERIFIED. A domain marked † has every listed requirement "
         "verified and awaits that review. A domain that is partly built is IMPLEMENTING.",
         "",
         "Requirement counts: " + ", ".join(f"{k} {v}" for k, v in sorted(out["requirement_counts"].items())) + ".", "",
         "Acceptance-criteria counts (children that decompose large requirements; counted separately): "
         + (", ".join(f"{k} {v}" for k, v in sorted(out["criteria_counts"].items())) or "none yet") + ".", "",
         "Domain counts (80): " + ", ".join(f"{k} {v}" for k, v in sorted(out["domain_counts"].items())) + ".", ""]
    by = {}
    for d in out["domains"]:
        by.setdefault(d["product"], []).append(d)
    L += ["## Domain status", "", "| ID | Product | Domain | Milestone | Status |", "|---|---|---|---|---|"]
    for d in out["domains"]:
        L.append(f"| {d['id']} | {d['product']} | {d['domain']} | {d['milestone']} | {d['status']}{' †' if d['all_listed_requirements_verified'] else ''} |")
    L.append("")
    for group, title in ((out["domains"], "Requirements by domain"), (out["cross_cutting"], "Cross-cutting platform requirements")):
        L += [f"## {title}", ""]
        for d in group:
            L += [f"### {d['id']} {d['domain']} — {d['status']}", "", f"Product {d['product']} · milestone {d['milestone']} · owner {d['owner']}", "",
                  "| ID | Requirement | Status | Evidence |", "|---|---|---|---|"]
            for q in d["requirements"]:
                ev = ", ".join([f"`{t}`" for t in q["evidence_tests"]] + ([f"`{q['evidence_file']}`"] if q["evidence_file"] else []))
                if q["note"]:
                    ev = (ev + " — " if ev else "") + q["note"]
                L.append(f"| {q['id']} | {q['text']} | {q['status']} | {ev} |")
                for k in q["criteria"]:
                    kev = ", ".join(f"`{t}`" for t in k["evidence_tests"])
                    if k["note"]:
                        kev = (kev + " — " if kev else "") + k["note"]
                    L.append(f"| {k['id']} | ↳ {k['text']} | {k['status']} | {kev} |")
            L.append("")
    return "\n".join(L)


if __name__ == "__main__":
    main()
