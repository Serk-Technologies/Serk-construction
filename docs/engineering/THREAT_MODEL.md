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

## Still open from the Bible's list

Independent penetration test, a real identity provider with step-up, HSM/KMS custody of the signer key, object-lock backups, prompt-injection tests (no retrieval exists yet), SSRF review of any future connector. These need people and infrastructure and are tracked in `docs/release/GO_NO_GO.md`.
