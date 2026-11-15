# Test matrix

How requirements are tested, and where the 22 mandatory scenarios of `MASTER_PROMPT.md` section 11 stand. Per-requirement evidence is in `REQUIREMENTS.md`; per-test results of the last executed run are in `evidence/`.

## Layers present

| Layer | Where | Runs against |
|---|---|---|
| Unit and golden | `pkg/*` | pure Go |
| Integration, adversarial (raw SQL as the tenant login and as the owner), concurrency | `internal/kernel/*_test.go` | real PostgreSQL 16, fresh database per test world, race detector |
| API | `internal/httpapi/*_api_test.go` | HTTP server over the same database |
| Contract | `TestOpenAPIMatchesRoutes` | served routes vs `api/openapi.yaml` |
| Browser end-to-end with accessibility audit | `e2e/*.mjs` via `TestConsoleBrowser*` | Chromium (Playwright) |
| Generated-document freshness | role matrix, KPI and report catalogues | — |
| Recovery drill | `deploy/drills/*.sh` | disposable clusters |

## Layers absent

Property/fuzz tests, load and scale tests, migration downgrade tests, mobile/offline client tests, AI evaluation sets, BIM fixtures, localisation round-trip tests, DAST. Each is an open item in `RELEASE_GATES.md`.

## Mandatory scenarios

Status: **COVERED** = the named tests exercise the required result and passed in the last run; **PARTIAL** = part of the result is exercised, the gap is stated; **NOT COVERED** = no executed test.

