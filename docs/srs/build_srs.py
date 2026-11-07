#!/usr/bin/env python3
"""Builds the SERK Construction OS Software Requirements Specification.

    python3 docs/srs/build_srs.py

Writes docs/srs/SERK_Construction_SRS_v1.0.html and .pdf (Chrome headless,
two passes so the table of contents carries real page numbers). Requirements
live in REQS below as plain data: edit them there, never in the PDF.
"""
import html, os, re, subprocess, sys, tempfile
from collections import OrderedDict

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_HTML = os.path.join(HERE, "SERK_Construction_SRS_v1.0.html")
OUT_PDF = os.path.join(HERE, "SERK_Construction_SRS_v1.0.pdf")
CHROME = os.environ.get("CHROME", "google-chrome")

TITLE = "SERK Construction OS"
VERSION = "v1.0 DRAFT | 2026-11-06"
DATE = "6 November 2026"

PRI = {"M": "Must", "S": "Should", "C": "Could"}
VER = {"T": "Test", "I": "Inspection", "D": "Demo", "A": "Analysis"}
BASE = {"E": "Exists", "P": "Partial", "N": "New"}

# ---------------------------------------------------------------------------
# Functional requirements. "## num|title|intro" starts a section; each other
# line is ID|priority|verification|baseline|text.
# ---------------------------------------------------------------------------
REQS = r"""
## 6.1|Identity, Tenancy and Access Control|Construction companies (tenants), their users, roles, scopes, sessions and delegations. Invariants R2, R3, R4.
IAM-01|M|T|E|The system shall support multiple tenants (construction companies). Each tenant's data shall be isolated in application code and by PostgreSQL row-level security, with the tenant derived from the database login and never from a client-settable value.
IAM-02|M|T|P|The system shall authenticate users through enterprise single sign-on using OpenID Connect and SAML 2.0 (for example Microsoft Entra ID, Google Workspace, Okta, Keycloak). Tenants without an identity provider shall be offered local credentials stored only as Argon2id hashes.
IAM-03|S|T|N|The system shall support SCIM 2.0 user provisioning and deprovisioning; a deprovisioned user's sessions shall be revoked within 60 seconds.
IAM-04|M|T|P|The system shall require multi-factor authentication (WebAuthn/passkeys or TOTP; SMS only if the tenant enables it) for every user holding an approval, posting, payment, security or administration permission, and step-up MFA bound to the specific action within a configurable freshness window (default 10 minutes).
IAM-05|M|T|E|The system shall provide role-based access control with the seed role catalogue of Appendix D, and shall let tenants compose custom roles from the fixed permission list. A role that would hold both the maker and the checker permission of the same control shall be refused.
IAM-06|M|T|E|The system shall restrict every read and command to the projects explicitly assigned to the user, unless the user holds a tenant-wide scope permission.
IAM-07|M|T|P|The system shall evaluate attribute-based rules (legal entity, business unit, project, amount band, document classification, device, principal kind) on every command and read in addition to role checks.
IAM-08|M|T|E|The system shall enforce segregation of duties (maker differs from checker, and checker differs from poster or releaser where configured) for every controlled document, both in the application and by database constraint.
IAM-09|S|T|P|The system shall analyse segregation-of-duties conflicts before a role is assigned, against a tenant-maintained conflict rule set, and shall require a documented mitigating control to accept a conflict.
IAM-10|M|T|E|The system shall distinguish human, service and AI principals from an identity-provider assertion that no role can grant. Only human principals shall approve, certify, post, pay, release, close or configure.
IAM-11|M|T|E|The system shall revoke sessions server-side across all API replicas; a role removal shall take effect on the next request.
IAM-12|M|T|P|The system shall rate-limit authentication and API use with limits shared across replicas, and shall temporarily lock an account after a configurable number of failures and notify the tenant security administrator.
IAM-13|M|T|E|The system shall maintain a delegation-of-authority matrix (approval limits by role, document type, amount band, entity and project). The matrix shall be versioned and approved by maker/checker.
IAM-14|S|T|E|The system shall support time-bound, scoped delegation of approval rights (for example during leave). A delegate shall never exceed the delegator's limits, and delegation shall never enable self-approval.
IAM-15|S|D|N|The system should provide time-limited break-glass access that requires a typed reason, notifies security administrators and is flagged for review.
IAM-16|C|T|N|The system may support tenant conditional-access policies (IP allow-lists, device posture, country restrictions).

## 6.2|Organisation and Master Data|Group structure, projects, business partners, cost structures and resources. Atlas references: modules Ledger, WBS, P2P.
ORG-01|M|T|P|The system shall model group, legal entity, region or branch, business unit and project. Each legal entity shall have its own functional currency, tax registration, chart-of-accounts mapping and fiscal calendar.
ORG-02|M|T|P|The system shall maintain a project master: code, name, client, contract type, location (with GPS coordinates), key dates, project manager, phase, status and stage-gate history.
ORG-03|M|T|E|The system shall maintain supplier and subcontractor masters with maker/checker approval and blocking. Bank accounts shall be dual-approved with MFA and shown masked unless the user holds the reveal permission (which is audited).
ORG-04|M|T|P|The system shall maintain a customer master with TIN, credit limit, payment terms and maker/checker approval.
ORG-05|S|T|N|The system should support supplier and subcontractor pre-qualification: trade licence and contractor grade, tax clearance, VAT registration, insurance and expiry tracking, with alerts before expiry and an evaluated performance rating.
ORG-06|M|T|E|The system shall maintain versioned WBS and cost-code structures with maker/checker approval. Documents shall only reference cost codes in an approved version.
ORG-07|M|T|P|The system shall maintain a resource library (labour trades, plant types, materials, subcontract packages) with units of measure and conversion factors.
ORG-08|S|T|N|The system should detect probable duplicate partners (same TIN, bank account, or similar name) at creation and route them to review.
ORG-09|M|T|P|The system shall maintain working calendars per entity and project, including Ethiopian public holidays, shifts and non-working days.

## 6.3|Business Development, Estimating and Tendering|From opportunity to an approved bid and baseline budget.
EST-01|S|T|N|The system should manage an opportunity and bid pipeline: source, client, value, go/no-go scoring, probability, expected value and bid calendar.
EST-02|M|T|E|The system shall manage bills of quantities (BOQ) in versions with items, units, quantities and rates mapped to cost codes. A submitted version shall be immutable; approval shall be by a different person.
EST-03|M|T|N|The system shall support resource-based estimating: unit-rate build-ups from labour, plant, materials and subcontract resources with productivity factors, plus indirect costs, overhead, risk and margin, drawing on a maintained cost database.
EST-04|S|T|N|The system should compare estimate versions and markup scenarios, and price imported resources in their source currency using approved FX rates.
EST-05|S|D|N|The system should support quantity take-off from 2D drawings and IFC models with each quantity linked to a BOQ item.
EST-06|S|T|N|The system should manage tender packages: documents, clarifications, addenda, bid-bond tracking and a submission checklist.
EST-07|M|T|E|An approved BOQ shall become the project's baseline budget through a controlled transition. It shall never be changed in place.
EST-08|C|D|N|The system may benchmark unit rates and productivity against completed projects.

## 6.4|Project Controls: Budget, Commitment, Cost and Forecast|The core of a construction ERP: knowing at any moment what each project was budgeted, has committed, has spent and will cost. Invariant R8.
PCC-01|M|T|E|The system shall keep the original budget, approved budget revisions, budget transfers between cost codes, and contingency and management-reserve draw-downs, each approved by maker/checker. Original and current budgets shall always be shown side by side.
PCC-02|M|T|E|The system shall record commitments from approved purchase orders and subcontracts in real time. It shall refuse a requisition or order above the remaining budget of its cost code unless an authorised person overrides it with fresh MFA and a reason.
PCC-03|M|T|E|The system shall derive actual cost from posted journals by project and cost code. Each figure shall drill down to its source documents.
PCC-04|M|T|P|The system shall support periodic cost forecasting: estimate to complete and estimate at completion per cost code (manual, CPI-based or remaining-budget methods), forecast versions and an approved monthly forecast cycle.
PCC-05|M|T|P|The system shall produce a cost report showing, per cost code: original budget, revised budget, committed, actual, accrued, forecast, and variances against budget and the last forecast.
PCC-06|M|T|N|The system shall produce a work-in-progress schedule per project (percentage complete, earned revenue, billed to date, over- or under-billing) that feeds revenue recognition (FIN-15).
PCC-07|S|T|N|The system should manage change events (potential, pending, approved) with cost and schedule impact, linked to client variations and subcontract changes.
PCC-08|S|T|N|The system should keep a quantified risk and opportunity register per project and compute contingency at P50 and P80 by Monte Carlo simulation.
PCC-09|M|T|P|The system shall roll up project figures by entity, region, business unit, client, contract type and portfolio.
PCC-10|S|T|N|The system should show project cash flow as planned, actual and forecast S-curves.

## 6.5|Planning and Scheduling|Critical-path scheduling integrated with cost and progress. Atlas reference: ADR 0013.
SCH-01|M|T|E|The system shall compute critical-path schedules with FS, SS, FF and SF relationships, lags, activity calendars and total and free float.
SCH-02|M|T|P|The system shall keep approved baselines (maker/checker) and compare the current schedule against any of several baselines.
SCH-03|M|T|N|The system shall support schedule constraints (start no earlier than, finish no later than, mandatory), activity codes and a WBS hierarchy.
SCH-04|S|T|N|The system should load resources onto activities, level them, and show labour and plant histograms.
SCH-05|M|T|N|The system shall import and export Primavera P6 (XER and XML) and Microsoft Project XML, with an integrity report of anything not carried over.
SCH-06|S|T|N|The system should support look-ahead planning (3 to 6 weeks) and weekly work plans with percent-plan-complete tracking.
SCH-07|S|T|N|The system should support delay analysis: time-impact fragnets and as-planned versus as-built comparison, linked to extension-of-time claims (CON-04).
SCH-08|S|D|N|The system should link schedule activities to model elements for 4D visualisation.
SCH-09|C|T|N|The system may run a schedule quality check (for example the DCMA 14-point assessment).

## 6.6|Progress Measurement and Earned Value|Objective measurement of performance. Atlas reference: ADR 0013.
EVM-01|M|T|E|The system shall record progress reports per activity or cost code, approved by a person other than the author.
EVM-02|M|T|E|The system shall compute PV, EV, AC, SV, CV, SPI, CPI, EAC, ETC and VAC with a documented price basis (nominal or constant) and provenance for every input.
EVM-03|S|T|N|The system should support rules of credit: weighted milestones, units installed, percent complete, start/finish and level of effort, configurable per cost code.
EVM-04|S|T|N|The system should feed installed quantities captured on site (SIT) into progress and client measurement.
EVM-05|S|T|N|The system should attach photo and geotag evidence to progress records.

## 6.7|Procurement and Supply Chain|Procure-to-pay from requisition to payment. Atlas reference: Journey A, ADR 0009. Invariants R2, R8.
PRC-01|M|T|E|The system shall support the flow requisition, RFQ, quotes, technical evaluation, award, purchase order, goods receipt and three-way match, with each step a controlled document.
PRC-02|M|T|E|The system shall enforce a minimum number of suppliers per RFQ unless there is a recorded single-source justification. An award to other than the lowest compliant quote shall require a reason.
PRC-03|M|T|E|Purchase-order approval shall follow the delegation-of-authority matrix and budget control (PCC-02).
PRC-04|S|T|N|The system should support framework agreements and blanket orders with call-offs against agreed price lists.
PRC-05|S|T|N|The system should provide e-sourcing through the supplier portal. Quotes shall be sealed until the due time and opened by two people.
PRC-06|M|T|N|The system shall manage import procurement: proforma, letter of credit (TRS-05), shipping documents, customs clearance, and allocation of landed cost (freight, insurance, duty, clearing) to received items.
PRC-07|S|T|N|The system should track expediting and delivery, and score supplier performance on price, time and quality.
PRC-08|M|T|E|The system shall flag duplicate supplier invoices and split purchases just below approval thresholds.
PRC-09|S|T|N|The system should version purchase-order changes, with re-approval when value, quantity or terms change.

## 6.8|Subcontract Management|Subcontractors typically account for most of a contractor's cost; they need their own controlled lifecycle.
SUB-01|M|T|N|The system shall manage subcontract agreements with a schedule of values or rates, retention percentage, advance payment, scope and terms. They shall commit against the budget (PCC-02).
SUB-02|M|T|N|The system shall process subcontractor payment applications: measured work, certification by a quantity surveyor other than the preparer, retention, advance recovery, back-charges, withholding tax, and generation of the payable.
SUB-03|M|T|N|The system shall manage subcontract variations linked to change events (PCC-07).
SUB-04|S|T|N|The system should track subcontractor compliance (insurance, licences, safety induction) and, if the tenant configures it, block payment while a document is expired.
SUB-05|S|T|N|The system should record back-charges and contra-charges with evidence and the subcontractor's acknowledgement.
SUB-06|S|T|N|The system should support pay-when-certified rules that link subcontractor payment to client certification.
SUB-07|S|D|N|The system should let subcontractors submit applications and documents through the subcontractor portal (COL-03).

## 6.9|Inventory and Materials|Central warehouses and site stores. Atlas reference: Journey C, ADR 0012.
INV-01|M|T|E|The system shall manage warehouses and site stores, with receipts, issues to project cost codes, and approved transfers between stores.
INV-02|M|T|E|The system shall value stock by FIFO or weighted average per tenant policy and reconcile the stock subledger to the inventory control account with zero unexplained difference.
INV-03|M|T|E|The system shall support stock counts with approval. Adjustments shall generate journals approved and posted by people other than the counter.
INV-04|M|T|P|The system shall convert units of measure (for example bag to tonne, piece to metre) and store stock in a base unit.
INV-05|S|T|E|The system should support material requests and reservations against schedule activities.
INV-06|S|T|N|The system should track batches, heat numbers and serial numbers (for example a rebar heat or cement batch) with linked test certificates (QA-04).
INV-07|S|T|E|The system should support reorder points, minimum and maximum levels per store, and suggested requisitions.
INV-08|S|T|N|The system should support returns to supplier and transfers of surplus between projects at valuation.
INV-09|S|T|N|The system should compare material consumption with the theoretical quantity from the BOQ and report wastage.
INV-10|C|D|N|The system may print barcode or QR labels and support mobile scanning.

## 6.10|Equipment and Fleet|Owned, leased and hired plant as a cost and revenue centre.
EQP-01|M|T|N|The system shall keep an equipment register (owned, leased, hired) linked to fixed assets (FIN-06), with ownership and operating cost.
EQP-02|M|T|N|The system shall charge internal hire rates to projects by recorded usage (hours, days or kilometres) through generated journals.
EQP-03|M|T|P|The system shall record daily working, idle and breakdown hours per item of equipment, captured in site diaries or timesheets.
EQP-04|S|T|N|The system should plan preventive maintenance (by hours, distance or calendar), raise work orders and issue spare parts from inventory.
EQP-05|S|T|N|The system should record fuel issues per item of equipment and flag consumption anomalies.
EQP-06|S|T|N|The system should record equipment transfers between sites with a condition report at each hand-over.
EQP-07|C|D|N|The system may import telematics data (ISO 15143-3 / AEMP 2.0).

## 6.11|Workforce, Time and Payroll|Salaried staff and daily labour, costed to projects.
HCM-01|M|T|N|The system shall keep an employee master covering salaried staff and daily labourers: contracts, grade, trade, rate, certifications and bank or mobile-money details (masked).
HCM-02|M|T|N|The system shall capture timesheets per person or crew against project and cost code, including offline, approved by a supervisor other than the person recording them.
HCM-03|S|T|N|The system should capture attendance from biometric devices or a geofenced mobile check-in.
HCM-04|M|T|N|The system shall compute payroll with data-driven, effective-dated statutory rules (Ethiopian employment income tax bands, employee and employer pension contributions, overtime under the labour law in force) confirmed by a qualified adviser.
HCM-05|M|T|N|Payroll shall be approved by maker/checker and shall generate journals by project and cost code, a bank or mobile-money payment file, and statutory returns.
HCM-06|S|T|N|The system should manage leave and overtime requests and approvals.
HCM-07|S|T|N|The system should report labour productivity (units installed per person-hour) against the estimate.
HCM-08|C|D|N|The system may support recruitment and onboarding of site workers, with induction records linked to HSE.

## 6.12|Contracts, Revenue and Claims|Client side: contract to cash. Atlas reference: Journey B, ADR 0011.
CON-01|M|T|E|The system shall manage client contracts with lines or rates, payment terms and a contract policy (retention, advance, VAT base), approved by maker/checker.
CON-02|M|T|E|The system shall support measurement, interim payment certificate (IPC), receivable invoice and receipt, with retention, retention release and advance recovery computed by the server.
CON-03|M|T|E|The system shall manage client variations with approval and their effect on contract value.
CON-04|M|T|N|The system shall keep a claims and extension-of-time register with contractual notices, time bars (for example a 28-day notice period) and alerts before a time bar expires.
CON-05|M|T|N|The system shall apply contract price-adjustment formulas based on published indices, per contract.
CON-06|S|T|N|The system should compute liquidated damages and bonuses from contract dates.
CON-07|M|T|N|The system shall keep a register of guarantees and bonds (bid, advance-payment, performance, retention) with issuer, amount, expiry, reduction and release, and alert before expiry.
CON-08|S|T|N|The system should provide configurable contract templates (FIDIC Red, Yellow and Silver Books; Ethiopian public-procurement standard documents) that set default policies.
CON-09|S|T|N|The system should keep a contract obligations register with owners and due dates.
CON-10|S|D|N|The system should let clients and consultants review measurements and IPCs through the client portal (COL-03).

## 6.13|Finance and Accounting|The deterministic financial core. Atlas references: ADRs 0002, 0003, 0009. Invariant R1.
FIN-01|M|T|E|The system shall record journals as balanced, immutable double entries in whole minor units, with preparer, approver and poster distinct. Corrections shall be by reversal only.
FIN-02|M|T|E|Each journal generated from a business document shall carry a unique source-event identifier so that no event can be posted twice.
FIN-03|M|T|E|The system shall manage accounting periods. Posting into a closed period shall be refused, and reopening shall need a privileged, audited permission.
FIN-04|M|T|E|The system shall support multi-currency transactions with approved rates selected by an approved FX policy, and remeasurement under IAS 21.
FIN-05|M|T|E|The system shall provide an accounts payable subledger and an accounts receivable subledger with aging, reconciled to their control accounts.
FIN-06|M|T|N|The system shall manage fixed assets under IAS 16: acquisition, components, depreciation methods, transfers, revaluation, impairment and disposal.
FIN-07|M|T|P|The system shall apply data-driven, effective-dated tax rules: output and input VAT, withholding tax on payments and receipts, and excise where applicable. It shall produce the supporting schedules for tax returns.
FIN-08|M|T|N|The system shall consolidate multiple entities, with intercompany transactions matched and eliminated automatically, and joint ventures accounted for under the tenant's policy.
FIN-09|M|T|P|The system shall produce trial balance, general ledger, income statement, balance sheet and cash-flow statement per entity and consolidated, in IFRS presentation.
FIN-10|S|T|N|The system should support recurring journals, and accruals and prepayments with automatic reversal.
FIN-11|S|T|N|The system should allocate overheads to projects by configurable rules.
FIN-12|S|T|N|The system should support corporate (overhead) budgets with variance reporting.
FIN-13|S|T|N|The system should support the Ethiopian fiscal year (Hamle to Sene) and display dates in the Ethiopian calendar.
FIN-14|S|A|N|The system should provide an adapter for tax-authority e-invoicing and fiscal-receipt obligations, once their applicability and format are confirmed (Appendix E).
FIN-15|M|T|N|The system shall recognise contract revenue over time under IFRS 15 (input or output method per contract) and maintain contract assets and liabilities.

## 6.14|Treasury and Cash Management|Payments as records, settlement from the bank. Atlas reference: Journey A steps 15-19.
TRS-01|M|T|E|Payments shall be prepared, approved (dual approval above a threshold) and released by different human principals with fresh MFA. The system shall never move money itself; settlement shall be recognised only from imported bank statements.
TRS-02|M|T|P|The system shall forecast cash for 13 weeks and for the project horizon, separating confirmed from forecast flows and including payroll, taxes, supplier schedules, expected IPC receipts and debt service.
TRS-03|S|T|N|The system should exchange payment files (ISO 20022 pain.001) and statements (camt.053, MT940 or bank CSV) with banks where they support it.
TRS-04|S|T|N|The system should track bank facilities, overdrafts and guarantee lines, and their utilisation.
TRS-05|S|T|N|The system should manage letters of credit for imports, from application to settlement.
TRS-06|C|T|N|The system may manage site petty cash on an imprest basis, with replenishment approvals.

## 6.15|Site Operations and Mobile|Work where it happens, on intermittent connectivity. Atlas reference: ADR 0017. Invariant R6.
SIT-01|M|T|E|The system shall record daily site diaries (weather, labour, equipment, notes, instructions), approved by a person other than the author.
SIT-02|M|T|E|Only registered, revocable devices shall sync offline work. Sync shall be idempotent per device and operation id, and a conflict shall be returned to the user, never merged silently.
SIT-03|M|D|N|The system shall provide an offline-first mobile application (Android first, then iOS) for diaries, timesheets, goods receipts, inspections, incidents, photos and approvals.
SIT-04|M|T|E|Offline devices shall prepare commands only. They shall never create authoritative financial state.
SIT-05|S|T|N|The system should capture photos with GPS position, time and watermark, and link them to any record.
SIT-06|S|T|N|The system should manage punch and snag lists with assignees, due dates and close-out photos.
SIT-07|S|T|N|The system should record site instructions and meeting minutes with tracked actions.
SIT-08|C|D|N|The system may link drone or 360-degree captures to locations and dates.

## 6.16|Document Control and BIM|A common data environment for drawings, models and correspondence.
DOC-01|M|T|N|The system shall provide a common data environment following ISO 19650 (work in progress, shared, published, archived), with tenant-defined numbering rules and metadata.
DOC-02|M|T|N|The system shall manage document revisions. A superseded revision shall never be presented as current.
DOC-03|M|T|N|The system shall issue transmittals and record each recipient's acknowledgement.
DOC-04|M|T|N|The system shall manage RFIs, with ball-in-court, due dates, responses, and links to cost and schedule impact.
DOC-05|S|T|N|The system should manage submittals (shop drawings, material approvals) with review status and stamps.
DOC-06|S|D|N|The system should keep a drawing register and allow mark-up in the browser.
DOC-07|S|D|N|The system should view IFC models in the browser and exchange model-based issues in BCF.
DOC-08|M|T|N|Files shall be encrypted at rest, scanned for malware and served only through short-lived signed URLs after an authorisation check.
DOC-09|S|T|N|The system should provide permission-filtered full-text search over documents, including text extracted by OCR.

## 6.17|Quality Management|Inspection and test plans, non-conformances, material tests.
QA-01|M|T|N|The system shall manage inspection and test plans per activity or work package, with hold and witness points.
QA-02|M|T|N|The system shall record inspection requests and results. If the tenant configures it, progress or measurement on an activity with an unreleased hold point shall be refused.
QA-03|M|T|N|The system shall manage non-conformance reports with root cause, corrective and preventive action, cost impact and verified closure.
QA-04|S|T|N|The system should record material test results (for example concrete cubes or rebar tensile tests) linked to batches (INV-06).
QA-05|S|T|N|The system should feed quality outcomes into supplier and subcontractor ratings.

## 6.18|Health, Safety and Environment|Safety records that are timely and impossible to quietly lose.
HSE-01|M|T|N|The system shall record incidents and near misses (including offline), with investigation, root cause and corrective actions tracked to closure.
HSE-02|M|T|N|The system shall manage permits to work (hot work, confined space, work at height, excavation, lifting) with approvals, conditions and expiry.
HSE-03|S|T|N|The system should record inductions, toolbox talks and training, and can be configured to block site access or timesheets for workers who have not been inducted.
HSE-04|S|T|N|The system should run safety inspection and observation checklists.
HSE-05|M|T|N|The system shall compute HSE indicators (LTIFR, TRIR, severity rate) using hours worked from timesheets.
HSE-06|C|T|N|The system may record environmental aspects: waste, spills and environmental permits.

## 6.19|Workflow, Approvals and Automation|Configurable routing in which only people decide. Atlas references: ADRs 0007, 0008, 0015. Invariants R2, R3.
WFL-01|M|T|E|The system shall execute versioned workflow definitions declaring steps, permissions, quorums, SoD rules, MFA, amount thresholds, SLA escalation and expiry. A validator shall refuse any definition that weakens a control.
WFL-02|M|T|E|When a definition is published as mandatory, the database shall refuse the subject's sensitive transition unless the same transaction recorded the workflow effect with its full quorum.
WFL-03|M|T|E|Every human decision shall carry an attestation signed by the isolated approval signer and verified by the database.
WFL-04|M|T|P|The system shall present one task inbox across all modules, on web and mobile, with step-up MFA for sensitive decisions.
WFL-05|S|T|N|The system should provide a visual workflow designer that produces the same validated definition format.
WFL-06|M|T|E|Automation rules shall only notify or prepare drafts. The rule's author and its activator shall be different people.
WFL-07|S|T|N|The system should redirect tasks on absence through approved delegations (IAM-14).

## 6.20|Reporting, Dashboards and Analytics|Trusted numbers, traced to their documents.
RPT-01|M|T|P|The system shall provide a maintained catalogue of standard reports across all modules (at least 60 at general availability; currently 11 of the 25 initially listed exist).
RPT-02|M|T|E|Every report and list shall export to PDF, Word and Excel with the tenant's letterhead. Exports shall contain no more than the user can see, and shall be watermarked and audited.
RPT-03|M|T|P|The system shall provide role dashboards (executive, project, finance, procurement, HSE). Every KPI shall have a documented formula and drill down to its source documents.
RPT-04|S|T|N|The system should offer a self-service report builder over a governed semantic layer, with saved filters and views.
RPT-05|S|T|N|The system should deliver reports by email on a schedule, subject to the recipient's permissions.
RPT-06|M|T|P|Every report shall display its filters and data-freshness timestamp.
RPT-07|S|T|N|The system should provide a read-only analytics feed (warehouse export and BI connectors such as Power BI) that preserves tenant and project isolation.
RPT-08|S|T|N|The system should produce statutory reports: VAT return, withholding tax, pension and payroll tax schedules.

## 6.21|Embedded Intelligence and AI|AI that advises and drafts but never decides. Atlas reference: platform 0038. Invariant R7.
AI-01|S|D|P|The system should answer operational questions (cost overrun, schedule slip, cash, overdue orders) from permission-filtered facts, citing sources, freshness, assumptions and missing data.
AI-02|M|T|E|AI principals shall not approve, certify, post, pay or release. Their only write capability shall be preparing drafts for a human.
AI-03|M|T|E|All numbers shown shall come from deterministic computation. Any language-model phrasing shall be checked against the facts and discarded if it introduces a number not in them.
AI-04|S|T|N|The system should extract data from supplier invoices, delivery notes and certificates into draft documents with per-field confidence. A person shall confirm each draft.
AI-05|S|T|P|The system should predict cost-overrun, schedule-slip, cash-shortfall and supplier-delay risk with explanations, and record each prediction so it can be compared with the rule-based baseline and with outcomes.
AI-06|M|T|P|Untrusted content (documents, OCR output, free text) shall be treated as data, never as instructions. Prompt-injection red-team tests shall run in CI.
AI-07|M|I|N|Use of an external model shall require tenant opt-in, data minimisation and an approved data-residency decision. A tenant administrator shall be able to disable all AI features.
AI-08|M|I|P|AI interactions (user, question, facts used, timestamp) shall be logged without unnecessary personal data.

## 6.22|Audit, Compliance and Traceability|Evidence that survives an auditor and an attacker. Invariant R9.
AUD-01|M|T|E|The system shall write a hash-chained, append-only audit record for every command, decision, export, sensitive-data reveal, login and configuration change.
AUD-02|M|T|E|Every business change shall write its audit record and outbox event in the same transaction.
AUD-03|M|T|E|Posted records shall be immutable to every application login and to the schema owner (database triggers). An integrity scan shall detect out-of-band tampering.
AUD-04|M|T|P|Audit logs shall be readable only by authorised roles, and reading them shall itself be audited. External auditors shall get time-boxed, read-only access.
AUD-05|M|T|P|The system shall provide a control review report: SoD exceptions, budget overrides, approvals outside SLA, reopened periods and revealed bank details.
AUD-06|M|T|P|The system shall trace end to end from payment to invoice, receipt, purchase order, award, requisition, budget line and BOQ item, and from receipt to IPC, measurement and contract line.
AUD-07|S|A|N|The system should generate a period evidence pack for the external auditor.

## 6.23|Collaboration, Notifications and Portals|Bringing clients, consultants, suppliers and subcontractors into controlled workflows.
COL-01|M|T|E|The system shall raise in-app notifications from outbox events after commit.
COL-02|S|T|N|The system should deliver notifications by email, SMS and messaging channels (for example Telegram), configurable by role and user preference, with retry.
COL-03|S|T|N|The system should provide external portals for clients, consultants, suppliers and subcontractors. Portal identities shall be limited to their own records and actions.
COL-04|S|T|N|The system should support comments and mentions on any record, retained with the record's audit trail.

## 6.24|Integration and Interoperability|Open by design, so that no tenant is locked in.
INT-01|M|I|E|All APIs shall be versioned, documented in OpenAPI 3, authenticated and rate-limited. Every command shall accept an idempotency key.
INT-02|M|T|E|Domain events shall be delivered as signed webhooks from the transactional outbox, with per-aggregate ordering, retries, a dead-letter queue and replay.
INT-03|S|T|N|The system should offer bulk import and export (CSV and Excel templates) through a validated staging area with review before commit.
INT-04|S|T|N|The system should provide connectors for Primavera P6, Microsoft Project, Microsoft 365 and SharePoint, Google Drive and Power BI.
INT-05|S|T|N|The system should connect to Ethiopian commercial banks and to mobile-money services (for example telebirr) where they offer interfaces.
INT-06|M|T|P|External adapters shall be isolated behind timeouts, retries with back-off and circuit breakers, so that no outside failure blocks internal transactions.
INT-07|S|T|N|The system should provide migration tooling from common legacy systems (Peachtree/Sage, QuickBooks, Excel, SAP Business One), with reconciliation reports.

## 6.25|Administration, Configuration and Localisation|Tenant self-service without code.
ADM-01|M|D|P|The system shall provide guided tenant onboarding: entities, chart of accounts (construction IFRS template), taxes, banks, users, roles and policies.
ADM-02|M|T|E|Operational policies (P2P, contract, stock, FX, thresholds) shall be versioned, maker/checker approved and audited.
ADM-03|M|T|P|The system shall manage company settings: letterhead, document numbering, locale and portal branding.
ADM-04|M|T|P|The interface shall be available in English and Amharic at launch. Afaan Oromo and Tigrinya shall be addable without code changes. Translation shall never alter canonical data.
ADM-05|S|T|N|The system should let administrators add custom fields per document type, which shall be validated and available in reports.
ADM-06|S|D|P|The system should show administrators backup, queue, sync and integration health.
ADM-07|S|T|N|The system should create sandbox copies of a tenant, with personal data masked, for training and testing.
ADM-08|M|T|N|The system shall support configurable retention periods and legal holds per record type.
"""

