// Page guides: the "How this page works" panel shown under every page title.
//
// Why this file exists: every screen in the console should explain itself to
// a first-time user without a manual: what the page is for, who uses it, how
// to use it, what the system will refuse and why, and where to go next.
//
// How it works:
//   * guideFor(key, docs, roles) returns the guide for a route key such as
//     "budget", "list/purchase_order", "doc/purchase_order" or "new/requisition".
//   * Special pages have hand-written entries in PAGES.
//   * Document lists, document pages and forms are generated: the purpose
//     sentence comes from DOC_PURPOSE, while the life cycle (statuses and
//     actions) and "who can do it" come from the console's DOCS metadata and
//     the server's role catalogue (GET /v1/meta/roles), so they cannot drift
//     from what the server actually enforces.
//
// Editing: keep sentences short and concrete; describe what the system does,
// never promise what it does not. Rules listed here must be enforced by the
// server (kernel and database), not only by the screen.

// Human-readable names for the seed roles (pkg/auth DefaultRoles).
// Word by word: acronyms stay upper case wherever they appear ("cfo" -> "CFO",
// "ap_clerk" -> "AP clerk"), and the first word is capitalised.
const ACRONYMS = { ap: 'AP', ar: 'AR', cfo: 'CFO', p2p: 'P2P' };
export function roleLabel(r) {
  return r.split('_').map((w, i) => ACRONYMS[w] || (i === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w)).join(' ');
}

// Roles that hold a permission ("a|b" means either), from the role catalogue.
export function rolesWith(perm, roles) {
  if (!perm || !roles) return [];
  const want = perm.split('|');
  return Object.keys(roles).filter((r) => roles[r].some((p) => want.includes(p))).sort().map(roleLabel);
}