| ID | Required result | Status | Tests / gap |
|---|---|---|---|
| FIN-001 | No unbalanced posting; rounding by policy | COVERED | `TestUnbalancedRejectedInGoAndInDatabase`, `TestSC14_DatabaseEnforcesCurrencyPrecision`, `TestConvertIsExactAndBoundToSnapshot`, `TestJourneyA_RoundingMatchesGo` |
| FIN-002 | One business effect on repeat/retry | COVERED | `TestIdempotentReplayAndConflict`, `TestConcurrentIdenticalCreatesAreIdempotent`, `TestR7_IdempotencyKeyPreclaim` |
| FIN-003 | No posting into a closed period under a race | COVERED | `TestSC08_PostWaitsForInFlightCloseThenRefuses`, `TestSC08_CloseWaitsForInFlightPostAndIncludesIt`, `TestR8_ARApprovalAfterPeriodClose` |
| FIN-004 | Reversal preserves the original; subledgers reconcile | COVERED | `TestReversalFlow`, `TestJourneyA_BOQToBankReconciliationToGL`, `TestJourneyB_ContractToCashToGL`, `TestJourneyC_StockValuationToGL` |
| SEC-001 | Tenant A cannot read B by API, search, export, AI or job | PARTIAL | `TestSC04_CompromisedTenantCredentialCannotReachOtherTenant`, `TestTenantIsolation`, `TestWorkflow_TenantIsolation`, `TestR3_11_CrossTenant`, `TestDocuments_ScopeAndTenantIsolation`, `TestPrivileges_OwnerMaintenancePolicyEverywhere` (every table has the policy). Gap: no test enumerates every route with a foreign tenant's token; there is no search index to test |
| SEC-002 | No self-approval; changed payload needs a fresh approval | COVERED | `TestSegregationOfDuties`, `TestApprovedContentIsFrozen`, `TestWorkflow_SubjectChangedAfterStartIsRefused`, `TestAttestation_ReplaySubstitutionExpiryAndTampering` |
| SEC-003 | Malicious file, replay, token reuse denied with audit | COVERED | `TestDocSecurity_FailClosedQuarantine`, `TestDocSecurity_InfectedAndMaliciousUploads`, `TestDocSecurity_RealScannerAndObjectStore`, `TestInspect`, `TestDocSecurity_DownloadLinksAndExportBudget`, `TestWebhookSignedDeliveryAndReceiverChecks`, `TestSessions_RevocationKillsOldTokensIncludingInFlightApprovals`, `TestTokenRejections`. Limit: scanner detection coverage is not measured (EICAR-only signature set in the evidence runs) |
| INV-001 | Concurrent issues cannot overdraw stock | COVERED | `TestJourneyC_Controls` (two concurrent issues, exactly one approved), `TestR7_ConcurrentPutAwayAndCount`, `TestR8_RawSQL_Stock` |
| INV-002 | Transfer, partial delivery, return reconcile | PARTIAL | `TestJourneyC_StockTransfer`, `TestR8_TransferLifecycleAndRaces`, `TestR7_PartialPutAwayOvershoot`. Gap: stock returns are not implemented |
| PROC-001 | Mismatch and duplicate invoice blocked or excepted | COVERED | `TestJourneyA_ReceiptAndMatchingControls` |
| IPC-001 | No duplicate certified quantity or billing | COVERED | `TestR6_15_ConcurrentMeasurementVerification`, `TestR6_14_ConcurrentConfirmations`, `TestCreditQuantityCorrection_NoDoubleDeduction`, `TestJourneyB_Controls` |
| PLAN-001 | CPM cycles, calendars, zero denominators | COVERED | `TestCPMRefusesInvalidNetworks`, `TestCalendarSixDayWeekWithHoliday`, `TestComputeAndZeroDenominatorPolicy` |
| OFF-001 | Airplane mode and restart keep local data | NOT COVERED | No field client exists |
| OFF-002 | Lost ACK, repeat, conflict, revocation | PARTIAL | Server side: `TestSync_IdempotencyConflictsAndRevocation`, `TestSync_ConcurrentRetriesApplyOnce`, `TestR12_TransientFailureIsRetried`. Gap: no client |
| DOC-001 | Revision history intact; scope enforced; malicious file | PARTIAL | `TestDocuments_RevisionsAndPublication`, `TestDocuments_ConcurrentPublicationKeepsOneCurrent`, `TestDocuments_ScopeAndTenantIsolation`, `TestAPIDocumentsAndRFIs`, `TestConsoleBrowserDocuments`, `TestDocSecurity_*`, `TestAPIDocumentSecurity`. Gap: no retention policy or legal hold; no restricted-export classification |
| AI-001 | Injection and unauthorised tool have no effect | PARTIAL | `TestGovernedAI_DraftOnly`, `TestPlatform_IntelligenceAndAssistant`. Gap: there is no retrieval or tool use to inject into, so no injection test exists |
| AI-002 | Abstain or show caveats on thin evidence | PARTIAL | `TestPlatform_IntelligenceAndAssistant` (answers carry sources, assumptions and missing data). Gap: no forecast-uncertainty evaluation |
| SCALE-001 | Correct totals and isolation under large data and load | PARTIAL | `tools/capacity/fleet_measure.sh` (2026-11-15): fleet register and summary query times at 100,000 machines and 1,000,000 logs through a tenant login (`CAPACITY_PLAN.md`). Gap: one session, one domain, no concurrent load, no API layer, no hot-tenant test |
| HIST-001 | Archive and restore old data | NOT COVERED | No archive exists |
| DR-001 | Restore and reconcile with measured RPO/RTO | PARTIAL | `deploy/drills/pitr_drill.sh` run on 2026-11-15 (`evidence/2026-11-15-pitr-drill.txt`): PASS; 16 table fingerprints identical at the restore point, audit chains verify, all 52 migrations replayed; restore verified in 0.64 s; 5 s of committed writes lost on losing the primary with `archive_timeout=10s`. Gap: disposable local clusters and a few hundred rows; no offsite, encrypted or production-scale restore in this session |
| LOC-001 | Ethiopian calendar, currency, rules | PARTIAL | `TestParseQuantisesToMinorUnits`, `TestAllocateConservesEveryMinorUnit`, `TestPDFEthiopicFontOnlyWhenNeeded`. Gap: no Ethiopian calendar conversion, no reviewed statutory fixtures |
| E2E-001 | Tender to receipt with reconciliation | PARTIAL | `TestAPIJourneyA_EndToEnd`, `TestAPIJourneyB_ContractToCash`, `TestAPIJourneyC_StockValuation` and the browser journeys cover BOQ → budget → purchase → receipt → issue → measurement → IPC → invoice → receipt → reports. Gap: tender and estimate stages do not exist |

