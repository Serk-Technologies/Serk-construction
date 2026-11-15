// Document security (ADR 0019; migration 0053): where a revision's file is
// kept, how it is inspected and scanned before it is stored, the quarantine
// that keeps an unscanned or infected file from being submitted, published or
// downloaded, short-lived download tokens, and the migration of files from
// PostgreSQL to object storage.
package kernel

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/blob"
	"github.com/serk-group/serk-construction/pkg/docscan"
)

// DocumentStore says where document files go and what scans them.
type DocumentStore struct {
	Blob     *blob.Sealed    // nil: files are kept in PostgreSQL (at most MaxDocumentBytes each)
	Scanner  docscan.Scanner // nil: nothing is scanned; revisions stay NOT_SCANNED
	Key      []byte          // at least 32 bytes: scan proofs and download tokens
	MaxBytes int64           // largest accepted file with object storage; 0 = 2 GiB
	// PreviousKeys are earlier document keys (after a rotation): they prove
	// scan verdicts recorded under them. Download links are signed with Key only.
	PreviousKeys [][]byte
	TempDir      string        // where an upload is spooled while it is checked; "" = the system's
	TokenTTL     time.Duration // life of a download token; 0 = 2 minutes
}

// UseDocumentStore configures document storage and scanning.
func (s *Service) UseDocumentStore(d DocumentStore) *Service {
	s.docs = d
	return s
}

// ErrQuarantined: the file may not be submitted, published or downloaded.
var ErrQuarantined = errors.New("kernel: the file is quarantined")

// ErrDownloadToken: the download token is malformed, forged or expired.
var ErrDownloadToken = errors.New("kernel: invalid or expired download link")

func (d DocumentStore) maxBytes() int64 {
	if d.Blob == nil {
		return MaxDocumentBytes
	}
	if d.MaxBytes > 0 {
		return d.MaxBytes
	}
	return 2 << 30
}

func (d DocumentStore) mac(label string, parts ...string) (string, error) {
	return macWith(d.Key, label, parts...)
}

