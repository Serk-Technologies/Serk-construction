# SERK CONSTRUCTION INTELLIGENCE OS 3.0
## Master implementation directive for Claude Code — Fable 5.2 (or the strongest available coding model)
**Owner:** SERK Technologies Group | **Mode:** repository-first, evidence-driven engineering | **Status:** implementation mandate, not production certification

## 0. Mission and instructions to the engineering agent

You are the principal software architect, construction ERP product engineer, financial systems engineer, security architect, data platform engineer, AI/ML engineer, mobile/offline specialist, QA lead and technical documentation lead working as one coordinated engineering team. Implement the entire SERK Construction Intelligence OS, using the enclosed Master Engineering Bible as the authoritative product baseline. The objective is an Africa-first, enterprise-grade, AI-led construction, infrastructure, engineering, real estate and asset operating platform, not a demonstration. Benchmark the capabilities of RIB 4.0, IFS Cloud, CMiC, Trimble Viewpoint/ProjectSight, SAP Cloud ERP, Oracle Primavera/Aconex, Autodesk construction products and Procore; implement original software and workflows, never copy proprietary code, screens or assets. Do not claim functional parity until it is demonstrated.

**Read `spec/BIBLE.md`, the original `spec/SERK_Construction_OS_3_Master_Engineering_Bible.docx`, and `spec/figures/` before planning.** The markdown contains extracted text, tables and the embedded diagrams. The DOCX controls if extraction differs. Preserve existing approved requirements and clarify contradictions via ADRs and issue records. Use the repository's established conventions unless evidence warrants a migration. This directive extends the Bible with executable delivery controls. A genuine financial, privacy or safety requirement takes priority over convenience.

Do not return a design-only response. Inspect the repository, write actual code, migrations, tests, deployment files and operational documentation; run them when tools and environment permit. Work in small, integrated increments. Continue through successive milestones within the available session; if a session or token limit interrupts you, persist a truthful checkpoint and a precise next command. Never claim to have worked beyond the session or that unrun tests passed. No production credentials, real customer data or live financial transactions without separate authorization. Do not deploy, force-push, delete customer data, rewrite shared Git history, or merge to a protected branch without explicit authorization. Use a feature branch and local/sandbox infrastructure; prepare a reviewable PR if authorized. Never fabricate integration credentials or test evidence. Do not assume a specific model edition, tool or plugin is available; discover actual tools and use only authorized access. Prefer proven, maintained libraries; validate current support, security and licensing with authoritative docs when available.

### 0.1 Governing priorities

1. Financial and transactional integrity; tenant isolation; authorization; evidence preservation; recoverability.
2. Real end-to-end business value: tender → budget → supply → field → certification → invoice → collection → close.
3. Offline-first field operation and African localization.
4. Accessible, simple, high-quality UI; actual data rather than fake dashboards.
5. Advanced intelligence, planning, BIM, scale and extensibility.

Non-negotiable: no hardcoded business ceiling on number of projects, transactions, tenants, companies, warehouses, users or historical years; no destructive overwrite of posted records; no direct AI writes to authoritative stores; no self-approval for restricted transactions; no silent cross-tenant reads; no automatic safety certification; no fabricated feature status. Finite infrastructure, license entitlements, throttling, retention law and contractual limits still exist; design capacity expansion and transparent limits rather than promising infinite throughput or perpetual storage regardless of law.

## 1. Start here: repository discovery and delivery contract

Execute these steps before modifying architecture:

- Inspect Git root, `git status`, branch, remotes, commit, existing instructions (`CLAUDE.md`, `AGENTS.md`), manifests, migrations, CI, tests, deployments, secrets handling, schema, existing APIs, frontend, mobile, services and current test results. Never print secret values. Determine whether repository is new, existing Construction OS, or a different project. Stop before writing into an unrelated repository; identify the issue.
- Preserve working functionality; do not scaffold a second application alongside a viable existing one. Reuse SERK Finance, Sentinel or other systems only if a verified integration contract, repository access and authorization exist. Otherwise implement stable adapters and clearly mark disconnected integrations; do not claim they are operational.
- Create `docs/engineering/BASELINE.md`, `REQUIREMENTS.md`, `ARCHITECTURE.md`, `THREAT_MODEL.md`, `ADR/`, `TEST_MATRIX.md`, `CAPACITY_PLAN.md`, `RELEASE_GATES.md`, `RUNBOOK.md`, `CHANGELOG.md` and machine-readable `requirements.json` and `execution-state.json`. Map all 80 Bible domains to unique IDs, lifecycle commands, dependencies, ownership and acceptance tests. Import the module register from `spec/module-register.csv`. Break each domain into independently testable requirements. Mark status NOT_STARTED, IMPLEMENTING, IMPLEMENTED_UNVERIFIED, VERIFIED or BLOCKED. Never mark a domain VERIFIED because a page exists.
- Record current state, exact commands and failures. Design ADRs for architecture, tenancy, accounting, data lifecycle, synchronization, identity, workflow, AI providers, BIM, localization, storage, scaling and deployment. Include alternatives, constraints, decision and reversal plan. Do not block ordinary engineering on every unknown: record an assumption and use conservative configurable defaults. Escalate unresolved safety, accounting, regulatory, destructive or permission-sensitive decisions before enabling real usage.
- Maintain `execution-state.json`: milestone, next work item, branch, commit, requirement IDs, migrations, commands, outcomes, evidence paths, known blockers, assumptions and next resume command. Update after every completed vertical slice. Use `docs/engineering/` as the durable handover; do not rely on conversation context.

### 1.1 Definition of done per feature

A feature is complete only when there is a working UI (if relevant), server-side command and query implementation, authoritative persistence/migration, authorization including cross-tenant denial, input and state validation, concurrency handling, auditing, meaningful errors, accessible empty/loading/offline states, unit and integration tests, relevant E2E coverage, documented usage and configuration, and evidence of executed checks. Test against a real database, not only mocks. An external integration can be verified using a documented sandbox or contract tests but must be labelled accordingly. No dead navigation links or empty action buttons masquerading as features. Feature flags may hide unfinished modules.

## 2. Architecture and infrastructure

### 2.1 Baseline architecture