## Evidence rule

Every run that is cited is stored as a per-test list in `evidence/` with its date; `requirements.json` names the file and the commit. `tools/requirements_register.py` refuses to write VERIFIED for a requirement whose tests are not PASS in that file.

## Directive checks of 2026-11-15 (priority A.3 and section 11)

| Check | Tests |
|---|---|
| Cross-tenant document access | `TestDocuments_ScopeAndTenantIsolation`, `TestDocSecurity_DownloadLinksAndExportBudget` (link refused to another tenant) |
| Unauthorised download URLs | `TestDocSecurity_DownloadLinksAndExportBudget`, `TestAPIDocumentSecurity` (forged, altered, truncated, expired, other key, other project) |
| Self-approval | `TestDocuments_RevisionsAndPublication`, `TestFleet_RegisterAndLifecycle`, `TestFleet_ReservationsNeverDoubleBook`, `TestFleet_DailyLogsAndMeter` |
| Stale MFA | `TestDocuments_RevisionsAndPublication`, `TestDocSecurity_FailClosedQuarantine`, `TestFleet_RegisterAndLifecycle` |
| Superseded document publication | `TestDocSecurity_InfectedAndMaliciousUploads` (superseded, current and discarded revisions cannot be published again) |
| Duplicate RFI answers | `TestDocuments_RFILifecycle` |
| Concurrent publication | `TestDocuments_ConcurrentPublicationKeepsOneCurrent` |
| Malicious uploads | `TestInspect`, `TestDocSecurity_InfectedAndMaliciousUploads`, `TestAPIDocumentSecurity` |
| Export-budget bypass | `TestDocSecurity_DownloadLinksAndExportBudget` |
| 1. Simultaneous reservations cannot double-book | `TestFleet_ReservationsNeverDoubleBook` (16 concurrent approvals, exactly one) |
| 2. Equipment under maintenance or hold cannot be released improperly | `TestFleet_RegisterAndLifecycle`, `TestLifecycleRules` |
| 3. Fuel issues cannot cause negative inventory | Not applicable yet: fuel is not built. Existing stock controls: `TestJourneyC_Controls` |
| 4. Duplicate logs cannot generate duplicate project costs | `TestFleet_DailyLogsAndMeter` (one log per machine, day and shift; offline repeat recorded once). No cost is generated from logs yet |
| 5. Every financial posting balances | Unchanged ledger tests; fleet posts nothing yet |
| 6. Cross-tenant equipment access rejected | `TestFleet_TenantIsolationAndPaging`. Maintenance and telemetry do not exist yet |
| 7. Offline replay cannot duplicate | `TestFleet_DailyLogsAndMeter` (eight concurrent repeats of one client reference), `TestSync_ConcurrentRetriesApplyOnce` |
| 8. Unauthorised users cannot approve restricted transitions | `TestFleet_RegisterAndLifecycle`, `TestAPIFleet` |
| 9. Historical records remain traceable after status changes | `TestFleet_RegisterAndLifecycle` (16 events on a disposed machine, identity intact) |
| 10. Failed event processing can be retried | Unchanged: `TestRelayRetryDeadLetterHeadOfLineAndReplay` |
| 11. Malicious or unscanned documents stay quarantined | `TestDocSecurity_FailClosedQuarantine`, `TestDocSecurity_InfectedAndMaliciousUploads`, `TestDocSecurity_RealScannerAndObjectStore` |
| 12. Recovery reproduces data and migration state | `evidence/2026-11-15-pitr-drill-0054.txt` |