func macWith(key []byte, label string, parts ...string) (string, error) {
	if len(key) < 32 {
		return "", errors.New("kernel: no document key configured")
	}
	k, err := hkdf.Key(sha256.New, key, nil, "serk-doc-v1|"+label, 32)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	for _, p := range parts {
		fmt.Fprintf(m, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(m.Sum(nil)), nil
}

func (d DocumentStore) scanProof(tenant string, r DocumentRevision) (string, error) {
	return d.mac("scan", tenant, r.ID, r.SHA256, r.ScanStatus, r.ScanEngine, r.ScanDetail)
}

// releasable is the quarantine rule the kernel applies before a file moves on
// (submission, publication, download). The database applies the same rule to
// the status columns; only the kernel can check that a verdict was written by
// the API (the proof), so a CLEAN written by raw SQL does not count.
func (d DocumentStore) releasable(tenant string, required bool, r DocumentRevision) error {
	proven := false
	if r.ScanStatus == "CLEAN" || r.ScanStatus == "INFECTED" {
		// A verdict recorded before a key rotation is proven by the key of
		// its time; the next scan records it under the current key.
		for _, key := range append([][]byte{d.Key}, d.PreviousKeys...) {
			want, err := macWith(key, "scan", tenant, r.ID, r.SHA256, r.ScanStatus, r.ScanEngine, r.ScanDetail)
			if err == nil && hmac.Equal([]byte(want), []byte(r.scanProof)) {
				proven = true
				break
			}
		}
	}
	switch {
	case r.ScanStatus == "INFECTED":
		return fmt.Errorf("%w: the scanner found %s", ErrQuarantined, r.ScanDetail)
	case !required:
		return nil
	case r.ScanStatus != "CLEAN":
		return fmt.Errorf("%w: it has not been scanned clean (scan status %s) and scanning is required", ErrQuarantined, r.ScanStatus)
	case !proven:
		return fmt.Errorf("%w: its scan verdict was not recorded by the scanner", ErrQuarantined)
	}
	return nil
}

// DocumentPolicy is a tenant's document policy.
type DocumentPolicy struct {
	ScanRequired bool   `json:"scan_required"`
	QuotaBytes   *int64 `json:"quota_bytes"` // null = no ceiling
	UsedBytes    int64  `json:"used_bytes"`
	Reason       string `json:"reason,omitempty"`
	UpdatedBy    string `json:"updated_by,omitempty"`
	Default      bool   `json:"default"` // no decision has been recorded; scanning is required
	// What this deployment can do, so the policy is read with its consequences.
	ScannerConfigured bool   `json:"scanner_configured"`
	Storage           string `json:"storage"`
	MaxFileBytes      int64  `json:"max_file_bytes"`
}

func documentPolicyTx(ctx context.Context, tx pgx.Tx) (DocumentPolicy, error) {
	var p DocumentPolicy
	err := tx.QueryRow(ctx, `SELECT document_scan_required(), (SELECT quota_bytes FROM document_policies), COALESCE((SELECT reason FROM document_policies), ''),
		COALESCE((SELECT updated_by FROM document_policies), ''), NOT EXISTS (SELECT 1 FROM document_policies),
		(SELECT COALESCE(sum(size_bytes), 0)::bigint FROM document_revisions)`).Scan(&p.ScanRequired, &p.QuotaBytes, &p.Reason, &p.UpdatedBy, &p.Default, &p.UsedBytes)
	return p, err
}

// GetDocumentPolicy returns the tenant's policy and the deployment's capabilities.
func (s *Service) GetDocumentPolicy(ctx context.Context, p auth.Principal) (DocumentPolicy, error) {
	var out DocumentPolicy
	if !s.authz.Has(p, auth.PermP2PRead) && !s.authz.Has(p, auth.PermTenantSettingsManage) {
		return out, auth.ErrForbidden
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = documentPolicyTx(ctx, tx)
		return err
	})
	out.ScannerConfigured = s.docs.Scanner != nil && s.docs.Scanner.Operational()
	out.Storage, out.MaxFileBytes = "postgres", s.docs.maxBytes()
	if s.docs.Blob != nil {
		out.Storage = s.docs.Blob.Store.Kind()
	}
	return out, err
}

// SetDocumentPolicy records the tenant's decision on scanning and quota: a
// person with tenant.settings.manage, fresh MFA, attested, with a reason.
func (s *Service) SetDocumentPolicy(ctx context.Context, p auth.Principal, scanRequired bool, quota *int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if len(reason) < 10 {
		return fmt.Errorf("%w: state the reason for this policy (10+ characters)", ErrInvalidInput)
	}
	if quota != nil && *quota <= 0 {
		return fmt.Errorf("%w: a quota is a positive number of bytes, or none", ErrInvalidInput)
	}
	if err := s.authz.Require(p, auth.PermTenantSettingsManage, p.TenantID); err != nil {
		return err
	}
	if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.authorize(ctx, tx, p, "document.policy.decide", "document_policy", p.TenantID, ""); err != nil {
			return err
		}
		// Update, else insert: an upsert would fire both triggers and the attestation is used once.
		tag, err := tx.Exec(ctx, `UPDATE document_policies SET scan_required = $1, quota_bytes = $2, reason = $3, updated_by = $4`, scanRequired, quota, reason, p.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO document_policies (scan_required, quota_bytes, reason, updated_by) VALUES ($1, $2, $3, $4)`, scanRequired, quota, reason, p.UserID); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "document.policy.decide", "document_policy", p.TenantID, "document.policy_set",
			map[string]any{"scan_required": scanRequired, "quota_bytes": quota, "reason": reason})
	})
}

// AddRevisionStream uploads a DRAFT revision from a stream. The file is
// spooled to disk (bounded memory), hashed, inspected, scanned when a scanner
// is configured, and only then stored: in the object store when one is
// configured, otherwise in PostgreSQL.
func (s *Service) AddRevisionStream(ctx context.Context, p auth.Principal, documentID string, in RevisionInput, r io.Reader) (string, error) {
	in.Revision, in.FileName, in.MediaType, in.ChangeNote = strings.TrimSpace(in.Revision), strings.TrimSpace(in.FileName), strings.TrimSpace(in.MediaType), strings.TrimSpace(in.ChangeNote)
	if in.Revision == "" || in.FileName == "" || len(in.ChangeNote) < 3 {
		return "", fmt.Errorf("%w: a revision needs its revision code, a file name and a note of what changed", ErrInvalidInput)
	}
	if _, ok := docscan.Media[in.MediaType]; !ok {
		return "", fmt.Errorf("%w: files of type %q are not accepted", ErrInvalidInput, in.MediaType)
	}
	project, err := s.projectOfDocument(ctx, p, auth.PermDocumentManage, documentID)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermDocumentManage, p.TenantID, project); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(s.docs.TempDir, "serk-upload-*")
	if err != nil {
		return "", err
	}
	defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
	limit := s.docs.maxBytes()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, limit+1))
	if err != nil {
		return "", fmt.Errorf("%w: the upload was interrupted", ErrInvalidInput)
	}
	switch {
	case size == 0:
		return "", fmt.Errorf("%w: the file is empty", ErrInvalidInput)
	case size > limit:
		return "", fmt.Errorf("%w: the file is larger than %d MiB", ErrInvalidInput, limit>>20)
	}
	if err := docscan.Inspect(in.MediaType, tmp, size); err != nil {
		if errors.Is(err, docscan.ErrUnsafe) {
			s.auditRefusal(ctx, p, "document.upload.refused", "document", documentID, map[string]any{"file_name": in.FileName, "media_type": in.MediaType, "why": err.Error()})
			return "", fmt.Errorf("%w: %s", ErrInvalidInput, strings.TrimPrefix(err.Error(), docscan.ErrUnsafe.Error()+": "))
		}
		return "", err
	}
	rev := DocumentRevision{ID: uuid.NewString(), SHA256: hex.EncodeToString(h.Sum(nil)), ScanStatus: "NOT_SCANNED"}
	if s.docs.Scanner != nil {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		s.scanInto(ctx, tmp, &rev)
	}
	var objKey string
	if s.docs.Blob != nil {
		if objKey, err = blob.ObjectKey(p.TenantID, rev.ID, rev.SHA256); err != nil {
			return "", err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		if err := s.docs.Blob.Put(ctx, p.TenantID, objKey, tmp); err != nil {
			return "", fmt.Errorf("kernel: storing the file: %w", err)
		}
	}
	err = s.documentTx(ctx, p, auth.PermDocumentManage, project, func(ctx context.Context, tx pgx.Tx) error {
		// One revision in preparation at a time: checked here for a clear
		// message, and held under concurrency by the database's unique indexes.
		var docNumber string
		if err := tx.QueryRow(ctx, `SELECT number FROM documents WHERE id = $1::uuid`, documentID).Scan(&docNumber); err != nil {
			return mapNoRows(err)
		}
		var open, taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_revisions WHERE document_id = $1::uuid AND status IN ('DRAFT', 'SUBMITTED') AND scan_status <> 'INFECTED'),
			EXISTS (SELECT 1 FROM document_revisions WHERE document_id = $1::uuid AND revision = $2)`, documentID, in.Revision).Scan(&open, &taken); err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: revision %s of this document already exists", ErrInvalidInput, in.Revision)
		}
		if open && rev.ScanStatus != "INFECTED" {
			return fmt.Errorf("%w: this document already has a revision in preparation; publish or reject it first", ErrInvalidInput)
		}
		pol, err := documentPolicyTx(ctx, tx)
		if err != nil {
			return err
		}
		if pol.QuotaBytes != nil && pol.UsedBytes+size > *pol.QuotaBytes {
			return fmt.Errorf("%w: the company's document storage quota (%d MiB) would be exceeded", ErrInvalidInput, *pol.QuotaBytes>>20)
		}
		// An infected file is quarantined from its first moment (so it never
		// holds the document's one slot for a revision in preparation); any
		// other verdict is recorded after the insert.
		var proof *string
		insertScan := "NOT_SCANNED"
		if rev.ScanStatus == "INFECTED" {
			m, err := s.docs.scanProof(p.TenantID, rev)
			if err != nil {
				return err
			}
			proof, insertScan = &m, "INFECTED"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_revisions (id, document_id, number, revision, file_name, media_type, size_bytes, sha256, change_note, created_by, scan_status, scan_engine, scan_detail, scan_proof)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, CASE WHEN $11 = 'INFECTED' THEN $12 END, CASE WHEN $11 = 'INFECTED' THEN $13 END, $14)`,
			rev.ID, documentID, docNumber+"/"+in.Revision, in.Revision, in.FileName, in.MediaType, size, rev.SHA256, in.ChangeNote, p.UserID,
			insertScan, rev.ScanEngine, rev.ScanDetail, proof); err != nil {
			return err
		}
		storage := "postgres"
		if objKey != "" {
			storage = s.docs.Blob.Store.Kind()
			if _, err := tx.Exec(ctx, `INSERT INTO document_contents (revision_id, storage, object_key, object_store) VALUES ($1::uuid, 'object', $2, $3)`, rev.ID, objKey, storage); err != nil {
				return err
			}
		} else {
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return err
			}
			content, err := io.ReadAll(tmp) // at most MaxDocumentBytes
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO document_contents (revision_id, content) VALUES ($1::uuid, $2)`, rev.ID, content); err != nil {
				return err
			}
		}
		if rev.ScanStatus != "INFECTED" {
			if err := s.recordScan(ctx, tx, p.TenantID, rev); err != nil {
				return err
			}
		}
		if rev.ScanStatus == "INFECTED" {
			if err := s.notify(ctx, tx, postgres.Notification{RecipientRole: "security_admin", Kind: "security.document_infected", Severity: "critical",
				Title: fmt.Sprintf("A quarantined upload by %s: %s", p.UserID, rev.ScanDetail), Body: "The file " + in.FileName + " was uploaded as " + docNumber + "/" + in.Revision + " and is quarantined. It cannot be submitted, published or downloaded.",
				SubjectType: "document_revision", SubjectID: rev.ID, Href: "#/doc/document_revision/" + rev.ID, DedupeKey: "security:document_infected:" + rev.ID}); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "document.revision.create", "document_revision", rev.ID, "document.revision_uploaded",
			map[string]any{"revision_id": rev.ID, "document_id": documentID, "revision": in.Revision, "sha256": rev.SHA256, "size_bytes": size,
				"storage": storage, "scan_status": rev.ScanStatus, "scan_engine": rev.ScanEngine, "scan_detail": rev.ScanDetail})
	})
	if err != nil && objKey != "" {
		_ = s.docs.Blob.Delete(context.WithoutCancel(ctx), objKey)
	}
	if err != nil {
		return "", err
	}
	return rev.ID, nil
}