# ---------------------------------------------------------------------------
# Non-functional requirements.
# ---------------------------------------------------------------------------
NFRS = r"""
## 7.1|Performance and Capacity|Proposed baselines for agreement; validated by load tests on a production-like environment (phase R4). Server-side, excluding client network latency.
NFR-PERF-01|M|T|N|Command endpoints shall respond within 1 s at the 95th percentile and 3 s at the 99th percentile under the reference load.
NFR-PERF-02|M|T|N|Lists and detail reads shall respond within 500 ms at the 95th percentile. Search shall respond within 1 s over 5 million documents per tenant.
NFR-PERF-03|S|T|N|Reports over up to 100,000 rows shall complete within 10 s. Larger reports shall run asynchronously and notify the user.
NFR-PERF-04|M|T|N|The reference deployment shall support 2,000 concurrent users, 500 active projects per tenant and 50 million journal lines per tenant without breaching NFR-PERF-01.
NFR-PERF-05|S|T|N|A device with 1,000 queued offline commands shall finish syncing within 5 minutes on a 3G-class connection.
NFR-PERF-06|S|T|N|Over a constrained connection, the web console shall load in under 1.5 MB (compressed) on first visit.

## 7.2|Availability, Reliability and Recovery|Invariant R9.
NFR-AVL-01|M|A|N|The SaaS service shall achieve 99.9 % monthly availability for transactional services, excluding announced maintenance (99.5 % during the first six months after launch).
NFR-AVL-02|M|T|P|The recovery point objective shall be at most 5 minutes (point-in-time recovery) and the recovery time objective at most 4 hours, or 1 hour for the premium tier.
NFR-AVL-03|M|T|E|An interruption at any point shall leave each transaction fully committed or fully rolled back. Replaying the request shall be safe.
NFR-AVL-04|M|T|P|A failure of reporting, AI, notification, export or integration services shall not prevent transactions.
NFR-AVL-05|M|T|P|Backups shall be encrypted, copied to an isolated off-site location in object-locked storage, and restore-tested at least quarterly, with a signed record of each test.
NFR-AVL-06|S|I|N|Deployments and migrations shall be backward-compatible so that releases need no downtime during working hours.

## 7.3|Security|Baseline standard: OWASP ASVS. Invariants R2, R3, R4, R7.
NFR-SEC-01|M|I|P|All traffic shall use TLS 1.2 or higher (TLS 1.3 preferred), with HSTS on public endpoints.
NFR-SEC-02|M|I|P|Data at rest shall be encrypted. Bank details and personal identifiers shall also have field-level encryption.
NFR-SEC-03|M|A|P|The system shall meet OWASP ASVS Level 2, with Level 3 for authentication, approvals, payments and administration.
NFR-SEC-04|M|I|P|The approval-signing key shall be held in an HSM or cloud KMS under a documented key ceremony. Other secrets shall be held in a vault and rotated. No secret shall be in source control or client code.
NFR-SEC-05|M|I|P|The CI pipeline shall run SAST, dependency vulnerability scanning (govulncheck), secret scanning, checksum verification and SBOM generation, and shall sign container images (target SLSA level 3).
NFR-SEC-06|M|T|E|Automated cross-tenant and privilege-escalation probes shall run in CI against the database as a tenant login and as the schema owner.
NFR-SEC-07|M|A|N|An independent penetration test shall close all critical and high findings before general availability, and shall be repeated at least annually.
NFR-SEC-08|M|I|P|Security events shall be logged centrally (SIEM export) with alerts. Logs shall never contain passwords, tokens or bank details.
NFR-SEC-09|M|T|P|Exports shall be subject to per-user volume budgets, watermarks and anomaly alerts (anti-exfiltration).
NFR-SEC-10|S|A|N|The vendor should obtain ISO/IEC 27001 certification and a SOC 2 Type II report within 18 months of general availability.

## 7.4|Privacy and Regulatory Compliance|Not legal advice: applicability shall be confirmed by qualified counsel.
NFR-PRV-01|M|A|N|The system shall support compliance with the Ethiopian Personal Data Protection Proclamation No. 1321/2024 (minimisation, purpose limitation, data-subject rights, breach notification, cross-border restrictions), and with GDPR for tenants that need it.
NFR-PRV-02|M|A|N|Tenants shall be able to choose in-country hosting or on-premises deployment (data residency).
NFR-PRV-03|M|A|N|Retention periods for financial, payroll, contract and HSE records shall be set to legal minimums confirmed by counsel. Deletion shall be by approved purge or anonymisation only.
NFR-PRV-04|M|I|N|A documented incident and breach-response procedure, and a data-processing agreement template, shall exist before general availability.
NFR-PRV-05|M|A|P|Financial and tax treatment (IFRS, VAT, withholding, payroll) shall be reviewed and signed by a qualified accountant or tax adviser before live use.

## 7.5|Usability, Accessibility and Localisation|
NFR-USB-01|M|I|P|Non-mobile screens shall conform to WCAG 2.1 level AA, and every function shall be usable by keyboard.
NFR-USB-02|S|D|N|A clerk shall be able to work a standard document (requisition, goods receipt, invoice) after no more than one day of training, to be validated in UAT.
NFR-USB-03|M|T|P|Layouts shall adapt from desktop to tablet and phone. Approvals and dashboards shall be fully usable on a phone.
NFR-USB-04|M|T|P|Ethiopic script shall render correctly on screen and in every export format.
NFR-USB-05|S|T|N|Users should be able to display dates in the Ethiopian or Gregorian calendar. Timestamps shall be stored in UTC and displayed by default in the Africa/Addis_Ababa time zone.
NFR-USB-06|M|T|E|The server's refusals shall be explained in plain language that names the rule and the action that would satisfy it.
NFR-USB-07|S|I|N|The product should apply one documented design system across web, mobile and portals.

## 7.6|Maintainability, Portability and Operations|Atlas references: ADRs 0001 and 0016.
NFR-MNT-01|M|I|E|The system shall be a modular monolith with explicit module boundaries. Modules shall not write each other's tables directly.
NFR-MNT-02|M|I|E|Database migrations shall be additive and checksum-locked. A committed migration shall never be edited.
NFR-MNT-03|M|T|P|Automated tests shall cover every invariant R1 to R9, and line coverage of the financial, approval, stock, contract and sync modules shall be at least 80 %.
NFR-MNT-04|M|I|P|The system shall be delivered with an OpenAPI specification, ER diagram, ADRs and operational runbooks, kept current with each release.
NFR-MNT-05|M|D|P|The system shall be containerised and deployable as multi-tenant SaaS, as a dedicated single-tenant instance, and on premises (including air-gapped), depending only on PostgreSQL and S3-compatible storage.
NFR-MNT-06|M|I|P|The system shall emit structured logs, metrics and OpenTelemetry traces with correlation IDs, with SLO-based alerts for availability, latency, outbox lag and ledger integrity.
NFR-MNT-07|S|T|N|API replicas and workers shall scale horizontally. Large tenants shall be placeable on a dedicated database.
NFR-MNT-08|M|T|P|The supported clients shall be the current and previous major versions of Chrome, Edge, Firefox and Safari, and Android 10 or later for the mobile application.

## 7.7|Data Integrity|Invariant R1.
NFR-DAT-01|M|I|E|Critical invariants shall be enforced by database constraints and triggers: balance, uniqueness of source events, immutability, period state, tenant identity and non-negative stock.
NFR-DAT-02|M|T|E|Financial, contractual and audit records shall never be hard-deleted.
NFR-DAT-03|M|T|E|Money shall use exact decimal types (never floating point), with explicit and tested rounding and allocation.
NFR-DAT-04|M|T|E|The server clock shall be authoritative. Device-claimed times shall be stored as evidence beside server receipt times.
NFR-DAT-05|S|T|P|A legacy data migration shall reconcile opening balances, open documents and stock to the source system's totals before go-live.
"""