Use the existing Go/PostgreSQL foundation if sound. Prefer a modular monolith for transactional workflows; use service extraction only when clear scaling, isolation or organizational need exists. Candidate stack: Go, PostgreSQL/PostGIS, React/TypeScript, Flutter (or verified existing mobile framework), encrypted SQLite, S3-compatible object storage, Redis-compatible cache only where justified, outbox workers, NATS JetStream or Kafka when demand warrants, OpenTelemetry, OIDC identity, Python for specialized AI/optimization, IfcOpenShell and WebGL viewing, analytics read models/ClickHouse when justified. Temporal or another durable workflow engine may be introduced for long-lived processes when proven necessary. Avoid mandatory Kubernetes for small installations; support Compose local environment and infrastructure-as-code for managed/dedicated profiles. Pin versions and produce an SBOM.

Design bounded contexts: identity and tenancy; organizations; portfolio/program/project; estimation; schedule; work execution; procurement; inventory; equipment; workforce; contracts; finance; BIM/GIS; quality/HSE; documents; workflow; notification; reporting; sync; AI; country packs; integrations. Each context owns its tables, migrations and business logic. No arbitrary cross-domain SQL writes; use domain services and explicit contracts. Shared transaction boundaries for atomic financial and inventory effects where required. Distinguish business domain events from integration messages and audit records. Use CQRS selectively; authoritative transactional state is not a search index, cache, analytics store or LLM.

Separate control plane (tenant directory, deployment configuration, cell routing, entitlements, health) from data plane (tenant transactions/documents). Avoid global shared mutable business databases. Use tenant-aware service credentials, namespace/object policies, cache keys, search filters, queues and logs. Route to tenant home cell; a tenant can have multiple isolated project data partitions with one explicit accounting authority. Do not allow distributed writes that break ledger or stock invariants. Use explicit read models for global portfolio reporting and show freshness. Design tenant migration as snapshot → change catch-up → validated cutover → reversible route change; verify counts, hashes, ledger balances, outstanding jobs, file references and permissions. No live sharding migration without rehearsals and approval.

### 2.2 Suggested layout (adapt to actual repo)

```text
apps/{web,field-mobile,partner-portal,edge}/
backend/{cmd,internal,domains,platform,workers}/
intelligence/{gateway,agents,retrieval,forecasting,optimization,evaluation}/
contracts/{openapi,events,schemas}/
country-packs/{ET,shared}/
infra/{compose,terraform,deploy,observability}/
migrations/  tests/{unit,integration,e2e,security,load,offline,dr,ai}/
docs/engineering/  scripts/  fixtures/  spec/
```

Enforce dependency boundaries via lint/architecture tests. Do not put business logic in React components or database triggers alone; critical constraints must exist at authoritative boundaries. Provide `make dev`, `make migrate`, `make seed-demo`, `make test`, `make test-integration`, `make test-e2e`, `make security`, `make load-smoke`, `make verify` (or documented equivalents). Development setup must run without paid services using safe adapters or local emulators where feasible; clearly distinguish test adapters. Provide `.env.example` with placeholders only, health/readiness endpoints and seeded synthetic users via documented local-only mechanisms.

## 3. Canonical data, commands, API and events

### 3.1 Identity, dimensions and storage

Use globally unique opaque identifiers (UUIDv7 or established equivalent); human document numbers are tenant/entity/project/series scoped, allocated transactionally and never used as auth. Every tenant-bound relation carries tenant ID; use composite constraints to prevent cross-tenant relationships, not just app filters. Hierarchy: tenant → group → legal entity → books and branches; portfolio → program → project → site/zone → WBS → work package → activity. A project can have an owning entity and controlled participation from others. Explicit effective-dated relationships for joint ventures and intercompany charges. Store canonical UTC timestamps, local business date/timezone, accounting period, source occurrence time and server acceptance time as appropriate. Store currency code and exact decimal amounts, units and conversion version, exchange rate source/date. Avoid binary float for money/authoritative quantity. Precision, rounding, functional currency balancing and unit conversions must have testable policies. Maintain original signed/approved documents and references to versions, not mutable pointers alone.

Representative entities: tenant, membership, legal_entity, book, fiscal_period, project, project_party, wbs_node, activity, baseline, estimate, boq_revision, boq_item, rate_resource, cost_code, budget_revision, commitment, work_package, daily_report, measurement, contract, variation, claim, ipc, requisition, rfq, quote, po, receipt, inspection, item, lot, stock_movement, reservation, asset, meter_reading, work_order, employee, timesheet, payroll_run, invoice, payment, journal, journal_line, reconciliation, document, document_revision, transmittal, approval, audit_event, outbox, consumer_inbox, sync_operation, agent_run, evidence_link. Define indexes, foreign keys, deletion policies, state machines and ownership for each. Partition high-volume relations based on measured access and tenant/time distribution; design unique source identity registries so idempotency remains enforceable across partitions. Use RLS with non-owner app roles, FORCE ROW LEVEL SECURITY as appropriate, transaction-scoped tenant context, explicit cross-tenant administrative workflows, and tests for connection-pool reuse. Do not rely on RLS for storage, search or AI permissions.

### 3.2 Command protocol

Every mutation follows: authenticate → resolve tenant and authority → validate typed input → check idempotency key and payload fingerprint → load and lock relevant versions → authorize actor/state/amount/scope → enforce invariants → commit authoritative writes, audit and outbox atomically → return result → asynchronously dispatch and reconcile. Use optimistic versions and deterministic lock order; retry serialization/deadlocks with bounded jitter. If outcome unknown, query idempotency record rather than reapplying blindly. Same key/same payload returns original result; same key/different payload conflicts. Preserve idempotency across retention tiers for legally relevant source identities. Do not let drafts act as postings. For recurring, batched or external commands, store source/reference identity separately and guard against duplicate business effects.

API: versioned REST/OpenAPI, generated typed client where feasible, cursor pagination, bounded filtering, consistent problem details (RFC 9457 or equivalent), 401/403/404 policy, 409 conflicts, 422 validation, 429 throttling, 202 async jobs, signed uploads with quarantine and scan, resumable downloads where useful. Long reports and imports are jobs with progress, cancellation where safe, retry and audit; do not expose internal stack traces. Do not paginate with unbounded OFFSET for huge tables. Include `correlation_id`, request ID and trace context. Use server-generated trusted tenant context, never arbitrary client-supplied tenant headers without membership validation.

Event envelope example:

```json
{"event_id":"uuid","schema_version":1,"type":"inventory.MaterialIssuePosted","tenant_id":"uuid","entity_id":"uuid","project_id":"uuid","aggregate_id":"uuid","aggregate_version":7,"occurred_at":"UTC ISO-8601","correlation_id":"uuid","causation_id":"uuid","actor_id":"uuid","data":{}}
```