// One sentence per document type: what it is in the business.
export const DOC_PURPOSE = {
  forecast_cycle: 'A cost forecast is the monthly estimate, by cost code, of what each part of the project will cost at completion. The system supplies cost to date and commitments; the project manager chooses how the remaining cost is estimated; a different person approves it with a fresh multi-factor sign-in. A line forecast above its budget must carry an explanation; an approved forecast never changes.',
  change_event: 'A change event records a change to the money or time of a project before it is agreed: a request for information that costs money, a site instruction, a client request, a design change. It carries its expected cost per cost code, its revenue impact and its schedule impact in days. One person prepares it; a different person decides it with a fresh multi-factor sign-in. Approving it drafts a budget revision for the cost lines; the budget moves only when that revision is approved by yet another person.',
  contract_notice: 'A contract notice records an event that may entitle the contractor to more time or money, and the date by which the contract requires it to be notified (commonly 28 days). The system computes that date and shows whether the notice is open, due soon, overdue, sent on time or sent late. A sent notice never changes.',
  contract_claim: 'A claim asks the client for more time, more money or both, usually following a notice. One person prepares it; a different person approves it for sending with a fresh multi-factor sign-in. If its notice is missing or was late it needs a stated reason. After approval you record the client\'s answer once, with their reference; agreed money and days cannot exceed what was claimed.',
  project_stage_gate: 'A stage gate is the controlled step from one phase of a project\'s life to the next (bid, mobilisation, execution, practical completion, defects, closed). One person prepares it with the evidence; a different person decides it with a fresh multi-factor sign-in, and only then does the project move.',
  uom_conversion: 'A unit conversion lets people count stock in another unit (a bag, a bundle, a truck). It is exact, applies only after a different person approves it, and there is one approved factor per pair of units.',
  authority_matrix: 'An authority matrix says who may approve how much: for each document type and amount band, the permission an approver must hold, and whether a fresh multi-factor sign-in is needed. One matrix is in force at a time.',
  delegation: 'A delegation lends part of your own approval authority to a colleague for a limited time, for example during leave. A third person approves it and you can revoke it at any time.',
  sod_exception: 'A duty-conflict exception lets one named person hold two permissions that are normally kept apart, for a limited time, under a written control that a different person approves.',
  budget_version: 'A budget version is the project\'s ORIGINAL baseline or a REVISION that adds to or reduces cost codes. The current budget is always original + approved revisions + transfers + drawdowns.',
  budget_transfer: 'A budget transfer moves money from one cost code to another inside the same project. The project total does not change.',
  contingency_drawdown: 'A contingency drawdown moves money out of a cost code that holds contingency into a cost code that needs it, when a risk materialises.',
  boq_version: 'A bill of quantities (BOQ) lists the priced work items of the project. An approved BOQ is the project\'s budget until an ORIGINAL budget version is approved.',
  purchase_requisition: 'A requisition is a request from site to buy something, charged to a cost code. Approval reserves the money against the budget.',
  rfq: 'A request for quotation (RFQ) is sent to several approved suppliers for an approved requisition, so the price is competitive.',
  rfq_quote: 'A quote is a supplier\'s price for an RFQ. Quotes are evaluated technically before an award.',
  purchase_order: 'A purchase order (PO) is the order placed with the awarded supplier. Approval commits the money against the budget.',
  grn: 'A goods receipt note (GRN) records what physically arrived against a purchase order. Confirmation posts the receipt to the books.',
  supplier_invoice: 'A supplier invoice is matched three ways: against the PO (price) and the confirmed receipts (quantity) before approval.',
  payment_instruction: 'A payment instruction is a record that a supplier should be paid. Nothing in this system moves money; the bank statement proves settlement.',
  supplier: 'A supplier must be approved before it can quote or be paid. Blocking stops new business with it.',
  supplier_bank_account: 'A supplier bank account is where payments go. Numbers are encrypted and masked; a second person approves every account.',
  company_bank_account: 'A company bank account is one of your own accounts that payments are made from and statements are imported for.',
  p2p_policy: 'The procure-to-pay policy sets tolerances, VAT rate, approval thresholds and control accounts for a legal entity.',
  customer: 'A customer (client) must be approved before contracts and invoices can be raised for it.',
  contract_policy: 'The contract policy sets retention, advance and VAT rules used when certificates and invoices are computed.',
  contract: 'A client contract lists the contract lines and rates the client will pay against.',
  measurement: 'A measurement records quantities of work done against contract lines for a period. A different person verifies it.',
  ipc: 'An interim payment certificate (IPC) turns verified measurements into the amount due, less retention and advance recovery.',
  customer_invoice: 'A customer invoice bills the client for a certified IPC. Approval posts the receivable.',
  customer_receipt: 'A customer receipt applies money received from the bank statement to open invoices.',
  contract_variation: 'A variation changes the contract scope or value. It must be approved before it can be measured.',
  customer_credit_note: 'A credit note reduces what the client owes, with a reason and approval.',
  retention_release: 'A retention release pays back retention held on certificates, when the contract allows it.',
  stock_item: 'A stock item is a material kept in store (for example cement or rebar), with its unit and valuation account.',
  warehouse: 'A warehouse is a central store or site store where stock is kept.',
  stock_receipt: 'A put-away records received goods into a store at their cost (FIFO or weighted average).',
  stock_issue: 'A stock issue takes material out of a store and charges it to a project cost code.',
  stock_count: 'A stock count compares counted quantities with the books. Differences become an adjustment journal after approval.',
  stock_transfer: 'A stock transfer moves material between stores at its valuation.',
  schedule_version: 'A schedule version is the project programme: activities, logic and durations. The critical path is computed by the server.',
  schedule_progress: 'A progress report records percent complete per activity at a status date. Approved progress feeds earned value.',
  site_diary: 'A site diary records the day on site: weather, labour, equipment and events. It can be written offline on a registered device.',
  money_in: 'Money received lists bank statement lines of incoming money that are not yet applied as a customer receipt.',
};

// Forms (#/new/<kind>) and the document they create.
export const FORM_DOC = {
  budget_revision: 'budget_version', budget_transfer: 'budget_transfer', contingency_drawdown: 'contingency_drawdown',
  requisition: 'purchase_requisition', boq: 'boq_version', supplier: 'supplier', companybank: 'company_bank_account',
  customer: 'customer', contractpolicy: 'contract_policy', contract: 'contract', schedule: 'schedule_version',
  diary: 'site_diary', progress: 'schedule_progress', stockitem: 'stock_item', warehouse: 'warehouse',
  stocktransfer: 'stock_transfer', stockissue: 'stock_issue', stockcount: 'stock_count', policy: 'p2p_policy',
  rfq: 'rfq', quote: 'rfq_quote', po: 'purchase_order', grn: 'grn', invoice: 'supplier_invoice', payment: 'payment_instruction',
  measurement: 'measurement', ipc: 'ipc', receipt: 'customer_receipt', variation: 'contract_variation',
  creditnote: 'customer_credit_note', project: 'project_stage_gate', projectedit: 'project_stage_gate', forecast: 'forecast_cycle', change_event: 'change_event', notice: 'contract_notice', claim: 'contract_claim', stagegate: 'project_stage_gate', putaway: 'stock_receipt', retention: 'retention_release',
  supplierbank: 'supplier_bank_account', uom_conversion: 'uom_conversion', authority_matrix: 'authority_matrix', delegation: 'delegation', sod_exception: 'sod_exception',
};