INVARIANTS = [
    ("R1", "Every posted journal balances, is in whole minor units, lies in an open period, is immutable, and its sources reconcile to the GL.", "FIN-01..05, INV-02, NFR-DAT-01..03"),
    ("R2", "No one approves, certifies, posts or releases their own work (maker/checker), in the application and in the database.", "IAM-08, IAM-09, WFL-01..03, TRS-01"),
    ("R3", "Only authenticated humans decide. Automation, service and AI principals cannot approve, post, pay or release.", "IAM-10, WFL-06, AI-02"),
    ("R4", "Tenant, entity and project data are isolated; exports show no more than the screen.", "IAM-01, IAM-06, RPT-02, NFR-SEC-06"),
    ("R5", "Exactly one effect per command or event, whatever the retries, duplicates or reordering.", "FIN-02, INT-01, INT-02, SIT-02"),
    ("R6", "Offline devices only prepare commands; conflicts are shown, never merged silently.", "SIT-02, SIT-04"),
    ("R7", "The assistant sees only what the user may see, uses only computed numbers, and cannot act.", "AI-02, AI-03, AI-06, AI-07"),
    ("R8", "No commitment beyond the approved budget without an attested, MFA-backed override.", "PCC-01, PCC-02, PRC-03"),
    ("R9", "Tampering is detectable and a backup restores to a consistent, verifiable system.", "AUD-01..03, NFR-AVL-02, NFR-AVL-05"),
]