Outbox written in same transaction as business state; relay may publish more than once. Consumer inbox deduplicates and applies effect atomically; ordering guaranteed only where specified by aggregate/partition, not globally. Retry with exponential backoff, dead-letter visibility, safe replay, reconciliation jobs and schema compatibility. Treat all incoming webhooks as untrusted; verify signature, timestamp, replay window, idempotency, allowlist where appropriate and tenant binding. Do not promise exactly-once network delivery; guarantee idempotent business effects where possible.

### 3.3 Workflow engine

Build versioned state machines and configurable authority matrix: maker/checker, sequential/parallel approvals, amount and currency thresholds, delegated authority with expiry, escalation, rejection, rework, cancellation, compensating actions, attachments, immutable decisions and policy snapshot. Revalidate on execution, including changed bank account, amount, scope, version and actor. Approval tokens bind to a specific operation/payload hash and expiry; no reusable blanket authorization. Work-package release requires approved drawings, prerequisites, resources, permits and inspection plan; only authorized professional releases it. Audit all transitions and overrides. Never let an AI recommendation silently alter a policy.

## 4. Full functional implementation — 16 product systems, 80 domains

Build the module register, preserve all IDs and expose only verified capabilities. Deliver these as real vertical slices, with appropriate UI, APIs, domain rules and integrations. The Bible specifies additional detailed behavior and diagrams. Implement sector templates for building, road, bridge, water, EPC, infrastructure, manufacturing/prefab and real estate; common core plus extensions rather than forks.

### 4.1 COMMAND — enterprise, portfolio, program, strategy, executive

Group/legal entities, branch permissions, portfolio/program dependency graph, investment gates, project creation/close/reopen, business units, OKRs, risks, decisions, KPIs. Consolidated dashboards with cash, EAC, margin, progress, contract exposure, outstanding decisions, resource utilization, 13-week cash projections; metric dictionary defines basis, formula, source, freshness, currency, filters and drill-down. Support multi-company reporting and FX as-of rates, not naive sums of currencies. Export PDF/XLSX, row-level permissions and as-of dates; large exports asynchronous. CEO receives configured daily exception digest (email/Telegram only via authorized adapters). No synthetic data in production dashboards.

### 4.2 ESTIMATE — CRM, feasibility, land, tenders, BOQ, rates, bid intelligence

Lead/opportunity pipeline, feasibility and discounted cash-flow scenarios (document assumptions), land/permit records, tender calendars, secure bid rooms, eligibility checklists, attachments, bid approval and submission records. BOQ hierarchy and versioning, import XLSX/CSV with preview, staging, mappings, validation, duplicate detection, errors and explicit commit; PDF extraction as reviewed suggestions, not authoritative. Quantity units, dimensions and conversions. Resource libraries, crew productivity, material waste, machine hours, transport, overhead, risk, escalation, margin and tax. Unit cost = sum of resources with quantities × rates plus approved overhead/allowances; selling price = cost/(1-target margin) when margin is on sale price; distinguish markup. Show cost and price per unit, totals, scenarios, comparison of estimate versus actual historical rates with scope/date. Approved tender snapshot becomes original project budget via controlled mapping. Never overwrite original estimate or contract BOQ. Protect tender confidentiality and prevent leakage between competing bidders.

### 4.3 PLAN — budget, schedules, resources, risk and work packages

Original/revised/current budget, WBS/cost code mapping, contingency and budget transfers; check available budget in same transaction as commitment, separate commitments from actual, prevent double counting of consumed commitments. CPM forward/backward pass, dependency types FS/SS/FF/SF, lag, calendars, milestones, critical path and float; detect cycles and invalid dates, explicit baseline revisions; resource and cost-loaded scheduling, lookahead and Last Planner constraint tracking, progress status date, planned-vs-actual S-curves. Resource leveling and cross-project allocation with constraints and scenario comparison. Monte Carlo delay forecasts with distributions, assumptions and reproducible seeds. Earned value: PV, EV, AC, CV=EV−AC, SV=EV−PV, CPI=EV/AC, SPI=EV/PV (undefined denominators explicit), EAC=AC+ETC, VAC=BAC−EAC. Use approved baseline and measured progress; physical progress differs from financial and schedule progress. Model work packages with release criteria, prerequisites, assignment, quantity limits, evidence and acceptance.

### 4.4 BUILD — site, daily reports, work execution, measurement and engineering

Field-first mobile and web: today's tasks, drawings, attendance, labor/plant/material usage, weather (manual or verified integration), photographs, daily reports, issues, delays, instructions, progress, measurement books, chainage/location, quantity checks, geotag when consent/policy permits, engineering deliverables and inspections. Draft/submitted/reviewed/accepted/rejected versions, correction rather than silent overwrites. Track planned, executed, inspected, measured, certified, invoiced separately; quantity overrun requires controlled variation or exception. Offline evidence retained and duplicate-safe. Road earthworks: chainage, cross section survey imports, method/version, cut/fill volumes, haul routes and quantity reconciliation. Concrete pour: approved mix, batch, volume, samples, curing, inspection and release. Support RFI/submittal links and project instructions. No sensor or AI estimate counts as certified without authorized human review.

### 4.5 SUPPLY — demand, procurement, suppliers, subcontractors, logistics and import

Material requirements from schedule/BOQ/stock; reservation and requisition; budget checks; RFQ, sealed quotations, normalization by currency/UOM/tax/freight, technical and commercial comparison, supplier qualifications and conflict disclosures, PO approval and revisions, delivery tracking, inspection, GRN, three-way match PO/GRN/invoice with configurable tolerances, exceptions, accruals, payables and reconciliation. Contracted rates and blanket orders, partial deliveries, returns, cancellations, advances, guarantees, subcontract progress and retention. Import: proforma, FX, LC, customs, shipping, demurrage, freight, inland logistics, landed cost allocation by documented basis; no invented bank API. Supplier bank changes dual-controlled and independently verified before payment. Track procurement lead time, price variance, supplier quality and on-time delivery. Duplicate invoice prevention by source and normalized business keys. No self-approval or split-order bypass of thresholds.

### 4.6 STOCK — item master, stores, movement, warehouse and logistics