// scanInto scans r and writes the outcome into rev. A scanner error, or a
// verdict that cannot be proved for lack of a key, is ERROR: never CLEAN.
func (s *Service) scanInto(ctx context.Context, r io.Reader, rev *DocumentRevision) {
	v, err := s.docs.Scanner.Scan(ctx, r)
	rev.ScanEngine, rev.ScanDetail = v.Engine, ""
	switch {
	case err != nil:
		rev.ScanStatus, rev.ScanDetail = "ERROR", err.Error()
	case len(s.docs.Key) < 32:
		rev.ScanStatus, rev.ScanDetail = "ERROR", "no document key is configured, so a verdict cannot be recorded"
	case v.Clean:
		rev.ScanStatus = "CLEAN"
	default:
		rev.ScanStatus, rev.ScanDetail = "INFECTED", v.Signature
	}
	if rev.ScanEngine == "" && rev.ScanStatus != "ERROR" {
		rev.ScanEngine = "unidentified scanner"
	}
	if len(rev.ScanDetail) > 500 {
		rev.ScanDetail = rev.ScanDetail[:500]
	}
	if len(rev.ScanEngine) > 300 {
		rev.ScanEngine = rev.ScanEngine[:300]
	}
}

func (s *Service) recordScan(ctx context.Context, tx pgx.Tx, tenant string, rev DocumentRevision) error {
	if rev.ScanStatus == "NOT_SCANNED" {
		return nil
	}
	var proof *string
	if rev.ScanStatus == "CLEAN" || rev.ScanStatus == "INFECTED" {
		m, err := s.docs.scanProof(tenant, rev)
		if err != nil {
			return err
		}
		proof = &m
	}
	return postgres.Exec1(ctx, tx, `UPDATE document_revisions SET scan_status = $2, scan_engine = NULLIF($3, ''), scan_detail = NULLIF($4, ''), scan_proof = $5 WHERE id = $1::uuid`,
		rev.ID, rev.ScanStatus, rev.ScanEngine, rev.ScanDetail, proof)
}

