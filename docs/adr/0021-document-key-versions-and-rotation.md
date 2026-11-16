# ADR 0021: Document encryption key versions and rotation

Status: accepted, 2026-11-15. Extends ADR 0019.

## Context

Stored document files are encrypted by the service before they reach the object store (ADR 0019) under one master key, `SERK_DOCUMENT_KEY`. There was no way to change that key without losing access to every stored file, no record in an object of which key it needs, and a wrong key was indistinguishable from a damaged file. The independent audit of 2026-11-15 listed this as a P1 gap.

Constraint: objects are write-once at a key fixed by the database (`t/<tenant>/rev/<revision>/<sha256>`). An object cannot be rewritten in place, so a design that re-encrypts every object on rotation would need a schema change and a non-atomic delete-and-put.

## Decision

1. **Every new object names its key version.** Format `SRKB2 | id length | key id | 8 random bytes | frames`. The data key is `HKDF-SHA256(master, "serk-blob-v2|" + key id + "|" + tenant)`. The key id is 1 to 32 characters of `[A-Za-z0-9_-]`.
2. **Rotation adds a key; it does not rewrite objects.** Configuration takes the current key and its id (`SERK_DOCUMENT_KEY`, `SERK_DOCUMENT_KEY_ID`, default `d1`) and optionally one previous key and id (`SERK_DOCUMENT_KEY_PREVIOUS`, `SERK_DOCUMENT_KEY_PREVIOUS_ID`). New files are written under the current key. Files under the previous key stay readable while it is supplied.
3. **A missing key is its own error.** An object whose key id is not supplied fails with `ErrKeyUnavailable` ("object needs key d1"), never `ErrCorrupt`; the API answers 503. Nothing is modified, so supplying the key again restores access. That is the recovery path.
4. **Objects written before versions existed** (`SRKB1`, no id) are opened with whichever supplied key authenticates their first frame.
5. **Scan verdict proofs** are HMACs under the document key. A verdict recorded before a rotation is accepted when it verifies under the current or a previous key. Without the key of its time a verdict is unproven and the file is quarantined (fail closed).
6. **Download links** are signed with the current key only; a rotation invalidates outstanding links, which live two minutes.

## Procedure

Rotation, recovery and custody steps are in `docs/engineering/RUNBOOK.md` ("Document key rotation").

## Consequences and limits

- One previous key at a time through configuration. The code accepts any number; a second rotation before old files are re-encrypted needs the configuration extended, or the oldest files become unavailable (not lost) until their key is supplied.
- **No re-encryption of existing objects.** A key that is believed compromised still opens every file written under it; retiring it for good needs a re-encryption tool that writes a new object generation and a schema change to reference it. Not built. Until then rotation limits exposure of new files only.
- **Custody is still an environment secret.** Keys should come from a KMS or HSM with access logging; that needs a provider decision and credentials (audit item 12). The service never writes a key to the database, logs or objects.
- Losing a key with no copy loses the files under it. The runbook requires an offline escrow copy under dual control before a key is first used; that is a procedure for people, not something the software can enforce.

## Evidence

`TestKeyVersionsRotationAndRecovery` (pkg/blob), `TestDocSecurity_KeyRotation` (internal/kernel, PostgreSQL), `TestDocumentKeyRotationConfig` (internal/config), `TestStorageFaultsAreNotReportedAsNotFound` (internal/httpapi).