GATES = [
    ("R1 Ledger integrity", "FINANCE: journal invariants, subledger reconciliation, journeys A-C (kernel, HTTP, browser)", "Zero integrity-scan rows; reconciliation reports"),
    ("R2 Separation of duties", "SECURITY: forged-approval probes as tenant SQL and as schema owner; review regression suites", "Before/after logs; probe results"),
    ("R3 Human-only decisions", "GOVERNANCE: AI/service-principal refusal tests in Go and SQL", "CI results"),
    ("R4 Isolation", "SECURITY: cross-tenant, cross-project and export probes", "Probe results; denial audit entries"),
    ("R5 Exactly once", "SYNC/INTEGRATION: duplicate, reordered and replayed commands, webhooks and payments", "Replay test results"),
    ("R6 Offline restrictions", "OFFLINE: revoked device, stale base version, network cut mid-sync", "Conflict evidence"),
    ("R7 Governed AI", "AI: prompt-injection red team, permission filtering, number verification", "Red-team report"),
    ("R8 Budget control", "PROJECT CONTROLS: over-budget requisition/PO/subcontract, override without MFA", "CI results"),
    ("R9 Recoverability", "OPS: PITR restore drill, encrypted off-site restore, audit-chain verification", "Signed drill records"),
]

USE_CASES = [
    ("UC-01", "Win and set up a project", "BD manager, Estimator, Commercial manager", "Opportunity > go/no-go > estimate > tender > award > BOQ approved > baseline budget, WBS and schedule", "EST, ORG, PCC, SCH"),
    ("UC-02", "Buy materials for site", "Site engineer, Procurement, PM", "Requisition > budget check > RFQ > quotes > evaluation > award > PO approval", "PRC, PCC"),
    ("UC-03", "Receive, store and issue", "Storekeeper, Site engineer", "GRN > confirm > stock receipt > issue to cost code > count", "PRC, INV, FIN"),
    ("UC-04", "Pay a supplier", "AP clerk, AP supervisor, Treasury, CFO", "Invoice > three-way match > approval > payment prepare > dual approve > release > bank statement > reconcile", "PRC, FIN, TRS"),
    ("UC-05", "Pay a subcontractor", "QS, Commercial manager, Treasury", "Application > measurement > certificate > retention/advance/back-charges > payable > payment", "SUB, FIN, TRS"),
    ("UC-06", "Bill the client and get paid", "QS, Commercial manager, AR", "Measurement > IPC > client certification > invoice > receipt > retention release", "CON, FIN"),
    ("UC-07", "Manage change", "PM, QS, Commercial manager", "Change event > estimate > client variation and subcontract change > budget revision > schedule update", "PCC, CON, SUB, SCH"),
    ("UC-08", "Run the monthly project review", "PM, Project controls, Finance", "Progress > EVM > forecast to complete > WIP > cost report > approved forecast", "EVM, PCC, FIN"),
    ("UC-09", "Record the day on site, offline", "Site engineer, Foreman", "Diary, timesheets, equipment hours, photos > sync > supervisor approval", "SIT, HCM, EQP"),
    ("UC-10", "Close the period", "Accountant, Controller, CFO", "Subledger reconciliations > accruals > depreciation > payroll > WIP > review TB > close > consolidate", "FIN"),
    ("UC-11", "Run payroll", "HR officer, Payroll checker, Finance", "Timesheets > payroll run > checks > approval > journal > payment file > statutory returns", "HCM, FIN, TRS"),
    ("UC-12", "Handle a safety incident", "Any worker, HSE officer, PM", "Report (offline) > notify > investigate > actions > close > KPIs", "HSE"),
    ("UC-13", "Answer an RFI", "Site engineer, Consultant", "RFI > ball-in-court > response > linked drawing revision > change event if needed", "DOC, PCC"),
    ("UC-14", "Ask the assistant", "Executive, PM", "Question > permission-filtered facts > deterministic answer > sources and assumptions", "AI"),
]

ACTORS = [
    ("Executive / Owner / CFO", "Portfolio performance, cash, risk", "Group-wide read, high-value approvals, policy approval, period reopen"),
    ("Business development / Estimator", "Win profitable work", "Opportunities, estimates, BOQ drafting"),
    ("Commercial manager / Quantity surveyor", "Protect contract value", "BOQ approval, measurement, IPC, variations, claims, subcontract certification"),
    ("Project manager / Project controls / Planner", "Deliver on time and budget", "Budget revisions, requisition approval, schedules, progress, forecasts"),
    ("Site engineer / Foreman", "Execute work safely", "Requisitions, receipt confirmation, diaries, timesheets, inspections, incidents (mobile, offline)"),
    ("Procurement officer / manager", "Buy right, on time", "Suppliers, RFQs, awards, purchase orders, subcontracts"),
    ("Storekeeper / Inventory controller", "Account for every item", "Receipts, issues, transfers, counts and their approval"),
    ("Equipment manager", "Keep plant productive", "Register, usage, maintenance, fuel, transfers"),
    ("HR / Payroll officer", "Pay people correctly", "Employees, timesheets, payroll, statutory returns"),
    ("Accountant / AP / AR / Controller / Finance manager", "Correct, timely books", "Journals, subledgers, reconciliations, close, consolidation"),
    ("Treasury officer / Bank statement officer", "Liquidity and safe payments", "Payment preparation and release; independent statement import"),
    ("QA/QC and HSE officers", "Quality and zero harm", "ITPs, NCRs, permits, incidents, inspections"),
    ("Auditor (internal / external)", "Independent assurance", "Read-only data and audit log, time-boxed"),
    ("Process owner / Workflow admin / Security admin", "Govern the system", "Workflows, automation, settings, sessions, devices; no financial authority"),
    ("External: client, consultant, supplier, subcontractor", "Collaborate in their own scope", "Portal access to their own documents and actions"),
]