Items, UOM, conversions, batches/serials, warehouses/bins, site inventory, reorder and demand, reservations, GRN, transfer dispatch/in-transit/receipt, issue, return, scrap, adjustment, cycle count, quarantine, and material ledger. Quantity and valuation recorded together with approved costing method (weighted-average or FIFO per policy), currency, project dimensions, lot and evidence. Prevent negative stock with atomic checks/locks; controlled emergency policy records exceptions without hiding deficits. Concurrent issue tests, returns tied to source costs, reversal and valuation reconciliation. Track cement batches, reinforcement grades, fuel tanks, aggregates, concrete and tools. Bulk barcode/QR support; offline allocation through expiring quantity rights, not blind overwrite. Warehouse and finance reconciliation; no double counting inter-site transit.

### 4.7 FLEET — equipment, fuel, maintenance and fixed assets

Asset registry, ownership/rental, operator and project assignment, booking conflicts, utilization, meter readings, fuel delivery/dispense, maintenance plans, work orders, inspections, spare parts, warranties, repair downtime, internal equipment charge-out, project allocations, capitalization/depreciation/disposal integrated to finance. Validate meter monotonicity or documented replacement/rollover, duplicate readings, location anomalies and fuel variance. Cost per productive hour = attributable cost / productive hours; undefined if zero. Integrate optional GPS/telematics through typed adapters with provenance; sample data must be labelled. Maintenance predictions advise inspection; cannot autonomously release unsafe equipment. Track actual and forecast fleet cost per project.

### 4.8 PEOPLE — HR, attendance, payroll, competency and productivity

Employee/contractor lifecycle, contracts, wage types, hours, overtime, shift calendars, allowances, deductions, leave, site assignment, safety training, certifications, crew output and payroll allocation. Attendance offline, approval and exceptions, duplicate prevention, correction history. Payroll preview → review → approval → locked run → posting → approved payment and reconciliation. Effective-dated country rules, exact calculations, pay slips and statutory exports, multiple legal entities; separate protected personal data and access roles. No hard-coded tax rates without professional approval. Payroll totals and GL reconcile; retroactive adjustment via controlled run. Biometrics optional only with lawful authorization and privacy assessment; no automated employment decisions or discriminatory scoring.

### 4.9 CONTRACT — contract, variations, claims and IPC

Contract type (lump sum, remeasurement, unit rate, cost-plus, EPC), original signed version, BOQ, payment terms, notices, guarantees, bonds, insurance, advance, retention, obligations, milestones, defects period. Variation workflow: request → impact estimate → formal review → authorization → updated contract/BOQ/budget baseline mapping. Track pending separately from authorized. Claims evidence timeline, notice deadline rules and extensions; AI drafts only. IPC cumulative measured/verified/certified quantities × approved rates, previous certificate subtraction, retention, advance recovery, other deductions/tax separately configurable; partial and final certificates, correction and reconciliation. Certificate issuance, invoice, revenue recognition and collection are distinct. Prevent duplicate certification using unique source quantity allocation and cumulative controls. Authorized consultant portal with scoped evidence and e-signatures where supported. Preserve superseded certificates and notice history.

### 4.10 CAPITAL — financial operating system and accounting core

Implement section 5 in full. Include GL, AP, AR, project costing, treasury, tax, fixed assets, intercompany, consolidation, forecasting and audit. ERP operational documents must connect to controlled accounting; no dashboard that sums arbitrary estimates as posted financial actuals. Integrate SERK Finance only through verified contracts and avoid two independent authorities. Define book/source ownership explicitly.

### 4.11 DESIGN — BIM, GIS and digital twin

Original IFC files stored immutably, hash, model revision, parser version, metadata, object GUIDs scoped to model/revision, federation, visualization, 2D drawing review, BCF issue exchange, IDS validation where supported. Model-to-BOQ/WBS/work-package/procurement/inspection/asset mapping with provenance, manual review, version comparisons, quantity extraction with unit validation, 4D sequencing and 5D cost view. Use background parsers isolated from untrusted files, size/time quotas and malware scanning. Do not promise CAD authoring or certified quantity extraction without testing. GIS: PostGIS spatial objects, coordinate reference system and reprojection, chainage, site zones, map permissions and offline tiles only with appropriate licenses. Digital twin: installed asset IDs, as-built evidence, telemetry, time-series readings, data quality and source distinction. Optional drone/photo progress: reviewed observations, uncertainty and independent certification. Do not ingest real photos without authorization.

### 4.12 ASSURE — quality, lab, safety and sustainability

ITP templates and revision control, hold/witness points, samples and material test results, concrete/soil/asphalt, lab calibration, approved specifications, acceptance thresholds, NCR, corrective/preventive actions, reinspection and closure. HSE: hazards, risk assessments, permit to work, toolbox talks, training, incidents, near misses, emergency response, local stop-work, restricted sensitive reporting. Offline safety actions available locally; central conflict resolution preserves safety events. No automatic safety release, engineering certification or legal determinations by AI. Carbon, waste, water and energy tracking with factor provenance/version, boundaries, uncertainty and unit validation; distinguish measured vs estimated emissions. Commissioning checks and final acceptance.

### 4.13 ESTATE — real estate, facilities, commissioning and defects

Land, feasibility and development phases; units/floors, availability and pricing, reservations with atomic uniqueness/expiry, customer contracts, installments, collections, restricted/escrow fund policy where applicable, cost per square meter, sales absorption, handover and warranties. Facilities: installed assets, maintenance schedules, service requests, leasing, occupancy, utility records, work orders, SLAs. Digital handover packages include as-built revisions, manuals, certifications, warranties, commissioning/punch-list evidence. Owner/developer and contractor roles separated. No claims of escrow or statutory compliance without validation.

### 4.14 CONNECT — CDE, documents, tasks, workflow, stakeholder portals

Document metadata, immutable revisions, controlled states (draft/shared/published/archived or configured equivalent), transmittals, correspondence, RFIs, submittals, markups, tasks, minutes, decision logs, reminders, approvals, signature workflows, access groups. File uploads resumable, hash validation, quarantine, scan, safe rendering, signed short-lived URLs, controlled export/preview. Preserve original source and audit trails. QR verifies an authorized document/version and authenticity status without disclosing private data. Company logo, stamp and signature template distinct from legally recognized cryptographic signature. Portals for clients, consultants, suppliers, regulators; own-scope data and explicit sharing rules. Universal search filters authorization and rechecks before document access. Email/Telegram/SMS notifications opt-in, redact sensitive details and link to authenticated views. External APIs optional and scoped.

### 4.15 SENTINEL — security, audit and administration

Implement section 9. Tenant provisioning, configuration/version history, access reviews, role management, delegated authority, audit exports, operational monitoring, security alerts, incident workflows, backup policies, legal hold and disaster recovery. Integrate existing SERK Sentinel only through authenticated, tested interface; implement local secure defaults regardless. System admins lack automatic business authority. No silent access escalation.