// Hand-written guides for pages that are not a document list, page or form.
export const PAGES = {
  home: {
    what: 'Your work queue: every document waiting for an action you are allowed to take, counted per type.',
    steps: ['Open a queue to see the documents in it.', 'Act on each one from its page (approve, reject, confirm ...).'],
    rules: ['Only documents of projects you are assigned to are counted.', 'You never see your own documents as waiting for your approval: the preparer cannot approve.'],
  },
  ask: {
    what: 'Ask a question about the business (for example "which projects are over budget?") and get an answer computed from the records you are allowed to see.',
    steps: ['Type a question in plain words.', 'Read the answer, then follow its sources to the underlying reports.'],
    rules: ['Every number comes from the system\'s own calculations, never from guesswork.', 'The assistant can only read. It cannot approve, post or pay anything.'],
  },
  risk: {
    what: 'Risk signals across your projects: cost and schedule performance, budget consumption, overdue orders, cash and control exceptions.',
    steps: ['Scan the signals by severity.', 'Open the linked report or document to investigate.'],
    rules: ['Signals are rule-based and show the rule that raised them.'],
  },
  reports: {
    what: 'All reports in one place. Each can be exported as PDF, Word or Excel with the company letterhead.',
    steps: ['Choose a report.', 'Set its filters.', 'Export if you need to share it.'],
    rules: ['Exports contain no more than you can see on screen.', 'Every export is recorded and counted against a daily limit.'],
  },
  budget: {
    what: 'The project budget by cost code, and how it was built: original + approved revisions + transfers in - out + contingency drawdowns in - out = current budget.',
    steps: ['Choose the project.', 'Read each cost code\'s current budget and its parts.', 'Use "New revision" to change scope, "Request transfer" to move money between codes, or draw down contingency.'],
    rules: ['Changes take effect only when a different person approves them.', 'A transfer or drawdown cannot take a code below what is already committed, reserved or spent on it.', 'Requisitions and purchase orders are checked against the current budget.'],
    related: [['#/cost', 'Cost report'], ['#/list/budget_version', 'Budget versions'], ['#/list/budget_transfer', 'Budget transfers']],
  },
  cost: {
    what: 'Cost control per cost code: original and current budget, committed (approved orders), reserved (approved requisitions), received, invoiced, actual and available.',
    steps: ['Choose the project.', 'Look at "Available": the budget not yet committed, reserved or spent.'],
    rules: ['Available = current budget - committed - reserved - other actual cost.', 'Figures come from approved and posted documents only.'],
    related: [['#/budget', 'Project budget'], ['#/list/purchase_order', 'Purchase orders']],
  },
  evm: {
    what: 'Earned value for a project: planned value, earned value and actual cost, and the indices (CPI, SPI) and forecasts (EAC, ETC, VAC) derived from them.',
    steps: ['Choose the project and status date.', 'A CPI or SPI below 1.00 means over cost or behind schedule.'],
    rules: ['Only approved schedules and approved progress are used.'],
  },
  stock: {
    what: 'Stock on hand per item and store, with quantity and value.',
    steps: ['Filter by store or item.', 'Use the buttons to issue stock or record a count.'],
    rules: ['Stock cannot go negative.', 'Values follow the stock valuation policy (FIFO or weighted average).'],
  },
  bank: {
    what: 'Bank statements: import a statement file, then reconcile its lines to released payments and receipts.',
    steps: ['Import the statement file exactly as received from the bank.', 'Reconcile each line to the matching payment or receipt.'],
    rules: ['The person who imports a statement is independent of the payments it settles.', 'A line reconciles only if amount, account and reference match.'],
  },
  journals: {
    what: 'General ledger journals: draft, approved, posted and reversed.',
    steps: ['Open a journal to see its lines.', 'Approve or post batches from here if you hold that duty.'],
    rules: ['A journal posts only if debits equal credits and its period is open.', 'Preparer, approver and poster are three different people.', 'Posted journals never change; corrections are reversals.'],
  },
  recon: {
    what: 'Subledger to general ledger reconciliation: each subledger total compared with its control account.',
    steps: ['Choose the legal entity.', 'Any non-zero difference is a break to investigate.'],
    rules: ['Only posted journals are counted.'],
  },
  tb: {
    what: 'The trial balance: posted journal lines summed by account up to a date.',
    steps: ['Choose the legal entity and the date.'],
    rules: ['Total debits equal total credits, always. The page says so explicitly.'],
  },
  aging: {
    what: 'Aging of open payables or receivables by how long they have been due.',
    steps: ['Read the buckets left to right: current, then increasingly overdue.'],
    rules: ['Only approved invoices that are not fully paid appear.'],
  },
  controls: {
    what: 'Control review: exceptions an auditor or controller should look at, such as overrides, late approvals and revealed bank details.',
    steps: ['Review each exception and open the document behind it.'],
    rules: ['This page reads only; it changes nothing.'],
  },
  automation: {
    what: 'Automation rules: notifications sent when a condition is met (for example "stock below minimum").',
    steps: ['A process owner writes a rule.', 'A workflow administrator activates it.'],
    rules: ['Rules can only notify or prepare drafts. They never approve, post or pay.', 'The author of a rule cannot activate it.'],
  },
  settings: {
    what: 'Company settings: name, letterhead on exports, language, time zone and the daily export limit.',
    steps: ['Change a field and save.'],
    rules: ['Only a security administrator can change settings.', 'Every change is versioned and recorded.'],
  },
  'new/designate': {
    what: 'Names the stock item that an inventory line of an approved purchase order delivers, so the storekeeper can put it away into stock.',
    steps: ['Choose the order line and the stock item.', 'Designate. It is set once, before anything is put away.'],
    rules: ['Only an approved stock item can be designated.', 'After designation the storekeeper can put away only that item for the line.'],
  },
  'new/reservation': {
    what: 'Sets material aside for a project so nobody else can issue it.',
    steps: ['Choose the store, item, project and quantity (in another unit if a conversion exists).', 'Say what it is for and until when.'],
    rules: ['You cannot reserve more than is on hand and not already reserved.', 'It lapses on its end date; you can release it earlier with a reason.'],
  },
  'new/reorderrule': {
    what: 'Sets the minimum, maximum and reorder quantity of an item in a store, so the Low stock report can suggest what to order.',
    steps: ['Choose the store and item.', 'Enter the minimum (below this is low), the maximum (the level to reach) and the smallest sensible order.'],
    rules: ['It only reports and suggests: nothing is ordered automatically.', 'Available stock means on hand minus reserved.'],
  },
  setup: {
    what: 'The things a new company needs in place before it can run a project, in a sensible order. Each is ticked automatically when its records exist.',
    steps: ['Work down the groups: company, finance policies, structure, a first project, then suppliers, customers and stock.', 'Open a step to do it; each says who does it and why.', 'Use Data imports to load many suppliers, customers or stock items at once.'],
    rules: ['Nothing is ticked by hand: the page reads the real records.', 'Optional steps are marked; the progress bar counts only required ones.', 'Setup follows the same approvals as daily work: a policy, a bank account or a supplier still needs a second person.'],
    related: [['#/imports', 'Data imports'], ['#/projects', 'Projects'], ['#/org', 'Organisation']],
  },
  imports: {
    what: 'Load suppliers, customers or stock items from a spreadsheet saved as CSV, safely: the file is checked first, a different person commits it, and the records still need their normal approval.',
    steps: ['Choose what to import and fill in the template or choose your file.', 'Stage it: every row is checked and nothing is created.', 'Review the errors, fix the source and stage again if needed.', 'A different person counts the rows in the original source and commits.'],
    rules: ['Rows with errors are never created.', 'The person who staged a file cannot commit it, and the confirmed row count must equal the staged file.', 'Each record is created through the ordinary command, so a supplier or stock item still needs approval by someone else.', 'A partly failed import can be committed again; records that already exist are never duplicated.'],
  },
  projects: {
    what: 'Every project you may see with its master data: client, region, business unit, contract type, manager, dates and phase.',
    steps: ['Create a project (it starts PLANNED, in phase BID).', 'Edit its details as the work becomes clear.', 'Request the next stage gate when its evidence is ready.'],
    rules: ['The phase moves only through a stage gate approved by a different person with a fresh multi-factor sign-in.', 'A project in the bid phase takes no part in costs; it becomes active when it mobilises.', 'The code and company never change once a project exists.'],
    related: [['#/list/project_stage_gate', 'Stage gates'], ['#/org', 'Regions and business units']],
  },
  org: {
    what: 'The company structure used for roll-ups (regions and business units) and the company calendar of holidays and exceptional working days.',
    steps: ['Add regions and business units, then assign them to projects.', 'Enter the holidays of the year ahead, including movable ones.'],
    rules: ['Only the people who manage the organisation can change it; everyone who reads projects can see it.', 'Nothing is assumed about movable holidays: enter them each year.'],
  },
  reservations: {
    what: 'Stock set aside for projects. Reserved stock cannot be issued to anyone else, so material promised to one pour is not used by another.',
    steps: ['Reserve stock for a project and a date.', 'Issue against the reservation when the work starts: approving that issue uses the reservation up.', 'Release a reservation you no longer need, with a reason.'],
    rules: ['You cannot reserve more than is on hand and not already reserved, even if two people try at the same moment.', 'An issue can never use stock reserved for others, and cannot exceed its own reservation.', 'A reservation lapses on its end date and is never edited, only released or used.'],
    related: [['#/stock', 'Stock on hand'], ['#/report/low-stock', 'Low stock'], ['#/new/reorderrule', 'Reorder rule']],
  },
  report: {
    what: 'A catalogue report: a read-only listing built from approved and posted records, with the filters it takes, its totals, and the time its data is as of.',
    steps: ['Choose the legal entity and, where offered, the project, the supplier or customer, and the dates.', 'Read the rows and the Total line.', 'Export to PDF, Word or Excel if you need to share it.'],
    rules: ['A report shows only the projects and records you may see; company-wide reports need company-wide scope.', 'Totals are computed by the server in exact decimals.', 'Every export carries the company letterhead and your name and the time, is counted against your daily limit, and contains no more than the screen.'],
    related: [['#/reports', 'All reports']],
  },
  dashboard: {
    what: 'The numbers that tell you whether the company and its projects are under control: budget consumed, over-budget codes, risks, cash, overdue payables and receivables, ledger balance and control findings.',
    steps: ['Pick the view that fits your job (Executive, Project, Finance, Procurement).', 'Read the level on each tile; "Needs action" means a documented threshold was crossed.', 'Click a contributor or "See the rows behind this number" to reach the records, and "How is this calculated?" for the exact formula.'],
    rules: ['Every figure is computed now by the server from approved and posted records, within your permissions and project scope: nothing is typed in or cached.', 'A view only chooses which tiles to show; tiles your role may not read are listed as not shown, with what they need.', 'The dashboard reads only; it changes nothing.'],
    related: [['#/cost', 'Cost report'], ['#/risk', 'Risks'], ['#/controls', 'Control review'], ['#/tb', 'Trial balance']],
  },
  authority: {
    what: 'Who may approve how much. The authority matrix sets a permission and an amount band per document type; delegations show authority lent between colleagues; duty conflicts list where permissions that should be kept apart are held together.',
    steps: ['Read the matrix in force: an approval above a band needs the permission shown there.', 'Use "Delegate my authority" before leave so work is not blocked.', 'A process owner proposes a new matrix version; the CFO approves it.'],
    rules: ['An approval above a band is refused unless the approver holds the permission the band needs (checked by the server; budget approvals also by the database).', 'A delegate acts only within what you hold yourself, never on documents you prepared, and never for permissions that would give them a conflict of duties.', 'Delegations end automatically and can be revoked at any time.'],
    related: [['#/list/authority_matrix', 'Matrix versions'], ['#/list/delegation', 'Delegations'], ['#/list/sod_exception', 'Duty-conflict exceptions']],
  },
  people: {
    what: 'The company directory. Everyone who has signed in appears here with the roles their sign-in gave them. Once an administrator manages a person\'s access here, the roles and projects on this page are the ones in force on every request, whatever the sign-in says.',
    steps: ['Find a person with the search box or the filters; click a card to open their sheet.', 'To change access: give a reason, then "Manage access here", give or remove roles, tick their projects, or disable them.',
      'The Duty map shows who prepares, approves, releases and governs; highlight a role to see who holds it.', 'Roles: open a product role to see what it gives; compose a company role from the fixed permission list.',
      'Access history lists every change with who made it and why.'],
    rules: ['Nobody changes their own access; the database refuses it too.', 'A role is refused if it would give the person both halves of a control (for example raising and approving purchase orders), unless an approved duty-conflict exception covers that pair.',
      'A company role can never hold both halves of a control, and cannot be changed so that a holder would.', 'Changes need a fresh multi-factor sign-in and a reason. Removed roles stay in the history.',
      'Disabling a person refuses their next request and signs them out on every device.'],
    related: [['#/authority', 'Authority and duties'], ['#/list/sod_exception', 'Duty-conflict exceptions'], ['#/list/delegation', 'Delegations'], ['#/me', 'My profile']],
  },
  cash: {
    what: 'The next 13 weeks of cash: confirmed flows (approved invoices, released payments), expected ones (certificates not yet invoiced or paid, open purchase orders) and treasury\'s planned items.',
    steps: ['Read the tiles: cash now, in 13 weeks, and the lowest point.', 'Open any item to see the document behind it.', 'Treasury adds loans, tax, payroll and other planned flows; cancel one with a reason when it changes.'],
    rules: ['Cash now is the posted balance of the company bank accounts.', 'Overdue items are shown in the first week.', 'Only tenant-wide finance and treasury see the forecast.'],
    related: [['#/aging/ar', 'Receivables aging'], ['#/aging/ap', 'Payables aging'], ['#/bank', 'Bank statements']],
  },
  periodend: {
    what: 'Period-end work: the fiscal calendar (Ethiopian or Gregorian), monthly recurring journals, accruals that reverse themselves, and overhead spread over projects.',
    steps: ['Fiscal calendar: a controller generates the next fiscal year of monthly periods.', 'Recurring journals: define once; each month press "Create the journals due today".',
      'Accruals: record at month end with the reversal date; once posted, "Create the reversals due today" makes the reversal.', 'Overhead: choose the pool and the range, preview the shares, allocate.'],
    rules: ['Every journal made here is a draft that finance approves and a different person posts.', 'A recurring journal makes one journal per date, never twice; an accrual is reversed once, on its date.',
      'Overhead shares are computed by the database from posted figures; the same pool and range are allocated once.'],
    related: [['#/journals', 'Journals'], ['#/tb', 'Trial balance']],
  },
  assets: {
    what: 'The fixed asset register: cost, depreciation and net book value of every asset, the monthly depreciation run and disposals.',
    steps: ['Classes: set each kind of asset with its accounts, method and useful life.', 'Register: record an asset; the CFO activates it, which capitalises its cost.',
      'Depreciation: pick the month, read the charges, prepare the run; finance approves and posts its journal.', 'Open an asset to propose its disposal; the CFO approves it.'],
    rules: ['The database computes every charge, never below the residual value, once per asset and month, months in order.', 'Net book value and gain or loss on disposal are computed at approval.',
      'Every asset journal is checked against its document when approved and posted, and is never edited or reversed.'],
    related: [['#/journals', 'Journals'], ['#/list/fixed_asset', 'Fixed assets (list)'], ['#/list/asset_disposal', 'Disposals']],
  },
  quality: {
    what: 'Inspection and test plans with hold points, inspections, non-conformance reports and material tests.',
    steps: ['Plans: the quality engineer prepares a plan per activity with its hold, witness and review items; a project manager approves.', 'Inspections: the site requests an inspection of an item; the quality engineer passes or fails it.',
      'Non-conformances: anyone raises one; the quality engineer records root cause and action; a project manager verifies and closes.', 'Tests: record a result against its specification; a failure raises a non-conformance by itself.'],
    rules: ['Progress above a hold point cannot be reported until its inspection has passed.', 'The requester cannot decide their own inspection; whoever raised or actioned a non-conformance cannot close it.'],
    related: [['#/list/schedule_progress', 'Schedule progress'], ['#/siterecords', 'Site records']],
  },
  siterecords: {
    what: 'Punch lists, instructions issued to the site and meeting minutes with their actions.',
    steps: ['Punch list: raise an item with place and due date; mark it fixed; someone else closes it or sends it back.', 'Instructions: a project manager issues an instruction (optionally linked to a change event); the site acknowledges it.',
      'Meetings: record the minutes and actions; issued minutes never change; mark actions done as they are completed.'],
    rules: ['Whoever fixes an item cannot close it; whoever issues an instruction cannot acknowledge it.'],
    related: [['#/list/change_event', 'Change events'], ['#/hse', 'Health and safety']],
  },
  timesheets: {
    what: 'Hours worked per person and day on each project, approved by a project manager and used by payroll.',
    steps: ['Record a day: choose the project and day, enter each person’s normal and overtime hours, then Save and submit.', 'A project manager opens the timesheet from Timesheets (or My work) and approves or rejects.',
      'The payroll officer imports approved hours into a draft pay run.'],
    rules: ['The recorder cannot approve; a person has at most 16 approved hours a day across projects and one approved line per project and day.', 'If the induction gate is on, nobody without a valid induction on the project can be submitted.'],
    related: [['#/hse', 'Health and safety'], ['#/payroll', 'Payroll']],
  },
  equipment: {
    what: 'The equipment register with daily usage, work orders and the monthly hire charge per project.',
    steps: ['Register: the plant manager adds each machine with its hire rate, fuel norm and service interval.', 'Usage: the site records hours working, idle and broken down and the fuel used; a project manager approves.',
      'Hire charge: the accountant charges approved usage to each project; finance approves the draft journals.', 'Work orders: open and complete services and repairs.'],
    rules: ['The charge is computed when usage is approved: working hours × rate (or a day’s rate); idle and breakdown time is free.', 'Usage is charged once; one record per equipment, project and day; at most 24 hours a day across projects.',
      'A fuel anomaly is flagged when litres per working hour over 30 days exceed the norm by more than 20%.'],
    related: [['#/journals', 'Journals'], ['#/assets', 'Fixed assets']],
  },
  hse: {
    what: 'Incidents, corrective actions, permits to work, inductions, toolbox talks and inspections, with the lost-time injury frequency rate.',
    steps: ['Report: anyone on site reports an incident; a major or critical one notifies HSE officers and project managers at once.', 'Investigate: the HSE officer records the root cause, raises corrective actions and has a different person verify each.',
      'Close: only after every action is verified, by someone other than the reporter.', 'Permits: the requester, the HSE officer who issues and the person who accepts are three different people.'],
    rules: ['A fatality is always critical; a lost-time injury has at least one lost day.', 'A permit lasts at most 31 days.', 'LTIFR uses approved timesheet hours of the last 12 months.'],
    related: [['#/timesheets', 'Timesheets'], ['#/list/site_diary', 'Site diaries']],
  },
  tax: {
    what: 'VAT and withholding returns computed from posted journals, and e-invoice submissions.',
    steps: ['Tax accounts: designate each entity’s output VAT, input VAT and withholding payable accounts.', 'Returns: choose the entity, return type and period; the lines appear at once.',
      'The CFO approves with fresh MFA; the preparer then records the filing with the authority’s reference.', 'E-invoices: send approved customer invoices; the result and its reference are recorded.'],
    rules: ['Lines are computed by the database from posted journals; a different line is refused.', 'The preparer cannot approve; an approved or filed return never changes; one live return per entity, type and period.',
      'Only an approved invoice is sent, and only once accepted.'],
    related: [['#/journals', 'Journals'], ['#/tb', 'Trial balance']],
  },
  consolidation: {
    what: 'Group accounts across legal entities: translation to the group currency, minority interests, associates and intercompany eliminations.',
    steps: ['Intercompany accounts: designate each due-from, due-to, income and expense account with its counterparty.', 'Groups: a controller proposes the members, ownership and method; the CFO approves.',
      'Runs: choose the group and period, enter the closing and average rates with their source, prepare; the CFO approves with fresh MFA.'],
    rules: ['The database computes every line; nothing is posted to any entity’s ledger.', 'Balance sheet at the closing rate, income and expense at the average rate; the difference is the translation reserve.',
      'What does not match between two entities stays visible as an intercompany difference.', 'An approved run never changes.'],
    related: [['#/tb', 'Trial balance'], ['#/list/consolidation_group', 'Groups (list)']],
  },
  payroll: {
    what: 'Employees, the income tax and pension rules in force, and the monthly pay run with its journals and bank file.',
    steps: ['Accounts: a controller proposes the five payroll accounts; the CFO approves them.', 'Rules: the payroll officer prepares the tax bands and pension rates with their source; the HR manager approves.',
      'Employees: add each person with pay type, base rate and bank account.', 'Pay run: open the month, enter units, overtime and allowances per person and the project charged, calculate, submit.',
      'The HR manager approves with fresh MFA (one journal per project goes to finance); treasury releases the payment and downloads the bank file.'],
    rules: ['Preparer, approver and payer are three different people.', 'The database computes every line; a line that differs from its computation is refused.',
      'A rule set never takes effect inside a period already approved; a leaver is not paid after the leaving date.', 'Account numbers are encrypted and shown masked; the bank file is the only place they leave the system, and its download is audited.'],
    related: [['#/journals', 'Journals'], ['#/list/company_bank_account', 'Company banks']],
  },
  subpolicy: {
    what: 'The accounts every subcontract certificate posts to, per legal entity.',
    steps: ['A controller proposes the five accounts; the CFO approves the policy from its page (Subcontract policies to approve on My work).'],
    rules: ['Payable, retention and withholding are liability accounts, advances an asset, back-charges an expense or revenue account; all five differ.'],
    related: [['#/list/subcontract', 'Subcontracts']],
  },
  revenue: {
    what: 'Revenue over time (IFRS 15): what each contract has earned against what was billed, and the month-end journal that books the difference.',
    steps: ['A controller proposes the revenue policy for each legal entity (progress measure, contract asset and liability accounts); the CFO approves it.',
      'A project accountant picks the project and month end, reads the preview, and prepares the run; it creates a draft journal.',
      'Finance approves the journal and a different person posts it (Accounting → Journals). Finance may cancel a run whose journal is still a draft.'],
    rules: ['Earned = contract value × progress; progress is cost to date ÷ the latest approved forecast at completion, or verified measured work ÷ contract value.',
      'The journal moves only what changed since the last posted run, is checked against the run when approved and posted, and is never edited or reversed.',
      'One run waits at a time per project; a later month end blocks an earlier one.'],
    related: [['#/journals', 'Journals'], ['#/list/forecast_cycle', 'Cost forecasts'], ['#/list/customer_invoice', 'Customer invoices']],
  },
  obligations: {
    what: 'Every obligation the contracts set, on either side: what must be done, under which clause, by whom and by when.',
    steps: ['Add an obligation from a contract’s clause, naming its owner here.', 'Filter for what is due within 7 days or overdue.', 'Close it once it is met (say how) or waived (say why).',
      'A process owner can add alert rules (Obligation due, Notice due, Guarantee expiring) that warn at 14, 7 and 2 days and when late.'],
    rules: ['An obligation is closed once and never reopened; it cannot be closed with a future date.', 'A waiver needs its reason.'],
    related: [['#/list/contract_notice', 'Contract notices'], ['#/list/contract_claim', 'Claims'], ['#/automation', 'Alert rules']],
  },
  bonds: {
    what: 'The bank guarantees held against each contract (bid, advance payment, performance, retention), and the price adjustment the contract allows when published price indices move.',
    steps: ['Guarantees: treasury registers each guarantee as the bank issued it; the cards show how much of its term has run and what expires within 30 days.',
      'Check "Advance payment cover": the advance not yet recovered through certified payment certificates should stay covered by an advance guarantee in force.',
      'Price indices: a quantity surveyor proposes each month’s published value; a commercial manager approves it.',
      'Price adjustment: set the contract’s formula once, then for each certified payment certificate choose the month of the indices, compute, and submit the adjustment for approval.'],
    rules: ['A guarantee’s value can only go down and its expiry only later, each with a reason. Releasing or recording a call needs another person than the one who registered it, with a recent second-factor sign-in.',
      'An expired guarantee cannot be called. A released or called guarantee never changes.',
      'An index value is approved by someone other than its proposer, is never for a future month, and once approved never changes.',
      'The formula’s fixed portion and weights add up to exactly 1; the factor and the adjustment are computed by the database from approved values only and computed again at approval.'],
    related: [['#/list/contract', 'Contracts'], ['#/list/ipc', 'Payment certificates'], ['#/list/price_adjustment', 'Price adjustments'], ['#/list/guarantee', 'Guarantees (list)']],
  },
  me: {
    what: 'Your profile: who you are in this company, what you may do and why, where, and what you did last.',
    steps: ['Keep your name, job title and phone current; colleagues see them in the directory.', '"What you may do" groups your permissions by business area; the colour shows the stage of work.',
      'Use "Sign out everywhere" if you lost a device or suspect someone used your account.'],
    rules: ['Your roles and projects are set by your administrator or your sign-in; you cannot change them here.', 'Approvals, payments and access changes need a multi-factor sign-in made within the last 10 minutes.'],
    related: [['#/home', 'My work'], ['#/authority', 'Authority and duties']],
  },
  site: {
    what: 'Field devices registered for offline work on site.',
    steps: ['Register your device to sync site diaries written offline.', 'Revoke a lost device immediately.'],
    rules: ['A revoked device can never sync again, even work it queued earlier.'],
  },
};