MATRIX_COLS = ["Est.", "QS/Comm.", "PM", "Site", "Proc.", "Store", "AP/AR", "Treas.", "Fin./Ctrl", "CFO", "HR", "Aud.", "Admin"]
MATRIX = [
    ("BOQ / estimate", "C", "A", "R", "-", "-", "-", "-", "-", "R", "R", "-", "R", "-"),
    ("Budget revision / transfer", "-", "R", "C", "-", "-", "-", "-", "-", "R", "A", "-", "R", "-"),
    ("Requisition", "-", "-", "A", "C", "R", "-", "-", "-", "-", "-", "-", "R", "-"),
    ("RFQ, award, PO", "-", "-", "R", "X", "C/A", "-", "-", "-", "-", "A*", "-", "R", "-"),
    ("Goods receipt", "-", "-", "R", "A", "R", "C", "R", "-", "R", "-", "-", "R", "-"),
    ("Supplier invoice", "-", "-", "-", "-", "R", "-", "C/A", "-", "A*", "-", "-", "R", "-"),
    ("Payment prepare / release", "-", "-", "-", "-", "-", "-", "-", "X", "A", "A", "-", "R", "-"),
    ("Subcontract certificate", "-", "C/A", "R", "R", "R", "-", "R", "-", "R", "R", "-", "R", "-"),
    ("Measurement / IPC", "-", "C/A", "R", "A", "-", "-", "R", "-", "R", "R", "-", "R", "-"),
    ("Client variation / claim", "-", "C", "R", "-", "-", "-", "-", "-", "R", "A", "-", "R", "-"),
    ("Stock count / adjustment", "-", "-", "-", "-", "-", "C", "-", "-", "A", "-", "-", "R", "-"),
    ("Schedule / progress", "-", "R", "A", "C", "-", "-", "-", "-", "-", "R", "-", "R", "-"),
    ("Timesheets / payroll", "-", "-", "R", "C", "-", "-", "-", "-", "A", "R", "X", "R", "-"),
    ("Journal create/approve/post", "-", "-", "-", "-", "-", "-", "C", "-", "A/P", "R", "-", "R", "-"),
    ("Period close / reopen", "-", "-", "-", "-", "-", "-", "-", "-", "X", "A", "-", "R", "-"),
    ("Workflow / automation rules", "-", "-", "-", "-", "-", "-", "-", "-", "-", "A", "-", "R", "C"),
    ("Users, sessions, devices", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "R", "X"),
    ("Audit log", "-", "-", "-", "-", "-", "-", "-", "-", "R", "R", "-", "R", "R"),
    ("Assistant / risk view", "-", "R", "R", "-", "R", "-", "-", "R", "R", "R", "-", "R", "-"),
    ("Export reports", "R", "X", "X", "-", "X", "-", "X", "X", "X", "X", "X", "X", "-"),
]

TESTS = [
    ("FIN-T01", "Unbalanced, closed-period or self-approved journal is refused by Go and by PostgreSQL", "FIN-01..03", "gate_a_ledger_test, review regressions"),
    ("FIN-T02", "Journeys A, B and C reconcile to the GL with zero difference", "FIN-05, INV-02, AUD-06", "journey_a/b/c_test, browser journeys"),
    ("SOD-T01", "Forged approval as tenant SQL or schema owner is refused", "IAM-08, WFL-03", "approval_provenance_test, attestation_*_test"),
    ("SOD-T02", "Concurrent decisions execute the effect exactly once", "WFL-01, FIN-02", "workflow_test (concurrency)"),
    ("AI-T01", "AI or service principal cannot approve, post, pay or release", "IAM-10, AI-02", "governed_ai_test"),
    ("AI-T02", "Injected instructions in a document cannot reach data or tools", "AI-06", "New: red-team suite"),
    ("ISO-T01", "Cross-tenant and cross-project reads and exports are refused", "IAM-01, IAM-06, RPT-02", "gate_a_tenancy_test, platform_test"),
    ("SYN-T01", "Duplicate, reordered and post-revocation sync commands", "SIT-02, SIT-04", "site_offline_test"),
    ("BUD-T01", "Over-budget PO without MFA override is refused", "PCC-02, PRC-03", "journey_a_controls_test"),
    ("SUB-T01", "Subcontract certificate retention, advance and back-charge arithmetic", "SUB-02", "New"),
    ("PAY-T01", "Payroll statutory computation against an adviser-signed fixture set", "HCM-04", "New"),
    ("REV-T01", "IFRS 15 over-time revenue and WIP schedule reconcile to the GL", "PCC-06, FIN-15", "New"),
    ("OPS-T01", "Point-in-time restore and encrypted off-site restore", "NFR-AVL-02, NFR-AVL-05", "BACKUP_PITR_RESTORE_PROOF"),
    ("OPS-T02", "Alert drill fires on induced failures only", "NFR-MNT-06", "metrics_drill_test"),
    ("PERF-T01", "Reference load and soak test", "NFR-PERF-01..04", "New"),
]

DECISIONS = [
    ("D-01", "Mobile technology: native Android/iOS versus PWA for the offline field application (SIT-03)", "Technical lead", "Before R3"),
    ("D-02", "Hosting: SaaS region, in-country hosting partner, on-premises support policy (NFR-PRV-02)", "Owner / Legal / Technical lead", "Before R1 GA"),
    ("D-03", "Identity provider for SaaS tenants, and HSM/KMS for the approval signer (IAM-02, NFR-SEC-04)", "Security lead", "Before R1 GA"),
    ("D-04", "Payroll statutory rules and their sign-off by a qualified adviser (HCM-04)", "Finance / HR / Tax adviser", "Before R2"),
    ("D-05", "IFRS 15 method per contract type, WIP policy and JV accounting (FIN-15, FIN-08)", "Finance lead / Auditor", "Before R2"),
    ("D-06", "Tax-authority e-invoicing and fiscal obligations and format (FIN-14)", "Tax adviser", "Before R2"),
    ("D-07", "Contract templates in scope (FIDIC editions, Ethiopian PPA documents) (CON-08)", "Commercial lead / Counsel", "Before R2"),
    ("D-08", "Bank and mobile-money integrations for release 1 (INT-05, TRS-03)", "Owner / Treasury", "Before R2"),
    ("D-09", "Retention periods and legal holds (NFR-PRV-03, ADM-08)", "Counsel", "Before R1 GA"),
    ("D-10", "External AI model policy and data residency (AI-07)", "Owner / Data-protection owner", "Before R4"),
    ("D-11", "Licensing model: per user, per project value or per company tier", "Owner / Commercial", "Before R1 GA"),
    ("D-12", "Languages at launch and translation ownership (ADM-04)", "Owner", "Before R1 GA"),
]

RISKS = [
    ("RK-01", "Scope breadth versus tier-1 suites delays core releases", "Late revenue", "Release plan R1-R4; MoSCoW; change control; finance and P2P first"),
    ("RK-02", "Statutory rules (tax, payroll, labour) misread or changed", "Non-compliance, rework", "Data-driven, effective-dated rules; adviser sign-off (NFR-PRV-05)"),
    ("RK-03", "Signer key or API process compromise", "Forged approvals", "Isolated signer, HSM/KMS, DB re-verification, key ceremony"),
    ("RK-04", "Poor site connectivity", "Lost or late field data", "Offline-first mobile app, idempotent sync, conflict review"),
    ("RK-05", "Legacy data quality at migration", "Wrong opening balances", "Staging, validation, reconciliation sign-off (NFR-DAT-05)"),
    ("RK-06", "AI misuse or prompt injection", "Data leakage, wrong advice", "Facts-only gateway, no decision rights, red team (R7)"),
    ("RK-07", "Performance at enterprise volume", "Slow close, user rejection", "Load tests, partitioning, dedicated DB tier (NFR-MNT-07)"),
    ("RK-08", "Competitors' ecosystems and integrations", "Lost deals", "Open API, P6/MS Project/BI connectors, migration tooling"),
    ("RK-09", "Dependence on missing infrastructure (IdP, vault, staging)", "Release blocked", "Decisions D-02 and D-03 before R1 GA"),
]

MILESTONES = [
    ("R1 Financial core + P2P (GA)", "Existing baseline hardened: IAM SSO/MFA, ledger, AP/AR, treasury, P2P, inventory, contracts to cash, workflow, audit, reports, exports", "Gates R1-R5, R8, R9; pentest; IdP, vault and HSM in place; signed GO"),
    ("R2 Project controls + commercial", "Budget revisions, forecasting, WIP and IFRS 15, subcontracts, claims, bonds, payroll, fixed assets, consolidation, tax returns", "Finance and tax sign-off; payroll fixture sign-off"),
    ("R3 Field + documents + QHSE", "Mobile app, timesheets, equipment, document control/CDE, RFIs, submittals, quality, HSE, P6 integration", "Gate R6; field pilot on two live projects"),
    ("R4 Intelligence + ecosystem", "Document AI, predictive risk, portals, BI connectors, report builder, load/soak at enterprise scale", "Gate R7; performance targets met"),
]

COMPETITION = [
    ("Financial control depth", "Strong (SAP, Oracle Fusion); weaker in field-first platforms, which rely on an ERP integration", "One ledger; controls enforced in the database as well as the app"),
    ("Project controls (budget, commitment, EVM, forecast)", "Strong in specialists (InEight, Oracle Primavera/Unifier, RIB)", "Native and linked to the ledger, so no reconciliation between systems"),
    ("Field and offline", "Strong in Procore and Autodesk Construction Cloud; heavy in classic ERPs", "Offline-first with idempotent sync, designed for low connectivity"),
    ("Approvals and SoD", "Configurable; enforced in the application layer", "Cryptographic attestations re-verified by the database; humans only"),
    ("AI", "Rapidly growing assistants", "Advisory only, permission-filtered, computed numbers, auditable"),
    ("Local fit (Ethiopia / East Africa)", "Partner localisation, typically limited", "Amharic and other languages, ETB, Ethiopian calendar and fiscal year, local tax, payroll and contract forms"),
    ("Deployment", "Mostly cloud-only SaaS (field platforms) or heavy on-premises (classic ERP)", "SaaS, dedicated, in-country or air-gapped on-premises from one codebase"),
    ("Total cost of ownership", "High licence and implementation cost", "Tiered pricing; guided onboarding; templates"),
]


# ---------------------------------------------------------------------------
def parse(block):
    secs = []
    for line in block.strip().splitlines():
        line = line.strip()
        if not line:
            continue
        if line.startswith("## "):
            num, title, intro = (line[3:].split("|") + [""])[:3]
            secs.append({"num": num, "title": title, "intro": intro, "reqs": []})
        else:
            rid, p, v, b, text = line.split("|", 4)
            assert p in PRI and v in VER and b in BASE, line
            secs[-1]["reqs"].append((rid, p, v, b, text))
    ids = [r[0] for s in secs for r in s["reqs"]]
    dup = {i for i in ids if ids.count(i) > 1}
    assert not dup, dup
    return secs


FR = parse(REQS)
NF = parse(NFRS)
ALL = FR + NF
e = html.escape


def table(head, rows, widths=None, cls=""):
    cols = "".join(f'<col style="width:{w}">' for w in widths) if widths else ""
    th = "".join(f"<th>{e(h)}</th>" for h in head)
    body = "".join("<tr>" + "".join(f"<td>{c}</td>" for c in r) + "</tr>" for r in rows)
    return f'<table class="{cls}"><colgroup>{cols}</colgroup><thead><tr>{th}</tr></thead><tbody>{body}</tbody></table>'


def req_table(sec):
    rows = [(f"<b>{e(r)}</b>", e(t), PRI[p], VER[v], f'<span class="b{b}">{BASE[b]}</span>') for r, p, v, b, t in sec["reqs"]]
    return table(["ID", "Requirement", "Pri.", "Verif.", "Baseline"], rows, ["12%", "61%", "8%", "10%", "9%"], "req")


def h(level, num, title, anchor):
    label = f"{num} {title}".strip()
    return f'<h{level} id="{anchor}">{e(label)}<span class="mk">QQ{anchor}QQ</span></h{level}>'