### 4.16 BRAIN — intelligence, knowledge and automation

Implement section 10. All agents scoped, evidence-backed, evaluated and gated. Build knowledge management, lessons learned, authorized document retrieval, forecasting, optimization and model/provider orchestration. Never require an external LLM for deterministic operations or offline essentials.

## 5. Financial constitution — the highest-risk transactional system

Implement a chart of accounts, fiscal calendars/periods, journals, AP, AR, inventory subledger, assets, payroll, project cost ledger, bank statements, cash management, budgets, commitments, consolidation, intercompany, WIP and revenue accounting. Separate operational quantities, valuation, billings, accounting revenue, expenses and cash. Define accounting policies with an accountant before enabling real postings. All posted books immutable to normal application users. No UPDATE/DELETE of posted journals; correction via authorized reversals and adjustments. Drafts clearly distinguished. A posting transaction must balance in book functional currency after documented rounding; foreign currency legs retain original amount/rate. Require valid accounts, cost codes, source, entity, project and open period; protect posting vs close races with locks. Source registry prevents repeated journal creation, even across partition rollover. Cross-entity transactions generate separately balanced entries and intercompany references. Audit and outbox in same commit.

### 5.1 Atomic posting algorithm

```text
BEGIN transaction with tenant-scoped context
  authenticate and authorize maker/checker; verify approved payload version
  reserve idempotency key + source identity; reject conflicting payload
  lock fiscal period, authoritative source and affected controls in stable order
  validate open period, source state, accounts, currencies, project dimensions
  calculate journal lines with exact decimals and versioned posting rules
  require total debit = total credit in functional currency
  insert immutable journal + lines, authoritative subledger effects
  mark source posted; insert audit and outbox; persist result
COMMIT
resolve ambiguous commit by idempotency key before retry
```

Do not treat row CHECK constraints alone as sufficient for balanced multi-row transactions. Enforce at controlled posting service and database authority (e.g., deferred validation/authorized stored procedure), revoke direct write paths from normal roles. Close requires reconciliations, approved adjustments, open exceptions, period locking and sign-off; authorized reopening logged with reason and independent permission. Exports show as-of basis and version. Budget checks and available commitment reservations must be atomic and concurrency tested.

### 5.2 Posting matrix (illustrative; validate policy)

| Source | Typical debit | Typical credit |
|---|---|---|
| Accepted material receipt | Inventory | Goods received not invoiced |
| Matched supplier invoice | GRNI/approved variance/tax | AP |
| Material issue to job | Project cost/WIP | Inventory |
| Supplier payment settled | AP | Bank |
| Customer invoice | AR | Billing/revenue or contract clearing under policy |
| Customer receipt | Bank | AR |
| Approved payroll posting | Labor expense/WIP | Payroll liabilities |
| Equipment depreciation | Depreciation expense/project allocation | Accumulated depreciation |

Separate advance recovery, retention, tax, contract assets/liabilities and discounts. IFRS 15 where applicable: configure contract/performance obligations, approved transaction price, recognition method, progress measure, variable consideration review, accounting adjustments; never equate IPC with revenue by default. Consolidate functional currencies using approved FX rates and elimination rules. Intercompany matching and consolidation adjustments independently traceable. Financial statements include trial balance, balance sheet, income statement, cash flow, job cost, WIP, AP/AR aging, retention, commitments, reconciliation and bank reports. Reconcile AP/AR control, inventory valuation, asset register, payroll, project ledger and bank statements; mismatch blocks close or explicitly escalates. Test all reversals, partial receipts/invoices, FX, rounding, period close, concurrent actions, failed transactions and duplicate source events. Professional accounting acceptance is a release gate.

## 6. Offline-first and site-edge protocols

Design web/mobile online modes and field mobile/optional site-edge operation on local LAN without WAN. Essential field workflows do not require LLM or cloud. Encrypted SQLite device DB, device-bound keys/secure storage, encrypted edge backups and bounded offline authorization lease. Bootstrap with authorized snapshot, schema/version and cursor; only minimal authorized data. Local mutations append durable operation record before returning success. Distinguish local provisional state from centrally accepted state; show last sync, pending, rejected, conflict and offline permission expiry. Do not imply a central financial posting succeeded offline.

```json
{"op_id":"uuid","device_id":"uuid","local_sequence":42,"tenant_id":"uuid","project_id":"uuid","aggregate_id":"uuid","base_version":3,"command":"field.SubmitDailyReport","schema_version":1,"policy_version":7,"occurred_at":"client timestamp","payload":{},"integrity_proof":"device-authenticated envelope"}
```

Sync handshake authenticates device and user, checks revocation, lease, schema compatibility, server cursor and limits; uploads bounded batches/resumable attachments with checksums; verifies operation identity, sequence, causal dependencies, scope, permissions and base version. Server returns each result ACCEPTED/DUPLICATE/CONFLICT/REJECTED/RETRY with authoritative version and reason; pull deltas ordered by server cursor; apply durable acknowledgement only after local commit. Restart-safe queues, backpressure, jitter and connection-loss tolerance. Lost acknowledgments repeat same operation id. Preserve original client timestamp but don't trust device time for central posting. Clock skew detection and server timestamp. Offline quantity allocations require authoritative reservation rights and prevent double spending across devices; never allow blind last-write-wins for finance, stock, IPC, approvals, safety or contract versions. Append/merge independent notes where safe; conflicting edits create review task. Offline stop-work persists and remains safety effective locally; re-release requires authorized review. Handle revoked device after offline period: reject unauthorized mutations but provide controlled recovery of evidence as policy allows. No remote-wipe guarantee while disconnected.

Test airplane mode, repeated restart, battery loss, 7-day offline objective, out-of-order uploads, corrupted attachment, concurrent stock issue, expired permissions, lost ACK, duplicate, server upgrade, device loss, edge failover, low bandwidth and sync of thousands of operations. Unsynced data recovery separate from central RPO. Document local recovery and encrypted backups. Support data minimization and deletion of cached data when rights change.

## 7. Scale indefinitely in product design; preserve authorized history

### 7.1 Capacity tiers and routing

