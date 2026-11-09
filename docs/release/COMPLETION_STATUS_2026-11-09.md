# Completion status — SERK Construction OS, 2026-11-09

What is finished, what is partly finished and what remains until the final release. Source of truth: the status board in `docs/IMPLEMENTATION_PLAN.md` and the evidence files in `docs/release/evidence/`.

## Finished and verified (30 work packages)

Each has database-enforced controls, automated tests, a console page and a page guide.

| Package | What |
|---|---|
| WP-0.1 | Scrollable collapsible sidebar |
| WP-0.2 | Continuous integration |
| WP-0.3 | Containerised dev environment |
| WP-0.8 | Self-explanatory pages |
| WP-1.2 | Authority matrix, delegation, duty conflicts |
| WP-1.3 | Budget revisions and transfers |
| WP-1.4 | Control dashboard and drill-down |
| WP-1.5 | Report catalogue |
| WP-1.6 | Units of measure, reservations, reorder |
| WP-1.7 | Multi-entity, project master |
| WP-1.8 | Onboarding wizard, imports |
| WP-1.10 | People, roles and profiles (account menu, dependency graph, adjustable roles) |
| WP-2.1 | Cost forecasting |
| WP-2.2 | WIP schedule |
| WP-2.3 | IFRS 15 revenue |
| WP-2.4 | Change events |
| WP-2.6 | Claims, EOT, notices, damages |
| WP-2.7 | Guarantees and price adjustment |
| WP-2.8 | Workforce and payroll |
| WP-2.9 | Fixed assets |
| WP-2.10 | Consolidation and intercompany |
| WP-2.11 | Tax returns and e-invoicing (stand-in adapter) |
| WP-2.12 | Cash forecasting |
| WP-2.13 | Fiscal calendar, accruals, overhead |
| WP-3.3 | Timesheets |
| WP-3.4 | Equipment and fleet |
| WP-3.6 | Quality |
| WP-3.7 | Health, safety and environment |
| WP-3.8 | Punch lists, instructions, meetings |
| WP-3.9 | Progress rules of credit and installed quantities |

## Partly finished

| Package | State |
|---|---|
| WP-1.9 | R1 hardening: documents and contract done; user acceptance testing, penetration test and go decision pending |
| WP-3.5 | Document control: built documents, revisions, publication, transmittals, RFIs, submittals; pending object storage, virus scan, OCR, drawing and IFC viewers |
| WP-2.5 | Subcontracts: built subcontracts, certificates, payments, documents; pending statement settlement, subcontract variations, portal, certificate PDF |

## Not started

| Package | What |
|---|---|
| WP-0.4 | Design system and translations |
| WP-0.5 | Identity provider: OIDC, step-up, revocation |
| WP-0.6 | Staging: TLS, vault, key management signer |
| WP-0.7 | Observability |
| WP-1.1 | Gate A closure, penetration test, load test |
| WP-3.1 | Mobile field app (handled by the user) |
| WP-3.2 | Planning extensions, P6 and MS Project import |
| WP-4.1 | External portals |
| WP-4.2 | Document AI |
| WP-4.3 | Predictive risk |
| WP-4.4 | Report builder, BI feed |
| WP-4.5 | Notification channels, SCIM |
| WP-4.6 | Integrations |
| WP-4.7 | Scale and certification readiness |

## Known limits of finished work (called out in each evidence file)

- Payroll tax bands, tax rules and demo rates are test data, not statements of current law; they need the adviser-signed fixtures (decision D-04). E-invoicing uses a stand-in adapter until the authority format is confirmed (D-06).
- Consolidation keeps equity at the closing rate (no historical rates) and does not match intercompany items by reference.
- Withholding is not yet deducted automatically on supplier payments and customer receipts.
- Menu entries for finance pages are shown to some roles who are then refused on opening (the refusal is correct; the menu should hide them).
- The tests run against synthetic data only. No real company data is loaded.

## Verification

`go vet ./...` and the full automated suite (all packages, including the real-browser journeys) pass on PostgreSQL 16; see the latest evidence files for counts.