def build(pages):
    toc_items = []
    parts = []

    def H(level, num, title):
        anchor = "s" + re.sub(r"[^a-z0-9]+", "-", f"{num} {title}".lower()).strip("-")
        toc_items.append((level, f"{num} {title}".strip(), anchor))
        return h(level, num, title, anchor)

    # ---- document control
    parts.append('<section class="page">' + H(1, "", "Document Control") + table(["Field", "Value"], [
        ("Document title", "SERK Construction OS: Software Requirements Specification (SRS)"),
        ("Version / status", "1.0 / DRAFT, issued for review and formal agreement"),
        ("Date", DATE),
        ("Product", "SERK Construction OS: enterprise construction ERP (finance, project controls, procurement, subcontracts, inventory, equipment, workforce, contracts, field, documents, QHSE, intelligence)"),
        ("Source baseline", "Repository <i>Serk-construction</i> at commit 5f94bc3 (platform 0038, 38 migrations, ADRs 0001-0017, recorded run of 378 tests: 0 failed, 0 skipped), extended to an enterprise target state"),
        ("Prepared for", "Product owner, engineering, finance, commercial, project controls, operations, HSE/QA, security, compliance and QA stakeholders"),
        ("Intended use", "Contractual and engineering baseline: what the system shall do and the qualities it shall have. How it is built is governed by the ADRs and the architecture documents."),
    ], ["24%", "76%"]))
    parts.append("<h3>Revision history</h3>" + table(["Version", "Date", "Author", "Change"], [
        ("0.1", "2026-11-06", "Drafting", "Derived from the implemented baseline and enterprise market requirements"),
        ("1.0", "2026-11-06", "Drafting", "Complete first issue for stakeholder review"),
    ], ["12%", "16%", "16%", "56%"]))
    parts.append("""<h3>How to read and use this document</h3><ul>
<li>Every requirement has a unique ID, a MoSCoW priority (<b>Must / Should / Could</b>), a verification method (<b>Test / Inspection / Demo / Analysis</b>) and a <b>Baseline</b> status.</li>
<li><b>Baseline</b> states how far the current codebase already meets the requirement: <span class="bE">Exists</span> (implemented and covered by automated tests), <span class="bP">Partial</span> (some of it exists) or <span class="bN">New</span> (to be built). It is engineering evidence, not acceptance: every requirement is still verified as written.</li>
<li><b>shall</b> = mandatory; <b>should</b> = recommended, may be deferred by agreement; <b>may</b> = optional.</li>
<li>Items in Appendix E (Open Decisions) need an owner and a decision before the related requirements can be frozen. After sign-off (Section 15), changes follow Section 14.</li></ul>
<div class="note"><b>Important notice.</b> This SRS defines requirements; it is not a certification, a legal or tax opinion, or an accounting policy. Tax, payroll, labour, data-protection and IFRS treatment must be confirmed by qualified professionals before live use. The current release decision recorded in the repository is <b>NO-GO</b> for production until the named gates and human sign-offs exist.</div></section>""")

    # ---- TOC placeholder
    parts.append('<section class="page"><h1>Table of Contents</h1><div id="toc">@@TOC@@</div></section>')

    # ---- 1 Introduction
    s = ['<section class="page">', H(1, "1.", "Introduction"), H(2, "1.1", "Purpose"),
         "<p>This document specifies the functional and non-functional requirements of SERK Construction OS, an enterprise resource planning platform for construction contractors. It covers the whole contractor lifecycle: winning work, controlling project cost and time, buying and subcontracting, running sites, paying people and suppliers, billing clients, and closing the books. It is the single reference against which design, implementation, testing and acceptance are measured, and it sets the scope needed to compete with established international construction and ERP suites.</p>",
         H(2, "1.2", "Scope"),
         "<p><b>In scope:</b> multi-company, multi-project construction operations: organisation and master data; business development, estimating and tendering; project controls (budget, commitment, cost, forecast, WIP); planning, scheduling and earned value; procurement and import; subcontract management; inventory; equipment and fleet; workforce, time and payroll; client contracts, variations, claims and revenue; finance, tax, fixed assets and consolidation; treasury; site operations and an offline mobile application; document control and BIM; quality; health, safety and environment; workflow and approvals; reporting and analytics; governed AI; audit; portals; integration and administration.</p>",
         "<p><b>Out of scope for release 1.x</b> (unless added by change control): design authoring (CAD/BIM modelling), real-estate sales and property management, manufacturing (precast plant MRP), full recruitment and talent management, e-commerce, and direct movement of money (the system issues instructions and reconciles; banks settle).</p>",
         H(2, "1.3", "Product vision and quality pillars"),
         table(["Pillar", "Meaning for requirements"], [
             ("CONTROLLED", "Every financial and commercial decision is made by an authorised human, checked by another, attested cryptographically and re-verified by the database. Controls hold even if the application is bypassed."),
             ("INTEGRATED", "One system of record from estimate to final account: budget, commitment, actual, forecast and revenue come from the same documents and the same ledger, with no reconciliation between separate products."),
             ("FIELD-READY", "Works on intermittent connectivity and on phones; field data is captured where work happens and synchronised safely."),
             ("INTELLIGENT", "Analytics and AI explain, predict and draft from permission-filtered facts with sources; they never decide."),
             ("LOCAL AND OPEN", "Built for Ethiopia and East Africa (languages, calendar, currency, tax, payroll, contract forms) and open through documented APIs, in any deployment model."),
         ], ["20%", "80%"]),
         H(2, "1.4", "Definitions, acronyms and abbreviations"),
         table(["Term", "Definition"], [
             ("ABAC / RBAC", "Attribute-based / role-based access control"),
             ("AP / AR / GL", "Accounts payable / accounts receivable / general ledger"),
             ("Attestation", "A signed statement, issued by the isolated signer for an authenticated human session, that the database verifies before accepting a decision"),
             ("BOQ", "Bill of quantities"),
             ("CDE", "Common data environment (ISO 19650)"),
             ("CPM / EVM", "Critical path method / earned value management (PV, EV, AC, SPI, CPI, EAC, ETC, VAC)"),
             ("EOT", "Extension of time"),
             ("FIFO / WAC", "First in, first out / weighted average cost"),
             ("GRN / PO / PR / RFQ", "Goods receipt note / purchase order / purchase requisition / request for quotation"),
             ("IPC", "Interim payment certificate"),
             ("ITP / NCR", "Inspection and test plan / non-conformance report"),
             ("Maker / checker", "The person who prepares a document and a different person who approves it"),
             ("MFA / step-up", "Multi-factor authentication / a fresh MFA required for a specific sensitive action"),
             ("Outbox", "Events written in the same transaction as the business change and delivered after commit"),
             ("RLS", "PostgreSQL row-level security"),
             ("RPO / RTO", "Recovery point objective / recovery time objective"),
             ("SoD", "Segregation of duties"),
             ("Tenant", "A construction company using the product, isolated from all others"),
             ("WBS", "Work breakdown structure"),
             ("WIP", "Work in progress (over/under-billing schedule)"),
         ], ["22%", "78%"]),
         H(2, "1.5", "References"),
         "<ul><li>SERK Construction repository: README, docs/architecture (MODULES, DATA_FLOW, THREAT_MODEL), ADRs 0001-0017, docs/release (READINESS, GO_NO_GO), docs/BACKLOG.md, api/openapi.yaml.</li>"
         "<li>IFRS (IAS 16, IAS 21, IFRS 15, IFRS 10/11); Ethiopian tax, labour and pension legislation as confirmed by advisers.</li>"
         "<li>Ethiopia Personal Data Protection Proclamation No. 1321/2024; GDPR where applicable.</li>"
         "<li>ISO 19650 (information management using BIM); IFC and BCF (buildingSMART); FIDIC conditions of contract; Ethiopian public-procurement standard bidding documents.</li>"
         "<li>ISO 20022 (payments); ISO 15143-3 (telematics); OWASP ASVS 4; WCAG 2.1; ISO/IEC 27001; SOC 2; ISO/IEC/IEEE 29148 (requirements engineering).</li></ul>",
         H(2, "1.6", "Document organisation"),
         "<p>Section 2 describes the product context and market position; Section 3 the business rules and invariants; Section 4 the interfaces; Section 5 the principal use cases; Section 6 the functional requirements; Section 7 the non-functional requirements; Sections 8 and 9 data, verification and release; Section 10 the client technology position; Sections 11 to 15 risks, quality assurance, change control and sign-off. The appendices give counts, traceability, the test catalogue, the role matrix and open decisions.</p>",
         "</section>"]
    parts.append("".join(s))

    # ---- 2 Overall description
    mods = [(x["title"], x["num"]) for x in FR]
    s = ['<section class="page">', H(1, "2.", "Overall Description"), H(2, "2.1", "Product perspective"),
         "<p>SERK Construction OS is a multi-tenant <b>modular monolith</b> written in Go on <b>PostgreSQL</b>, the authoritative store and last line of defence. Business commands pass through a kernel that checks permission, project scope and segregation of duties, then commit the change, its audit record and its outbox event in one transaction. An isolated <b>approval signer</b> holds the attestation key. Separate processes deliver outbox events (relay), run migrations and serve the web console. Users reach the system through a browser console, an offline-capable mobile application and external portals over HTTPS. Integrations use a versioned REST API and signed webhooks.</p>",
         H(2, "2.2", "Logical modules"),
         table(["Module", "SRS section"], mods, ["78%", "22%"]),
         H(2, "2.3", "User classes and characteristics"),
         table(["Actor", "Primary goals", "Key permissions"], ACTORS, ["30%", "25%", "45%"]),
         H(2, "2.4", "Operating environment"),
         "<ul><li><b>Server:</b> Linux containers; Go services (API, signer, relay, migrator, workers); PostgreSQL 16+ with point-in-time recovery; S3-compatible object storage; secret vault and HSM/KMS; TLS edge with WAF.</li>"
         "<li><b>Deployment models:</b> multi-tenant SaaS, dedicated single-tenant, in-country hosting, and on-premises including air-gapped.</li>"
         "<li><b>Clients:</b> current desktop browsers; tablets and phones; Android (priority) and iOS mobile app; intermittent connectivity on site must be expected.</li></ul>",
         H(2, "2.5", "Design and implementation constraints"),
         "<ul><li>The server is the authority for every amount, status and permission; the client holds none.</li>"
         "<li>Controls are enforced twice: in the kernel and by PostgreSQL (constraints, triggers, RLS, attestation checks).</li>"
         "<li>Money is exact decimal; migrations are additive; posted records are never edited or deleted.</li>"
         "<li>No AI, automation or service principal holds decision authority.</li>"
         "<li>Payments are instructions only; settlement comes from bank evidence.</li></ul>",
         H(2, "2.6", "Market position and differentiation"),
         "<p>Contractors today typically combine a finance ERP (for example SAP S/4HANA, Oracle Fusion, Microsoft Dynamics, Sage) with separate project-controls, scheduling, field and document products (for example Oracle Primavera and Aconex, Procore, Autodesk Construction Cloud, Trimble Viewpoint, InEight, RIB). SERK competes by offering that scope as one controlled system with strong local fit. The comparison below is indicative and must be refreshed by product marketing before external use.</p>",
         table(["Capability", "Typical market offering", "SERK target position"], COMPETITION, ["24%", "38%", "38%"]),
         H(2, "2.7", "Assumptions and dependencies"),
         "<ul><li>Tenants provide or adopt an identity provider for SSO, or use the built-in one with MFA.</li>"
         "<li>Qualified advisers confirm tax, payroll, labour, IFRS and data-protection treatment (Appendix E).</li>"
         "<li>Banks and mobile-money providers offer files or APIs; where they do not, statement import remains the settlement evidence.</li>"
         "<li>Customers supply legacy data for migration and accept a reconciliation sign-off.</li></ul>",
         "</section>"]
    parts.append("".join(s))

    # ---- 3 rules
    s = ['<section class="page">', H(1, "3.", "Core Business Rules and Invariants"),
         "<p>These invariants govern every requirement. Each has a release gate (Section 9); failure of any blocks production approval.</p>",
         table(["ID", "Invariant (non-negotiable)", "Primary requirements"], INVARIANTS, ["8%", "62%", "30%"]),
         H(2, "3.1", "Authoritative write path"),
         "<p>The API authenticates the principal and decodes the request. The kernel checks permission, project scope, segregation of duties and business rules, obtains an attestation from the isolated signer for human decisions, and executes one tenant transaction. PostgreSQL then re-verifies the attestation, state machine, balances, periods, budget and tenant identity, and commits the document, its journal (if any), the hash-chained audit record and the outbox event <b>together or not at all</b>. Notifications, webhooks, analytics and AI happen only after commit.</p>",
         H(2, "3.2", "Controlled document lifecycle (reference)"),
         table(["From", "To", "Trigger / guard"], [
             ("Draft", "Submitted", "Maker submits; content frozen and attested"),
             ("Submitted", "Approved / Rejected", "Checker other than maker; scope, limits, MFA if required; mandatory workflow quorum if published"),
             ("Approved", "Posted / Released / Certified", "Further distinct person where configured; period open; budget and policy re-checked"),
             ("Posted", "Reversed", "Reversal document by authorised person; original never changes"),
             ("Any open state", "Cancelled", "Authorised cancel with reason; commitments released"),
             ("Offline draft", "Recorded / Conflict / Refused", "Server revalidation on sync; conflict returned to the author"),
         ], ["20%", "24%", "56%"]),
         "</section>"]
    parts.append("".join(s))

    # ---- 4 interfaces
    s = ['<section class="page">', H(1, "4.", "External Interface Requirements"), H(2, "4.1", "User interfaces"),
         "<ul><li><b>EI-UI-01:</b> The system shall provide role workspaces (My work, approvals, dashboards) and task-specific screens for every module in Section 6.</li>"
         "<li><b>EI-UI-02:</b> Each document screen shall show status, history, attachments, related documents and the reason for any refusal.</li>"
         "<li><b>EI-UI-03:</b> The mobile application shall show clearly whether each record is synced, pending or in conflict.</li>"
         "<li><b>EI-UI-04:</b> All screens shall meet NFR-USB-01 to NFR-USB-07.</li></ul>",
         H(2, "4.2", "Hardware interfaces"),
         "<ul><li><b>EI-HW-01:</b> Phone camera and GPS for photos, geotagging and QR or barcode scanning.</li>"
         "<li><b>EI-HW-02:</b> Biometric attendance terminals through vendor adapters (HCM-03).</li>"
         "<li><b>EI-HW-03:</b> Equipment telematics through ISO 15143-3 feeds (EQP-07).</li></ul>",
         H(2, "4.3", "Software and API interfaces"),
         table(["Interface", "Examples", "Required behaviour"], [
             ("Public REST API", "/v1/... (OpenAPI 3)", "Authenticated, scoped, rate-limited; idempotency keys on commands"),
             ("Webhooks", "Outbox relay", "HMAC-signed, timestamped, ordered per aggregate, replayable"),
             ("Identity", "OIDC, SAML 2.0, SCIM 2.0", "Principal kind and MFA assertions; session revocation"),
             ("Banks", "ISO 20022 pain.001 / camt.053, MT940, CSV", "Files attested on import; settlement only from statements"),
             ("Scheduling", "Primavera P6 XER/XML, MS Project XML", "Import and export with integrity report"),
             ("BIM", "IFC, BCF", "View and issue exchange; take-off links"),
             ("BI", "Power BI and warehouse export", "Read-only; tenant and project isolation preserved"),
             ("Offline sync", "/v1/sync", "Registered device and user token; per-command result"),
             ("Assistant", "/v1/assistant/ask", "Facts-only gateway; no write tools"),
         ], ["18%", "32%", "50%"]),
         H(2, "4.4", "Communications interfaces"),
         "<ul><li><b>EI-CM-01:</b> HTTPS (TLS 1.2+) only for every external interface.</li>"
         "<li><b>EI-CM-02:</b> Email (SMTP with DKIM), SMS and messaging gateways through adapters with retry.</li>"
         "<li><b>EI-CM-03:</b> Controlled outbound egress with timeouts and circuit breakers.</li></ul>",
         "</section>"]
    parts.append("".join(s))

    # ---- 5 use cases
    parts.append('<section class="page">' + H(1, "5.", "Principal Use Cases") +
                 table(["UC", "Use case", "Actor", "Main flow (summary)", "Related"], USE_CASES, ["8%", "17%", "20%", "41%", "14%"]) + "</section>")

    # ---- 6 functional
    s = ['<section class="page">', H(1, "6.", "Functional Requirements"),
         "<p>Priority: <b>Must</b> = required for the release that contains the module; <b>Should</b> = important, may be deferred by agreement; <b>Could</b> = desirable. Verification: Test, Inspection, Demo, Analysis. Baseline: Exists, Partial, New (see Document Control).</p>"]
    for sec in FR:
        s.append(H(2, sec["num"], sec["title"]))
        if sec["intro"]:
            s.append(f'<p class="intro">{e(sec["intro"])}</p>')
        s.append(req_table(sec))
    s.append("</section>")
    parts.append("".join(s))

    # ---- 7 NFR
    s = ['<section class="page">', H(1, "7.", "Non-Functional Requirements")]
    for sec in NF:
        s.append(H(2, sec["num"], sec["title"]))
        if sec["intro"]:
            s.append(f'<p class="intro">{e(sec["intro"])}</p>')
        s.append(req_table(sec))
    s.append("</section>")
    parts.append("".join(s))

    # ---- 8 data
    parts.append('<section class="page">' + H(1, "8.", "Data Requirements") + H(2, "8.1", "Conceptual entities") +
                 "<p>Every entity carries tenant scope, creation and change metadata, and (where relevant) status and version. Key traceability relationships are mandatory.</p>" +
                 table(["Entity", "Key attributes", "Mandatory relationships"], [
                     ("TENANT / ENTITY / PROJECT", "Currency, tax registration, calendar; project client, dates, status", "Project belongs to an entity"),
                     ("USER / ROLE / SCOPE / DELEGATION", "Principal kind, roles, project scope, limits, validity", "Scopes every command"),
                     ("WBS / COST CODE (versioned)", "Code, name, level, version, approval", "Documents reference an approved version"),
                     ("BOQ / ESTIMATE / BUDGET (versioned)", "Items, quantities, rates, revisions, transfers", "Budget line traces to BOQ item"),
                     ("PR / RFQ / QUOTE / PO / GRN / INVOICE", "Lines, cost code, quantities, prices, status", "Each step references its predecessor"),
                     ("SUBCONTRACT / APPLICATION / CERTIFICATE", "SoV, retention, advance, back-charges", "Commits to budget; certificate generates payable"),
                     ("CONTRACT / MEASUREMENT / IPC / AR INVOICE / RECEIPT", "Lines, quantities, retention, VAT base", "IPC traces to measurement and contract line"),
                     ("STOCK ITEM / LAYER / MOVEMENT", "Store, quantity, unit cost, method", "Movement traces to source document; immutable"),
                     ("EQUIPMENT / USAGE / WORK ORDER", "Hours, rate, maintenance", "Usage charges a project cost code"),
                     ("EMPLOYEE / TIMESHEET / PAY RUN", "Rates, hours, statutory components", "Costs to project; journal per run"),
                     ("SCHEDULE / ACTIVITY / BASELINE / PROGRESS", "Dates, logic, float, % complete", "Progress feeds EVM"),
                     ("DOCUMENT / REVISION / TRANSMITTAL / RFI", "Number, revision, state, recipients", "One current revision"),
                     ("INSPECTION / NCR / PERMIT / INCIDENT", "Type, location, status, actions", "Linked to project and activity"),
                     ("JOURNAL / LINE / PERIOD / FX RATE", "Source event (unique), balance, currency", "Balanced; immutable once posted"),
                     ("PAYMENT / BANK STATEMENT / RECONCILIATION", "Payee, amount, release, statement line", "Settlement from statement only"),
                     ("ATTESTATION / AUDIT EVENT / OUTBOX EVENT", "Signer key, hash chain, delivery state", "Same transaction as the change"),
                 ], ["30%", "40%", "30%"]) +
                 H(2, "8.2", "Data retention, migration and quality") +
                 "<ul><li><b>DR-01:</b> Retention periods and legal holds are configurable per record type, set from confirmed legal minimums (NFR-PRV-03).</li>"
                 "<li><b>DR-02:</b> Opening balances, open documents, stock and fixed assets are loaded through validated staging and reconciled before go-live (NFR-DAT-05).</li>"
                 "<li><b>DR-03:</b> Master data, rates, tax rules and policies are effective-dated and fully audited.</li>"
                 "<li><b>DR-04:</b> Tenants can export all their data in documented open formats at any time (no lock-in).</li></ul></section>")

    # ---- 9 verification
    parts.append('<section class="page">' + H(1, "9.", "Verification and Release Gates") +
                 "<p>Each invariant is tied to a test suite and recorded evidence. Failure of any critical gate blocks production approval.</p>" +
                 table(["Invariant", "Test suite", "Evidence required"], GATES, ["22%", "50%", "28%"]) +
                 H(2, "9.1", "Acceptance criteria summary") +
                 "<ul><li>All Must requirements of the release verified with recorded evidence; no open critical or high defect.</li>"
                 "<li>All applicable gates R1-R9 passed.</li>"
                 "<li>Independent penetration test closed for critical and high findings (NFR-SEC-07).</li>"
                 "<li>Load and soak tests meet NFR-PERF targets on a production-like environment.</li>"
                 "<li>Restore test successful and documented within the last 90 days.</li>"
                 "<li>UAT sign-off by finance, commercial, project controls and site operations; pilot on live projects completed.</li>"
                 "<li>Tax, payroll, IFRS and data-protection mappings signed by qualified professionals.</li></ul>" +
                 H(2, "9.2", "Release plan") +
                 table(["Release", "Scope", "Exit criteria"], MILESTONES, ["22%", "48%", "30%"]) + "</section>")

    # ---- 10 client technology
    parts.append('<section class="page">' + H(1, "10.", "Client Technology Position") +
                 "<p>The business requirements are independent of client technology, provided every requirement in Sections 6 and 7 is met. The positions below are recorded for agreement.</p>" +
                 table(["Client", "Current baseline", "Target"], [
                     ("Web console", "Framework-free static SPA served by the API; no authority in the client; accessibility checks automated", "Keep the authority model; adopt a documented design system and component library; translation framework (ADM-04)"),
                     ("Mobile field app", "Offline sync API and device registration exist; no native app", "Offline-first app (decision D-01) with encrypted local store and durable command queue"),
                     ("Portals", "None", "Separate portal shell over the same API and authorisation rules (COL-03)"),
                     ("Exports", "PDF, Word, Excel writers with letterhead and Ethiopic fonts", "Template management (ADM-03), scheduled delivery (RPT-05)"),
                 ], ["16%", "42%", "42%"]) +
                 '<div class="note">In every case the server remains the authority for amounts, states and permissions; client code is never trusted for those decisions.</div></section>')

    # ---- 11-13
    parts.append('<section class="page">' + H(1, "11.", "Assumptions and Risks") +
                 table(["ID", "Risk", "Impact", "Mitigation"], RISKS, ["8%", "34%", "18%", "40%"]) +
                 H(1, "12.", "Quality Assurance Approach") +
                 "<ul><li>Requirements trace to design, code, tests and evidence in a matrix maintained for the life of the product (docs/release/FEATURE_EVIDENCE_MATRIX.csv).</li>"
                 "<li>CI runs unit, integration (fresh PostgreSQL), race, security-probe, migration and browser-journey tests on every change.</li>"
                 "<li>Defects are classified Critical, High, Medium or Low; no Critical or High defect may be open at release.</li>"
                 "<li>Changes to financial, approval, stock, payroll, contract or sync modules require review by two engineers, and an independent review before each release.</li>"
                 "<li>Gate status is generated from evidence files (tools/gate_status.py), never written by hand.</li></ul>" +
                 H(1, "13.", "Commercial and Support Requirements") +
                 "<ul><li><b>COM-01:</b> Support tiers with defined response times (Critical: 1 hour, 24x7 for premium).</li>"
                 "<li><b>COM-02:</b> A published roadmap, release notes, and deprecation of any API version with at least 12 months' notice.</li>"
                 "<li><b>COM-03:</b> An implementation methodology with templates (chart of accounts, workflows, roles, reports) so that a mid-size contractor can go live on finance and procurement within 12 weeks.</li>"
                 "<li><b>COM-04:</b> Training material and in-product help in every supported language.</li></ul></section>")

    parts.append('<section class="page">' + H(1, "14.", "Change Control") +
                 "<ul><li>After sign-off, every addition, deletion or modification of a requirement is raised as a written change request stating the reason, the affected IDs, and the impact on controls, compliance, schedule and cost.</li>"
                 "<li>Changes to Must requirements or to invariants R1-R9 require approval by the Product Owner, the Technical Lead and the Finance/Compliance Lead.</li>"
                 "<li>Approved changes increment the document version and are recorded in the revision history. Requirement IDs are never reused.</li></ul>" +
                 H(1, "15.", "Agreement and Sign-off") +
                 "<p>By signing, the parties agree that this SRS is the baseline for design, development and acceptance, subject to Section 14.</p>" +
                 table(["Role", "Name", "Signature", "Date"], [(r, "", "", "") for r in [
                     "Product Owner / Client", "Technical Lead / Vendor", "Finance Lead", "Commercial / Project Controls Lead",
                     "Operations (Site) Lead", "Security Lead", "Compliance / Legal Reviewer", "QA Lead"]],
                     ["34%", "24%", "26%", "16%"], "sign") + "</section>")

    # ---- Appendix A counts
    rows = []
    tot = [0, 0, 0, 0, 0, 0, 0]
    for sec in ALL:
        c = [sum(1 for r in sec["reqs"] if r[1] == k) for k in "MSC"]
        b = [sum(1 for r in sec["reqs"] if r[3] == k) for k in "EPN"]
        n = len(sec["reqs"])
        rows.append((e(f'{sec["num"]} {sec["title"]}'), *map(str, c), str(n), *map(str, b)))
        for i, v in enumerate(c + [n] + b):
            tot[i] += v
    rows.append(("<b>TOTAL</b>", *[f"<b>{v}</b>" for v in tot]))
    parts.append('<section class="page">' + H(1, "Appendix A -", "Requirement Counts by Priority and Baseline") +
                 table(["Section", "Must", "Should", "Could", "Total", "Exists", "Partial", "New"], rows,
                       ["44%", "8%", "8%", "8%", "8%", "8%", "8%", "8%"], "num") +
                 f'<p class="intro">Baseline coverage: {tot[4]} of {tot[3]} requirements already exist ({100*tot[4]//tot[3]} %), {tot[5]} are partial and {tot[6]} are new. Counts are computed from the requirement tables when this document is built.</p></section>')

    # ---- Appendix B traceability, C tests
    trace = [("ADR 0001 Modular monolith", "NFR-MNT-01, Section 2.1"),
             ("ADR 0002/0003 Postgres invariants, exact decimal money", "FIN-01..03, NFR-DAT-01..03"),
             ("ADR 0005/0006 Tokens, OIDC, DB-enforced tenant identity", "IAM-01, IAM-02, IAM-10"),
             ("ADR 0007/0008/0015 Workflow, attestations, isolated signer", "WFL-01..03, IAM-08, NFR-SEC-04"),
             ("ADR 0009/0010 Subledger journals, document visibility, masked bank data", "FIN-02, ORG-03, RPT-02"),
             ("ADR 0011 Contract to cash", "CON-01..03"),
             ("ADR 0012 Stock valuation", "INV-01..03"),
             ("ADR 0013 CPM and EVM", "SCH-01..02, EVM-01..02"),
             ("ADR 0014 Distributed abuse limits", "IAM-12"),
             ("ADR 0016 Non-superuser schema owner", "NFR-SEC-06, AUD-03"),
             ("ADR 0017 Site operations and offline sync", "SIT-01, SIT-02, SIT-04"),
             ("Platform 0038 (settings, notifications, automation, intelligence)", "ADM-03, COL-01, WFL-06, AI-01..03"),
             ("docs/BACKLOG.md gap register", "All requirements marked Partial or New")]
    parts.append('<section class="page">' + H(1, "Appendix B -", "Traceability: Baseline Architecture to SRS") +
                 table(["Baseline element", "Covered by SRS requirements"], trace, ["55%", "45%"]) +
                 H(1, "Appendix C -", "Reference Test Case Catalogue") +
                 table(["Test ID", "Expected behaviour", "Requirements", "Existing suite"], TESTS, ["11%", "47%", "20%", "22%"]) + "</section>")

    # ---- Appendix D matrix
    parts.append('<section class="page">' + H(1, "Appendix D -", "Role-Capability Matrix (initial proposal)") +
                 "<p>C = create/prepare, A = approve/certify, P = post, X = execute, R = read, - = none; * = above threshold or exception, with MFA. Project scope applies to every role except those holding tenant-wide scope. Columns group seed roles (for example, Fin./Ctrl = accountant, finance manager and controller, who are always different people for create, approve and post). To be confirmed in R1 design.</p>" +
                 table(["Capability"] + MATRIX_COLS, MATRIX, ["19%"] + ["6.2%"] * len(MATRIX_COLS), "matrix") + "</section>")

    # ---- Appendix E decisions
    parts.append('<section class="page">' + H(1, "Appendix E -", "Open Decisions for Agreement") +
                 table(["ID", "Decision required", "Suggested owner", "Needed by"], DECISIONS, ["8%", "56%", "22%", "14%"]) +
                 "<p><i>End of document.</i></p></section>")

    body = "".join(parts)
    # TOC
    toc = []
    for level, label, anchor in toc_items:
        if level > 2:
            continue
        pg = pages.get(anchor, "")
        toc.append(f'<div class="t{level}"><a href="#{anchor}">{e(label)}</a><span class="dots"></span><span class="pg">{pg}</span></div>')
    body = body.replace("@@TOC@@", "".join(toc))
    return CSS_DOC.replace("@@BODY@@", COVER + body), toc_items