No application-level hard-coded lifetime limit, fixed ID sequence range in business logic, fixed time horizon in queries, hidden row cap, hard-coded single project/company, or deletion merely due to age. Use expandable cells, partitioning, object storage and archive catalog. Finite quotas, capacity and resource protection are required. Explicitly disclose capacity, measured performance and retention. Suggested benchmark tiers (not maximums): T1 100 projects/10M journal lines/200 concurrent users; T2 500 projects/50M lines/2,000 concurrent users; T3 billion-line class through planned multi-cell/partition architecture, measured at feasible scale. Test 100M+ rows and simulated archive/recovery; if hardware cannot do so, execute smaller tests, record actual result and create repeatable scale scripts. Do not extrapolate a certification from smoke tests.

Partition on measured workload (tenant/cell, time, project where justified); local indexes, partition pruning, incremental summaries, selective columns, cursor pagination, streaming reports, batch inserts/import jobs, bounded query plans, backpressure and per-tenant fairness. Make critical accounting source identity independent of partition boundaries. Use replicas for stale-tolerant reads only; route consistency-sensitive reads to authoritative primary. Large reports, BIM jobs, exports and analytics use separate resource budgets and queues. Protect transaction SLO from archive scans. Document hot-tenant isolation and cell move protocol, cross-cell read-model freshness, tenant-local transaction boundaries and disaster failover. Avoid distributed 2PC unless justified and tested; use compensating workflows for non-atomic cross-service actions.

### 7.2 Historical preservation architecture

Hot: current operational partitions and indexes. Warm: historical read-only relational/analytics tier, schema-aware. Cold: immutable object archive with original source files and open formats (Parquet/CSV/JSON plus manifests), schema/encoding version, checksums, record counts, business totals, original IDs, time ranges, encryption and key references. Archive job: select closed/eligible records → snapshot → export → verify counts/hash/GL totals → register signed manifest and catalog → rehearse restore → only then move/drop hot copy under approved policy. Preserve references and authoritative source identities; protect legal holds. Restore/retrieval job: authorize → locate catalog by tenant/time/type → verify hash/keys → reconstruct version-aware read model → reconcile → deliver report and evidence. Never silently omit archived data in historical reports. Distinguish as-of accounting, event and current correction views. Support historical schema migration and readable open-format customer export. Audit log independently retained; hash-chain anchoring to separately protected storage, not a false claim of absolute immutability.

Retention policy per data class/jurisdiction/customer, with effective dates, approval, hold, expiry, privacy deletion/anonymization and backup expiry. Preserve statutory/contract records only as lawfully required or authorized; no promise of unlimited retention of personal data. Encryption rotation must preserve authorized access to historical archives; test long-term key recovery. Version and validate archive manifests, data dictionary, formats and restore tools. Ensure customer exit/export even if software is discontinued.

### 7.3 Storage and performance economics

Implement capacity telemetry and a reproducible sizing tool: rows/day = transactions/day × rows/transaction; annual rows × 365; measured bytes per row + index/WAL/replication/backup/overhead. Example 1M business transactions/day × 6 lines × 365 = 2.19B lines/year; 300 hypothetical bytes/line = 657 GB raw/year, excluding overhead; not a benchmark. Forecast 90-day headroom, monitor disk, CPU, locks, WAL, vacuum, queue lag, replication, p95/p99, archive jobs and cost. Add cells/partitions/capacity before thresholds; record capacity and actual economics. Suggested central SLO target 99.95%, p95 <300 ms common APIs at specified load, RPO ≤5 min, RTO ≤60 min for defined protected central services; define test profile and report measured values, not guarantees. Time-series retention and compression separately evaluated; legal and customer policies prevail.

## 8. Premium UX, localization and integrations

Implement original minimal design, forest/ivory/graphite, high-contrast accessible typography, consistent spacing, responsive role-based workspaces. Desktop: sidebar, global command search, breadcrumbs, contextual actions, data tables, filters, saved views, sort, keyboard navigation, virtualized large lists, pagination and bulk jobs. Mobile: offline banner, prominent today/record/sync actions, large touch targets, lightweight photo upload, clear errors and accessibility. Include dark/light, reduced motion, accessible contrast, screen reader labels and WCAG 2.2 AA target; validate on representative devices. Every chart includes metric definition, source, freshness and drill-down. Real data or visibly marked synthetic fixtures only. Recreate diagrams as needed from the Bible; use licensed or original visuals, no scraped competitor UI. PDF preview before export, print-ready A4 forms, XLSX exports/imports with controlled mapping, document version, company identity, approval state, and verified signatures where available. Prevent spreadsheet formula injection and unauthorized exports. PDFs and reports can be generated asynchronously and expire according to access policy.

Country packs versioned independently: ETB, English, Amharic, Tigrinya, Afan Oromo, Gregorian/Ethiopian calendar display and input with round-trip tests, fiscal years, timezone, tax, payroll, document series, statutory forms, procurement rules, chart templates, currency/rounding and accounting policy. Store canonical ISO dates and derive Ethiopian equivalents via validated library; no lossy conversion. Professional local legal/accounting approval required, not hardcoded guessed legislation. Extend to other countries without forks. Localized strings, fonts, pluralization, number and date formats, right-to-left readiness, translation review. Protect data residency, data protection rights and lawful retention. 

Adapters for banking, payment, CAD/BIM, scheduling, productivity, messaging, mapping, government, IoT and security; discover supported APIs, terms, credentials and permission. Each adapter has versioned schema, scoped credentials, retry, timeout, circuit breaker, idempotency, reconciliation, monitoring, revocation and contract tests. No browser automation to evade provider rules. Do not implement actual bank transfers until authorized, verified sandbox, dual approval and reconciliation. Record unsupported interfaces as BLOCKED, not complete. Prefer CSV/XLSX/IFC standards when lawful integrations unavailable.

## 9. SERK SENTINEL — defense-in-depth, security and privacy

Threat model STRIDE-style plus construction-specific fraud: cross-tenant leakage, IDOR, malicious supplier bank changes, duplicate invoice/payment, bid disclosure, insider fraud, compromised admin, stolen edge device, ransomware, malicious BIM/spreadsheet/PDF, prompt injection, webhook replay, SSRF, dependency compromise and archive tampering. Map trust boundaries and mitigation tests. OIDC/passkeys/MFA, secure sessions/refresh rotation/revocation, least privilege RBAC + ABAC (tenant/entity/project/role/amount/state/classification), service identities, default deny, separation of duties and delegated authority expiry. No shared privileged accounts. Break-glass just-in-time access, approval, time limit and independent audit. Verify role/permission on every API, background task, report, download, search, cache and AI tool. Test direct object access, cross-tenant joins, RLS bypass via owner role, connection context reuse and stale permission caches.

