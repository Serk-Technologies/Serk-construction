# ADR 0018: controlled documents, revisions and RFIs; files held in PostgreSQL for now

Status: accepted by the delivering team (2026-11-15); awaiting human review. Migration 0052. Requirements CNX-01, CNX-02, CNX-03 (`docs/engineering/REQUIREMENTS.md`).

## Context

The Master Engineering Bible (section "Common data environment") requires document control with immutable revisions, controlled states, hashed uploads and secure sharing. `docs/BACKLOG.md` listed document control as a missing module of high severity. The repository has no object storage, no malware scanner and no pre-signed URL service, and none can be operated or verified in the build environment.

## Decision

**A document is a register entry; its revisions carry the file.**
- `documents`: number, the project's own reference (unique per project), type and title. Never updated: the application role has no UPDATE or DELETE on it.
- `document_revisions`: revision code, file name, media type, size, SHA-256 and a change note. Only status columns ever change.
- `document_contents`: the bytes, one row per revision, written once in the transaction that creates the revision. No UPDATE or DELETE is granted.

**The database owns the hash.** A trigger computes SHA-256 and size of the stored bytes and refuses a file that differs from what its revision declares. A deferred constraint refuses a revision committed without its file. The approval attestation covers the revision row, so the hash an approver signs is the hash of the stored bytes. The kernel re-hashes on download and refuses to serve bytes that differ.

**States.** The Bible's states map to the repository's document statuses: work in progress = `DRAFT`, shared = `SUBMITTED`, published = `APPROVED`, archived = `SUPERSEDED` (plus `REJECTED`). Reusing the existing statuses lets revisions use the generic document list, detail, worklist and approval code.

**Publication is maker/checker with fresh MFA, human only, attested** (`document.manage` uploads and submits; `document.approve` publishes; the pair is a separation-of-duties conflict). Publishing supersedes the previous current revision in the same transaction. The database allows the supersession only while an attested publication of another revision of the same document is in that transaction, and checks at commit that a published successor exists. A partial unique index allows one `APPROVED` revision per document, and another allows one revision in preparation.

**Files are restricted, not scanned.**
- An allow-list of media types (PDF, PNG, JPEG, text, CSV, DOCX, XLSX, DWG, DXF, IFC), enforced in the kernel and by a CHECK constraint.
- The leading bytes must match the declared type.
- 10 MiB per file, enforced in the kernel and in the database. This is a storage protection of this profile, not a limit on the number of documents or revisions.
- Files are only ever served as `Content-Disposition: attachment` with `nosniff`, with the revision's status in a response header. The console never renders them.
- Every download is recorded in `data_access_events` and counts against the person's daily export budget.

**RFIs.** Raised on a project, optionally about one revision. Answered once by a person other than the author; the answer is an attested human decision (`rfi.answer` is not a draft action, so an AI principal is refused in Go and in SQL). Closed by the author; an unanswered RFI is withdrawn with a reason.

## Alternatives considered

- **Object storage (S3-compatible) with pre-signed URLs.** The target design. Not chosen now: it adds a service that cannot be run or tested here, and a second place where tenant isolation must be proven. PostgreSQL keeps files under the same row-level security, backup and point-in-time recovery as the rest of the tenant's data.
- **File bytes in the revision row.** Rejected: every read and every approval of a revision would carry the file through `to_jsonb`.
- **Metadata only, files elsewhere.** Rejected: a register whose hashes the system cannot verify is not document control.

## Consequences and limits

- Database size grows with files. Backups and WAL carry them. `docs/engineering/CAPACITY_PLAN.md` states the measure to watch.
- No malware scanning or quarantine (requirement CNX-01.4, BLOCKED). Do not enable document upload for real users on an internet-facing deployment until a scanner is in the upload path or the risk is accepted in writing.
- No resumable upload; the upload is one JSON request of at most about 14.3 MiB.
- No transmittals, mark-ups, retention policy or legal hold yet.

## Reversal / migration plan

Move bytes to object storage without changing the contract: add `storage_ref` to `document_contents`, copy each object and verify it against `document_revisions.sha256`, switch `DownloadRevision` to read by reference, then drop `content` in a later migration. Revision ids, numbers, hashes and attestations are unaffected.