// RescanRevision scans a stored file whose verdict is not final (uploaded
// before a scanner existed, or the scanner failed). A person who manages the
// project's documents or the company's settings may ask for it.
func (s *Service) RescanRevision(ctx context.Context, p auth.Principal, id string) (DocumentRevision, error) {
	var rev DocumentRevision
	if s.docs.Scanner == nil {
		return rev, fmt.Errorf("%w: no scanner is configured in this deployment", ErrInvalidInput)
	}
	perm := auth.PermDocumentManage
	if !s.authz.Has(p, perm) {
		perm = auth.PermTenantSettingsManage
	}
	err := s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "document_revision", id, false); err != nil {
			return err
		}
		return scanRevision(tx.QueryRow(ctx, `SELECT `+revisionCols+` FROM document_revisions v WHERE v.id = $1::uuid`, id), &rev)
	})
	if err != nil {
		return rev, mapNoRows(err)
	}
	if rev.ScanStatus == "CLEAN" || rev.ScanStatus == "INFECTED" {
		return rev, fmt.Errorf("%w: this file already has a final verdict (%s)", ErrInvalidInput, rev.ScanStatus)
	}
	rc, err := s.openContent(ctx, p.TenantID, rev)
	if err != nil {
		return rev, err
	}
	s.scanInto(ctx, rc, &rev)
	rc.Close()
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.recordScan(ctx, tx, p.TenantID, rev); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "document.scan", "document_revision", id, "document.revision_scanned",
			map[string]any{"revision_id": id, "scan_status": rev.ScanStatus, "scan_engine": rev.ScanEngine, "scan_detail": rev.ScanDetail})
	})
	return rev, err
}