Encryption in transit and at rest, KMS-backed keys and rotation, secure device secrets, restricted bank/HR fields, log redaction, protected object storage, signed download URLs, quarantine and malware scanning, sandbox file parsers, limits on decompression and file types. Use parameterized SQL, CSP, CSRF controls as relevant, secure headers, rate limits, input validation, output encoding, SSRF/network egress restrictions. Secrets outside repo; CI scans secrets, SAST, dependency CVEs, SBOM, signed images, protected branch, peer review, reproducible builds when feasible. Audit events include actor, scope, object, action, approved policy, before/after or controlled diff, source, device, time, correlation; no secret or excessive personal data. Tamper-evident independent retention and periodic verification. Security events routed to monitored alerting, incident triage, containment, rollback and recovery. No offensive actions beyond authorized synthetic/local security tests. Use OWASP ASVS, NIST AI RMF, buildingSMART standards and applicable privacy/accounting references; verify current versions and applicability. Independent penetration testing and professional compliance review required before production. Never claim certification from implementation alone.

## 10. SERK BRAIN — intelligent construction operating brain

Build model-independent AI gateway with provider registry, local/private and approved cloud routing, data classification/consent checks, usage and cost budgets, retries, timeouts, rate limits, redaction, observability and versioning. No direct production DB access for models or external provider. Retrieval ACL before embedding search and recheck on retrieval; tenant/project/version scopes, ingestion of authorized files only, source references, document freshness, deletion propagation, no cross-tenant training without explicit authorized policy. Treat all retrieved text and files as untrusted; prompt injection must not control tools. Model-generated SQL prohibited on unrestricted production DB; use typed reporting tools and read models. Structured tool schema, server-side permission checks, idempotent drafts and exact approval binding. Model failure must not prevent ordinary ERP operation.

### 10.1 Agent registry — 25 distinct roles

Executive, CFO, cost engineer, estimator, tender, scheduler, resource, procurement, supplier, warehouse, fleet, fuel, site engineer, quality, safety, BIM, quantity surveyor, contract, claims, accountant, payroll, document, sustainability, compliance and security. For every agent define use cases, data sources, required roles, allowed tools, prohibited operations, maximum tool calls, execution budget, model version, response schema, evaluation set, failure fallback and human owner. Start with read-only Executive/CFO/Cost/Procurement/Document agents; expand after evaluation. Build multi-agent orchestration as bounded task graph with dependency outputs and evidence, not uncontrolled recursive agent loops. Every agent must respect same authorization and approval as human users. Agents may propose actions; they may not silently post journals, release payments, certify safety/engineering, approve contract changes, decide hiring/termination or modify policies.

### 10.2 Decision package and authority protocol

L0 explain → L1 recommend → L2 prepare draft → L3 preauthorized low-risk reversible action → L4 explicitly authorized execution via ordinary command. No autonomous irreversible authority. Agent run stores actor, tenant/project, question, evidence IDs/version/timestamp, model/provider/version, prompt template version, assumptions, tool calls, uncertainty, alternatives, proposed impacts, risks, confidence only when calibrated, policy decision, approval and outcome. Enforce allowlist, authorization, approval hash, expiry and execution audit. Example: cost overrun → planner traces critical path delay → supply traces overdue material → resource agent proposes redeployment → CFO simulates cost/cash → executive packages alternatives → authorized human selects and approves specific actions. If evidence insufficient, abstain or ask for missing records, never fabricate. 

### 10.3 Non-LLM intelligence

Deterministic calculations for financial/quantity metrics; time-series/demand forecasting with backtesting; schedule uncertainty Monte Carlo with reproducible seeds; constrained optimization for equipment/crew/material and logistics; anomaly detection for fuel, invoice, cost and inventory using explainable thresholds/statistics before ML; computer vision proposals for progress; predictive maintenance based on approved telemetry. Track training dataset provenance, privacy, leakage, version, feature schema, drift, calibration and rollback. Evaluate against baselines; expose uncertainties and failure modes. Never use AI as engineering certification or safety decision authority. Collect human feedback only with appropriate permission. Report forecast errors, alert precision/recall, grounding, refusal, tool permissions, cross-tenant isolation, prompt injection, cost, latency and drift. Synthetic tests and human-reviewed holdouts; require evidence before enabling consequential workflows. Local inference may be limited by hardware; fallback to deterministic workflows.

## 11. Verification program — test like a regulated financial and industrial system

Implement traceability requirement → design → code → test → evidence → reviewer. Store command, timestamp, commit SHA, environment, fixture/version, result, artifact and actual failure. Unit tests, property/fuzz tests, real Postgres integration, E2E browser/mobile, contract tests, migration upgrade/downgrade compatibility, concurrency/race, tenant isolation, offline/restart, security, performance, DR, localization, accounting, BIM and AI evaluation. CI checks build, format, lint, typecheck, unit/integration, E2E smoke, migration, secrets/dependency scans and artifact generation. Do not suppress failures or disable tests to make CI green. Run full suite where environment permits; mark blocked tests with exact reason and next action. Independent penetration test and professional accounting, engineering and HSE UAT are human release gates.

Mandatory scenarios (extend to every domain):

| ID | Scenario | Required result |
|---|---|---|
| FIN-001 | Balanced journal, multi-currency, rounding | No unbalanced posting; correct policy |
| FIN-002 | Repeated source, retry after uncertain commit | One business effect |
| FIN-003 | Concurrent posting vs period close | No unauthorized closed-period posting |
| FIN-004 | Reversal, AP/AR/inventory reconciliation | Original preserved; totals reconcile |
| SEC-001 | Tenant A reads B via API/search/export/AI/job | Denied; no metadata leakage |
| SEC-002 | Self-approval and changed payload after approval | Denied; independent fresh approval |
| SEC-003 | Malicious file, replay, token reuse | Quarantine/deny with audit |
| INV-001 | Concurrent issues exceed stock | No unauthorized negative stock |
| INV-002 | Transfer, partial delivery, return | Movement and valuation reconcile |
| PROC-001 | PO/GRN/invoice mismatch and duplicate invoice | Block or authorized exception |
| IPC-001 | Concurrent/duplicate cumulative certification | No duplicate quantities or billing |
| PLAN-001 | CPM cycle, calendars, zero denominators | Clear validation and defined metrics |
| OFF-001 | Airplane mode, app/device restart | Durable local data and pending status |
| OFF-002 | Lost ACK, repeat, conflict, revocation | Idempotent effects, conflict evidence |
| DOC-001 | Revision, restricted export, malicious file | Approved history intact; scope enforced |
| AI-001 | Injection in document, unauthorized tool | No secret disclosure or unauthorized effect |
| AI-002 | Insufficient evidence, forecast uncertainty | Abstain or show caveats and sources |
| SCALE-001 | Large data/archived report, hot-tenant load | Correct totals, isolation, measured SLO |
| HIST-001 | Archive and restore old schema/data | IDs, hashes, totals and access verified |
| DR-001 | Restore backup and reconcile | Measured RPO/RTO; no hidden loss |
| LOC-001 | Ethiopian calendar, currency, rules boundary | Valid round trips and reviewed fixtures |
| E2E-001 | Tender to receipt and project reporting | Operational and financial reconciliation |