// Builds the guide for a route key. docs is the console's DOCS metadata;
// roles is the role catalogue ({role: [permissions]}) or null while loading.
export function guideFor(key, docs, roles, actionLabels) {
  if (PAGES[key]) return PAGES[key];
  const [kind, type] = key.split('/');
  const docType = kind === 'new' ? FORM_DOC[type] : type;
  const meta = docs[docType];
  if (!meta && !DOC_PURPOSE[docType]) return null;
  const purpose = DOC_PURPOSE[docType] || '';
  const who = [];
  if (meta && meta.create) who.push(['Prepares', rolesWith(meta.create, roles)]);
  const life = [];
  const deciders = new Set();
  if (meta && meta.actions) {
    for (const [status, acts] of Object.entries(meta.actions)) {
      const names = acts.map(([a, perm]) => {
        rolesWith(perm, roles).forEach((r) => { if (a !== 'submit' && a !== 'cancel') deciders.add(r); });
        return (actionLabels && actionLabels[a]) || a;
      });
      life.push(`${status}: ${names.join(', ')}`);
    }
  }
  if (deciders.size) who.push(['Decides', [...deciders].sort()]);
  const rules = ['The person who prepares a document cannot approve it.', 'Every decision is recorded with who made it and when, and cannot be undone silently.'];
  if (kind === 'list') {
    return { what: purpose, steps: ['Filter by status to find what needs action.', 'Open a reference to see the document and act on it.'], life, who, rules };
  }
  if (kind === 'doc') {
    return { what: purpose, steps: ['Check the details and the history below.', 'The buttons show only the actions you are allowed to take now.'], life, who, rules };
  }
  return { what: purpose, steps: ['Fill in the fields. Amounts and totals are computed by the server.', 'Save: the document is created as a DRAFT that only you can submit.', 'Submit it for approval by a different person.'], life, who, rules };
}