COVER = f"""<section class="cover"><div class="ctitle">{TITLE}</div><div class="bar"></div>
<div class="csub">Software Requirements Specification</div>
<div class="cline">Enterprise Construction ERP | Project Controls | Procurement and Subcontracts | Finance | Field and Offline | Governed AI</div>
<div class="cline">Based on: SERK Construction repository baseline (platform 0038) and enterprise target state</div>
<div class="cfoot"><b>Document version 1.0 - DRAFT FOR AGREEMENT</b><br>Date: {DATE}<br>Structure follows ISO/IEC/IEEE 29148 (requirements engineering)<br>Deployment: multi-tenant SaaS, dedicated, in-country or on-premises<br><br>
<i>Not legal, tax, accounting or regulatory advice. Applicability must be confirmed by qualified professionals.</i></div></section>"""

CSS_DOC = """<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>SERK Construction OS - SRS v1.0</title><style>
@page { size: A4; margin: 24mm 18mm 20mm 18mm;
  @top-left { content: "SERK Construction OS - SRS"; font: bold 8.5pt Helvetica, Arial, sans-serif; color: #fff; background: #13233f; padding-left: 18mm; margin-left: -18mm; }
  @top-right { content: "v1.0 DRAFT | 2026-11-06"; font: 8pt Helvetica, Arial, sans-serif; color: #fff; background: #13233f; padding-right: 18mm; margin-right: -18mm; }
  @bottom-left { content: "Confidential - for agreement between stakeholders"; font: 8pt Helvetica, Arial, sans-serif; color: #667; border-top: .5pt solid #ccd; }
  @bottom-right { content: "Page " counter(page); font: 8pt Helvetica, Arial, sans-serif; color: #667; border-top: .5pt solid #ccd; } }
@page cover { margin: 0; }
html { -webkit-print-color-adjust: exact; print-color-adjust: exact; background: #fff; }
body { font: 9.6pt/1.42 Helvetica, Arial, "Noto Sans Ethiopic", sans-serif; color: #1b2433; margin: 0; }
.cover { page: cover; background: #13233f; color: #fff; height: 100vh; overflow: hidden; box-sizing: border-box; padding: 70mm 22mm 20mm; position: relative; page-break-after: always; }
.ctitle { font-size: 38pt; font-weight: bold; } .bar { width: 26mm; height: 2.4mm; background: #0f7c73; margin: 12mm 0 10mm; }
.csub { font-size: 19pt; margin-bottom: 6mm; } .cline { color: #c9d3e3; font-size: 10.5pt; margin-bottom: 4mm; }
.cfoot { position: absolute; bottom: 22mm; left: 22mm; right: 22mm; font-size: 9.5pt; color: #c9d3e3; } .cfoot b { color: #fff; }
.page { page-break-before: always; }
h1 { font-size: 20pt; color: #13233f; margin: 4mm 0 4mm; page-break-after: avoid; }
h2 { font-size: 13pt; color: #0f7c73; margin: 6mm 0 2mm; page-break-after: avoid; }
h3 { font-size: 11pt; color: #13233f; margin: 5mm 0 2mm; page-break-after: avoid; }
.page > h1 ~ h1 { margin-top: 9mm; }
p.intro { font-size: 8.8pt; color: #3a4558; margin: 0 0 2mm; }
table { width: 100%; border-collapse: collapse; table-layout: fixed; margin: 1.5mm 0 3mm; font-size: 8.6pt; }
thead { display: table-header-group; } tr { page-break-inside: avoid; }
th { background: #13233f; color: #fff; text-align: left; padding: 1.6mm 1.8mm; font-weight: bold; }
td { border: .5pt solid #d3dae6; padding: 1.5mm 1.8mm; vertical-align: top; }
tbody tr:nth-child(even) td { background: #f2f5fa; }
table.num td:not(:first-child), table.num th:not(:first-child), table.matrix td:not(:first-child), table.matrix th:not(:first-child) { text-align: center; }
table.matrix { font-size: 7.8pt; } table.matrix th { padding: 1.4mm .6mm; font-size: 7.2pt; }
table.sign td { height: 12mm; }
.bE, .bP, .bN { font-weight: bold; padding: 0 1.2mm; border-radius: 1mm; font-size: 8pt; }
.bE { color: #0b5d36; background: #dff3e8; } .bP { color: #7a5200; background: #fdf0d2; } .bN { color: #23408e; background: #e3e9fa; }
ul { margin: 1mm 0 3mm; padding-left: 6mm; } li { margin-bottom: 1mm; }
.note { border: .7pt solid #0f7c73; background: #eaf8f4; padding: 2.5mm 3mm; margin: 3mm 0; font-size: 8.8pt; }
.mk { font-size: 1px; color: #fff; }
#toc a { color: #1b2433; text-decoration: none; }
#toc .t1, #toc .t2 { display: flex; align-items: baseline; }
#toc .t1 { font-weight: bold; font-size: 10.5pt; margin: 2.6mm 0 1mm; }
#toc .t2 { font-size: 9pt; margin: .6mm 0 .6mm 6mm; }
#toc .dots { flex: 1; border-bottom: 1pt dotted #8892a6; margin: 0 1.5mm; transform: translateY(-1mm); }
#toc .t1 .dots { border-bottom: none; }
</style></head><body>@@BODY@@</body></html>"""