// DiscardRevision lets the author dispose of their quarantined (infected) draft.
func (s *Service) DiscardRevision(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermDocumentManage, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermDocumentManage, "document_revision", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only its author discards a revision", auth.ErrForbidden)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE document_revisions SET status = 'REJECTED', decision_reason = $2 WHERE id = $1::uuid AND scan_status = 'INFECTED'`, id, strings.TrimSpace(reason)); err != nil {
			return fmt.Errorf("%w: only a quarantined revision is discarded", ErrInvalidInput)
		}
		return s.record(ctx, tx, p, "document.revision.discard", "document_revision", id, "document.revision_discarded", map[string]any{"revision_id": id, "reason": reason})
	})
}

// requireReleasable loads a revision and applies the quarantine rule.
func (s *Service) requireReleasable(ctx context.Context, tenant, id string) error {
	return s.store.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		var rev DocumentRevision
		if err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionCols+` FROM document_revisions v WHERE v.id = $1::uuid`, id), &rev); err != nil {
			return mapNoRows(err)
		}
		pol, err := documentPolicyTx(ctx, tx)
		if err != nil {
			return err
		}
		return s.docs.releasable(tenant, pol.ScanRequired, rev)
	})
}

// hashCheck fails the final read if the stream was not the attested file.
type hashCheck struct {
	io.ReadCloser
	h    hash.Hash
	want string
}

func (c *hashCheck) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.h.Write(p[:n])
	if err == io.EOF && hex.EncodeToString(c.h.Sum(nil)) != c.want {
		return n, fmt.Errorf("%w: stored file does not match its recorded hash", blob.ErrCorrupt)
	}
	return n, err
}

