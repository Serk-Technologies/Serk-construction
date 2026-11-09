# WP-3.5 Document control, RFIs (partial) and WP-3.9 Rules of credit: verification, 2026-11-09

Migrations `0066_documents.sql` and `0067_credit_rules.sql`; `internal/kernel/documents.go`, `progresscredit.go`; `internal/httpapi/docs_handlers.go`, `credit_handlers.go`;
console **Documents and RFIs** and **Progress credit**. Permissions `doc.manage`, `doc.approve`, `doc.raise`, `doc.respond`, `credit.manage`, `credit.approve`,
`credit.record`; role `document_controller` (planner gains `credit.manage`); duty-conflict pairs "Document publication" and "Rules of credit"; persona `u-doc-controller`.

## Controls (database-enforced)
Files up to 10 MB of an allowed type, stored with SHA-256 which the database recomputes; stored files and hashes can never be edited; exactly one WIP and one
published revision per document; the uploader cannot publish; publishing supersedes the previous revision; work in progress is invisible to the site; every download is
audited; transmittals list only published revisions and never change; an RFI cannot be answered by its raiser or closed by its responder, with cost and schedule impact
flags and a link to a change event of the same project; a submittal is reviewed once, by someone else, with code A to D (C and D need a note).
Rules of credit: weights add up to 100; the preparer cannot approve; one approved rule per activity; steps completed once, in order; percent is computed by
`credit_percent`; the planner turns percentages into a draft progress record (the quality hold-point guard still applies); installed quantities are insert-only and cumulative.

## Tests
`TestDocuments_RevisionsPublishTransmitRFI`, `TestCredit_RulesToProgress` (kernel) and `TestConsoleBrowserDocs` (upload through the real file input, publication by a second
person, download, RFI raised, answered and closed, an inspection plan prepared and approved, accessibility incl. 390 px dark).

## Not built
Object storage (S3) and server-side encryption (files live in the database), virus scanning, OCR and full-text search inside files (search covers number, title, discipline and type),
PDF.js and IFC viewers, BCF exchange, numbering rules per tenant, mobile capture of steps and quantities (WP-3.1).