def render(html_text, pdf):
    with open(OUT_HTML, "w", encoding="utf-8") as f:
        f.write(html_text)
    subprocess.run([CHROME, "--headless=new", "--disable-gpu", "--no-pdf-header-footer",
                    f"--print-to-pdf={pdf}", "file://" + OUT_HTML],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def page_map(pdf, items):
    n = int(re.search(r"Pages:\s+(\d+)", subprocess.run(["pdfinfo", pdf], capture_output=True, text=True).stdout).group(1))
    texts = []
    for i in range(1, n + 1):
        t = subprocess.run(["pdftotext", "-f", str(i), "-l", str(i), pdf, "-"], capture_output=True, text=True).stdout
        texts.append(re.sub(r"\s+", " ", t))
    pages = {}
    for level, label, anchor in items:
        for i, t in enumerate(texts):
            if f"QQ{anchor}QQ" in t.replace(" ", ""):
                pages[anchor] = i + 1
                break
    return pages, n


def main():
    tmp = tempfile.mkdtemp()
    first = os.path.join(tmp, "pass1.pdf")
    doc, items = build({})
    render(doc, first)
    pages, _ = page_map(first, items)
    doc, items = build(pages)
    render(doc, OUT_PDF)
    pages2, n = page_map(OUT_PDF, items)
    if pages2 != pages:  # TOC length changed the layout; one more pass
        doc, items = build(pages2)
        render(doc, OUT_PDF)
    total = sum(len(s["reqs"]) for s in ALL)
    print(f"{OUT_PDF}: {n} pages, {total} requirements")


if __name__ == "__main__":
    sys.exit(main())