// openContent opens a revision's bytes wherever they are, without any
// permission or quarantine check (callers have made them).
func (s *Service) openContent(ctx context.Context, tenant string, rev DocumentRevision) (io.ReadCloser, error) {
	var storage string
	var key *string
	var content []byte
	err := s.store.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT storage, object_key FROM document_contents WHERE revision_id = $1::uuid`, rev.ID).Scan(&storage, &key); err != nil {
			return err
		}
		if storage == "postgres" {
			return tx.QueryRow(ctx, `SELECT content FROM document_contents WHERE revision_id = $1::uuid`, rev.ID).Scan(&content)
		}
		return nil
	})
	if err != nil {
		return nil, mapNoRows(err)
	}
	if storage == "postgres" {
		// Small by rule: verified whole before a byte is handed out.
		if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != rev.SHA256 {
			return nil, fmt.Errorf("kernel: stored file of revision %s does not match its recorded hash", rev.ID)
		}
		return io.NopCloser(bytes.NewReader(content)), nil
	}
	if s.docs.Blob == nil {
		return nil, errors.New("kernel: this file is in object storage and no object store is configured")
	}
	rc, err := s.docs.Blob.Get(ctx, tenant, *key)
	if err != nil {
		return nil, fmt.Errorf("kernel: opening the stored file: %w", err)
	}
	return &hashCheck{ReadCloser: rc, h: sha256.New(), want: rev.SHA256}, nil
}

// OpenRevision returns a revision's file as a stream to a reader of its
// project, if the file is not quarantined. The download counts against the
// person's daily export budget and is recorded. The stream fails rather than
// deliver bytes that are not the attested file.
func (s *Service) OpenRevision(ctx context.Context, p auth.Principal, id string) (DocumentRevision, io.ReadCloser, error) {
	rev, err := s.downloadable(ctx, p, id)
	if err != nil {
		return rev, nil, err
	}
	rc, err := s.openContent(ctx, p.TenantID, rev)
	return rev, rc, err
}

// downloadable makes every check of a download and records it.
func (s *Service) downloadable(ctx context.Context, p auth.Principal, id string) (DocumentRevision, error) {
	var rev DocumentRevision
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "document_revision", id, false); err != nil {
			return err
		}
		if err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionCols+` FROM document_revisions v WHERE v.id = $1::uuid`, id), &rev); err != nil {
			return err
		}
		pol, err := documentPolicyTx(ctx, tx)
		if err != nil {
			return err
		}
		return s.docs.releasable(p.TenantID, pol.ScanRequired, rev)
	})
	if err != nil {
		if errors.Is(err, ErrQuarantined) {
			s.auditRefusal(ctx, p, "document.download.refused", "document_revision", id, map[string]any{"why": err.Error()})
		}
		return rev, mapNoRows(err)
	}
	if err := s.RecordExport(ctx, p, "document_revision:"+id, rev.SizeBytes); err != nil {
		return rev, err
	}
	return rev, nil
}

type downloadClaims struct {
	Tenant   string `json:"t"`
	Revision string `json:"r"`
	User     string `json:"u"`
	Expires  int64  `json:"e"`
}

// IssueDownloadToken makes every check of a download now, records it, and
// returns a token that lets its holder fetch that one file for a short time
// without presenting the bearer token again (so a browser can stream a large
// file). The token names the tenant, revision and person and is signed by the
// API; it is not a credential for anything else.
func (s *Service) IssueDownloadToken(ctx context.Context, p auth.Principal, id string) (string, time.Time, error) {
	if len(s.docs.Key) < 32 {
		return "", time.Time{}, fmt.Errorf("%w: download links are not configured in this deployment", ErrInvalidInput)
	}
	if _, err := s.downloadable(ctx, p, id); err != nil {
		return "", time.Time{}, err
	}
	ttl := s.docs.TokenTTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	exp := time.Now().Add(ttl)
	body, _ := json.Marshal(downloadClaims{p.TenantID, id, p.UserID, exp.Unix()})
	b64 := base64.RawURLEncoding.EncodeToString(body)
	mac, err := s.docs.mac("download", b64)
	if err != nil {
		return "", time.Time{}, err
	}
	return b64 + "." + mac, exp, nil
}

// RedeemDownloadToken opens the file a valid, unexpired token names. The
// quarantine rule is applied again at this moment; the use is audited.
func (s *Service) RedeemDownloadToken(ctx context.Context, token string) (DocumentRevision, io.ReadCloser, error) {
	var rev DocumentRevision
	b64, mac, ok := strings.Cut(token, ".")
	want, err := s.docs.mac("download", b64)
	if !ok || err != nil || !hmac.Equal([]byte(want), []byte(mac)) {
		return rev, nil, ErrDownloadToken
	}
	var c downloadClaims
	body, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil || json.Unmarshal(body, &c) != nil || time.Now().Unix() > c.Expires {
		return rev, nil, ErrDownloadToken
	}
	err = s.store.WithTenant(ctx, c.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		if err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionCols+` FROM document_revisions v WHERE v.id = $1::uuid`, c.Revision), &rev); err != nil {
			return err
		}
		pol, err := documentPolicyTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := s.docs.releasable(c.Tenant, pol.ScanRequired, rev); err != nil {
			return err
		}
		return postgres.Audit(ctx, tx, postgres.AuditEntry{TenantID: c.Tenant, ActorID: c.User, Action: "document.download.link_used", AggregateType: "document_revision",
			AggregateID: c.Revision, RequestID: requestID(ctx), Payload: map[string]any{"expires": c.Expires}})
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rev, nil, ErrDownloadToken
		}
		return rev, nil, err
	}
	rc, err := s.openContent(ctx, c.Tenant, rev)
	return rev, rc, err
}

// StorageMigration reports one run of MigrateDocumentContents.
type StorageMigration struct {
	Copied    int      `json:"copied"`    // verified in the object store and recorded in this run
	Failed    []string `json:"failed"`    // revision ids that could not be copied or verified (left untouched)
	Remaining int      `json:"remaining"` // files still only in PostgreSQL
	Awaiting  int      `json:"awaiting_release"`
}

// MigrateDocumentContents copies up to limit files that are only in
// PostgreSQL into the object store. Each file is written, read back and
// compared with its recorded hash before its key is recorded; the bytes stay
// in PostgreSQL until the schema owner releases them. Safe to run again: a
// file is handled once, a failure leaves it as it was.
func (s *Service) MigrateDocumentContents(ctx context.Context, p auth.Principal, limit int) (StorageMigration, error) {
	out := StorageMigration{Failed: []string{}}
	if err := s.authz.Require(p, auth.PermTenantSettingsManage, p.TenantID); err != nil {
		return out, err
	}
	if s.docs.Blob == nil {
		return out, fmt.Errorf("%w: no object store is configured in this deployment", ErrInvalidInput)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	type row struct{ id, sha string }
	var rows []row
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT v.id::text, v.sha256 FROM document_contents c JOIN document_revisions v ON v.id = c.revision_id
			WHERE c.storage = 'postgres' AND c.object_key IS NULL ORDER BY v.created_at, v.id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var x row
			if err := r.Scan(&x.id, &x.sha); err != nil {
				return err
			}
			rows = append(rows, x)
		}
		return r.Err()
	})
	if err != nil {
		return out, err
	}
	for _, x := range rows {
		if err := s.migrateOne(ctx, p.TenantID, x.id, x.sha); err != nil {
			out.Failed = append(out.Failed, x.id)
			continue
		}
		out.Copied++
	}
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE storage = 'postgres' AND object_key IS NULL), count(*) FILTER (WHERE storage = 'postgres' AND object_key IS NOT NULL)
			FROM document_contents`).Scan(&out.Remaining, &out.Awaiting); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "document.storage.migrate", "document_policy", p.TenantID, "document.storage_migrated",
			map[string]any{"copied": out.Copied, "failed": out.Failed, "remaining": out.Remaining})
	})
	return out, err
}

func (s *Service) migrateOne(ctx context.Context, tenant, id, sha string) error {
	key, err := blob.ObjectKey(tenant, id, sha)
	if err != nil {
		return err
	}
	var content []byte
	if err := s.store.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT content FROM document_contents WHERE revision_id = $1::uuid AND storage = 'postgres'`, id).Scan(&content)
	}); err != nil {
		return err
	}
	if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != sha {
		return errors.New("kernel: the file in the database does not match its recorded hash")
	}
	// An object left by an interrupted earlier run is verified like a new one.
	if err := s.docs.Blob.Put(ctx, tenant, key, bytes.NewReader(content)); err != nil && !errors.Is(err, blob.ErrExists) {
		return err
	}
	rc, err := s.docs.Blob.Get(ctx, tenant, key)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, rc)
	rc.Close()
	if err != nil || hex.EncodeToString(h.Sum(nil)) != sha {
		return errors.New("kernel: the copy in the object store does not read back as the file")
	}
	return s.store.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		return postgres.Exec1(ctx, tx, `UPDATE document_contents SET object_key = $2, object_store = $3 WHERE revision_id = $1::uuid AND storage = 'postgres' AND object_key IS NULL`,
			id, key, s.docs.Blob.Store.Kind())
	})
}
