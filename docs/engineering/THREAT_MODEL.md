# Threat model

The maintained threat models are `docs/architecture/THREAT_MODEL.md` and `docs/security/V040_THREAT_MODEL.md`, with twelve independent AI review reports in `docs/security/` and the adversarial results in `docs/security/ADVERSARIAL_RESULTS.md`. This page adds only what this session's change introduces.

## Added by controlled documents (migration 0052)

| Threat | Mitigation | Test | Residual |
|---|---|---|---|
| Malicious file uploaded and opened by a colleague | Media-type allow-list, leading-byte check, 10 MiB cap; served only as an attachment with `nosniff`; never rendered in the console's origin | `TestDocuments_RevisionsAndPublication`, `TestAPIDocumentsAndRFIs` | **No malware scan.** A malicious PDF or Office file that is a valid file of its type passes. Open requirement CNX-01.4 |
| Stored file swapped after approval | No UPDATE or DELETE grant on contents; database-computed SHA-256 bound into the approval attestation; re-hash on download | same | A schema owner can still alter rows; covered by the existing owner-maintenance controls |
| Superseded drawing used for construction | One current revision per document by unique index; supersession only with an attested successor; status shown with every download | `TestDocuments_ConcurrentPublicationKeepsOneCurrent` | Printed copies are outside the system |
| Self-publication, forged publication | Maker/checker constraint, fresh MFA, human-only decision, attestation checked in SQL | `TestDocuments_RevisionsAndPublication` | Depends on the identity provider's MFA claim, as for every approval |
| Reading another project's or tenant's documents | Project scope in the kernel, RLS in the database; absence and refusal are indistinguishable | `TestDocuments_ScopeAndTenantIsolation` | — |
| Bulk download (exfiltration) | Each download recorded and counted against the daily export budget; security notified at the limit | `TestDocuments_RevisionsAndPublication` (event), `TestPlatform_ExportBudget` (budget) | The budget is per person per day, not per byte |
| Large uploads as denial of service | The larger body limit applies to one authenticated route; in-flight and rate limits unchanged | `TestAPIDocumentsAndRFIs` (413 elsewhere) | 14.3 MiB bodies are held in memory; lower `MaxInFlight` if memory is tight |
| AI answering an RFI | `rfi.answer` is a human decision in Go and SQL | `TestDocuments_RFILifecycle` | — |

## Changed by document security (migration 0053, ADR 0019)

The "no malware scan" residual above is closed for deployments that run the ClamAV daemon; the table rows on malicious files and bulk download now have these controls.

| Threat | Mitigation | Test | Residual |
|---|---|---|---|
| Malware uploaded and opened by a colleague | Scan at upload; unscanned, failed-scan and infected files cannot be submitted, published or downloaded while scanning is required (default); enforced in kernel and database | `TestDocSecurity_FailClosedQuarantine`, `TestDocSecurity_InfectedAndMaliciousUploads`, `TestDocSecurity_RealScannerAndObjectStore` | Detection is the scanner's; evidence used an EICAR-only signature set. A tenant can turn the requirement off by an attested decision |
| Verdict forged in the database | Keyed proof only the API can compute; a verdict without it does not count | `TestDocSecurity_FailClosedQuarantine` | The key is in the API process |
| Archive bomb, path traversal, macros, active PDF, SVG/HTML | Refused before storage; refusals audited | `TestInspect`, `TestAPIDocumentSecurity` | PDF checks are heuristic |
| Storage provider or bucket misconfiguration exposes files | Objects encrypted by the service per tenant; anonymous read refused in the integration test | `TestS3AgainstARealServer`, `TestSealedRoundTripAndTamperDetection` | Bucket policy, versioning and object lock are the operator's |
| Object swapped, truncated or repointed | AEAD binds key and frame order; database fixes the key; whole-file hash on read | `TestDocSecurity_ObjectStorageAndMigration` | — |
| Stolen or guessed download link | Signed, two-minute life, bound to tenant, revision and person, re-checked at use, audited | `TestDocSecurity_DownloadLinksAndExportBudget` | Reusable until expiry; appears in the request path |
| Export budget bypass through links or streams | Budget counted at issue and at open | same | Per person per day, not per byte |
| Loss of files during storage migration | Copy verified by read-back; bytes released only by the schema owner | `TestDocSecurity_ObjectStorageAndMigration` | Release does not re-read the object |
| Loss of `SERK_DOCUMENT_KEY` | — | — | **Every stored object becomes unreadable.** Key custody and rotation are open |

## Added by SERK FLEET part 1 (migration 0054, ADR 0020)

| Threat | Mitigation | Test | Residual |
|---|---|---|---|
| Unsafe machine put back to work | Out-of-service states leave only through `fleet.release` with fresh MFA, human, attested; duty conflict with operation | `TestFleet_RegisterAndLifecycle`, `TestLifecycleRules` | The certifier's competence is outside the system |
| Machine diverted to another project | Project changes only by mobilisation against that project's approved reservation | `TestFleet_RegisterAndLifecycle` | Physical movement is not sensed (no telematics) |
| Double-booking | Exclusion constraint | `TestFleet_ReservationsNeverDoubleBook` | — |
| Hours inflated or meter rolled back | Hours bounded by meter and day; meter monotonic; moves only with an attested approval; second-person approval | `TestFleet_DailyLogsAndMeter` | Readings are typed by people; fuel is recorded, not reconciled |
| Duplicate log from an offline retry | Unique day/shift and client reference | `TestFleet_DailyLogsAndMeter` | — |
| Ghost equipment or duplicate registration | Normalised unique serial and registration; commissioning by a second person | `TestFleet_RegisterAndLifecycle` | — |
| History rewritten | No write grant on history; written by the database | `TestFleet_RegisterAndLifecycle` | Schema owner, as everywhere |
| Cross-tenant access | RLS on all six tables | `TestFleet_TenantIsolationAndPaging` | — |

## Still open from the Bible's list

Independent penetration test, a real identity provider with step-up, HSM/KMS custody of the signer key, object-lock backups, prompt-injection tests (no retrieval exists yet), SSRF review of any future connector. These need people and infrastructure and are tracked in `docs/release/GO_NO_GO.md`.