Golden path fixture: two tenants, two companies, multiple concurrent projects, approved BOQ and control budget; order, partial receipt and material issue, labor and equipment, certified quantities, IPC, invoice, payment, reconciliation, variance and executive dashboard. Include negative paths, pending variation, retention/advance, correction, FX, offline site, denied cross-tenant actor, failed external integration, archive/retrieval. Provide deterministic seed and expected ledger totals; run and export evidence. Never use actual customer details in demo. Load generator must vary skew, users, projects, years, file sizes and transaction ratios; record hardware, throughput, latency, error rate, DB locks, I/O, queue lag and integrity checks. Stress tests must be opt-in and confined to authorized environments.

## 12. Delivery milestones and exact execution order

Proceed through vertical slices with executable evidence. Do not start 80 simultaneous incomplete modules. Within each milestone, make bounded changes, run tests, update requirement matrix, checkpoint and commit to the feature branch when authorized. Dependencies matter more than calendar estimates.

| Milestone | Required integrated outcome | Gate |
|---|---|---|
| M0 Discover | Repo audit, ADRs, requirement register, risk and baseline test report | No destructive changes; known status |
| M1 Foundation | Local runnable stack, identity, tenancy, roles, organizations, audit, workflow primitives, migrations, API clients | Cross-tenant and role tests pass |
| M2 Project/estimate | Project/WBS, BOQ import, rate analysis, approved budget, revisions | Import/version/budget invariants |
| M3 Supply/stock/GL | PR/RFQ/PO/GRN, reservation and issue, posting, project cost and reconciliation | Atomic posting and concurrency |
| M4 Field/IPC | Daily reports, work packages, measurement, certification, invoice, receipt, executive report | Complete golden path |
| M5 Offline | Field app and edge sync, local safety, retries, conflict UX | Restart, revocation and duplication tests |
| M6 Enterprise ops | Schedule/CPM/EVM, resources, fleet, maintenance, payroll, quality and HSE | Integrated job cost, UAT fixtures |
| M7 Contracts/CDE | Variations, claims, document controls, portal, real estate and handover | Permission, signature/approval and retention |
| M8 BIM/GIS | IFC import/view, model mappings, 4D/5D, GIS, asset digital record | Licensed standards and measured models |
| M9 BRAIN | Read-only evidence agents, governed draft tools, prediction/optimization | AI evaluation and safety authorization |
| M10 Scale/history | Cell architecture, partitions, archive catalog, historical reports, restore and capacity testing | Reconciled historical retrieval, benchmark |
| M11 Localization/deploy | ET pack, validated other packs as available, cloud/hybrid/on-prem profiles, observability and support | Local professional approval, DR |
| M12 Release candidate | Complete requirement coverage, hardened UI, security review, UAT, rollback and handover | Human release decision only |

For each milestone: (1) review relevant Bible sections; (2) decompose into issues with acceptance IDs; (3) implement smallest complete slice; (4) migrate and run; (5) verify negative cases; (6) capture evidence; (7) document blockers and next work. If time/context is insufficient, finish a coherent slice, persist checkpoint and resume at next item. Do not fabricate a completion timeline; the full enterprise scope is a multi-release program. The Bible's 36-month schedule is indicative, not a promise.

## 13. Deployment, operational readiness and customer rollout

Provide local reproducible environment, staging and production templates (no live deployment without permission). Profiles: managed single cell for small contractors; enterprise dedicated cells/replicas; sovereign/on-prem approved infrastructure. Infrastructure as code, health checks, rolling deployment and compatibility, migration expand-contract, backup before risky migration, rollback/roll-forward procedure and restore rehearsal. Avoid auto-destructive migrations. Observability: OpenTelemetry traces, metrics, structured logs without secrets, correlation, queue lag, sync conflicts, lock contention, reconciliation, security and AI cost; alerts and incident runbooks. Encrypted PITR/offsite immutable backups, key recovery, restore in isolated environment, verify ledger/files/permissions/archives and measure RPO/RTO. Separate edge backup. Operational support: data migration/import staging, tenant onboarding, access reviews, training, SLA, support escalation, customer export and deletion/retention workflows. Pilot synthetic first, then approved historical, then limited real projects with parallel reconciliation and documented human acceptance. Senselet Construction and Vision Real Estate are potential pilots only if their authorized representatives approve.

## 14. Final deliverable and reporting format

Produce functioning source code, reviewed migrations, API/event schemas, local deployment instructions, demo fixture, role-specific UI, tests, CI, threat model, release and recovery runbooks, architecture diagrams, API docs, operator/admin/user guides, module/requirement status and known limitations. Include `docs/engineering/HANDOVER.md` with repository/branch/commit, implementation summary, exact run/test commands, test counts actual not invented, evidence links, required credentials/integrations, security findings, unresolved issues, professional review requirements, next tasks and release status. Include a truthful capability matrix: verified, implemented-unverified, blocked, not started. Demonstrate each integrated slice with actual data and screenshots only from running app when available. Record assumptions and deviations from Bible. No release claim without evidence; independent review cannot be replaced by your own generated tests. Provide reproducible build instructions for the next engineer. Do not expose private credentials in documentation or screenshots.

### First action now

Inspect the repository and the attached specifications. Start M0, create the baseline and requirement matrix, then implement M1 and the next coherent vertical slices as resources allow. Do not merely summarize these instructions. If tools cannot edit a repository, explain the blocker and produce the exact files/patches that can be safely delivered. At each session end, report what actually changed, what was verified, what remains and the precise next command. Continue from `execution-state.json` on subsequent sessions. Preserve the user's architecture intent: unlimited product-level project/transaction/history growth, no arbitrary deletion, robust financial governance, African realities and enterprise-grade reliability — implemented with measurable, lawful, finite infrastructure.
