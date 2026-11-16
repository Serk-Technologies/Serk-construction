package kernel

// Tests for document security (ADR 0019; migration 0053; requirements
// CNX-01.4, CNX-01.5, CNX-01.7 onwards). The scanner in these tests is a
// labelled test double; the real ClamAV client is tested in pkg/docscan and
// the S3 client in pkg/blob against real servers.
// Proven: fail-closed quarantine (unscanned, scanner error, infected) at
// submission, publication and download, in the kernel and in the database; a
// verdict forged by raw SQL does not count; the scan policy is changed only by
// an authorised person with fresh MFA; files over the PostgreSQL cap go to an
// encrypted object store under a key the database fixes; a tampered object is
// not served; the two-step migration keeps every byte until the owner
// releases it; download links are short-lived, bound and re-checked; the
// export budget covers downloads and links; the storage quota holds.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/internal/testdb"
	"github.com/serk-group/serk-construction/pkg/attest"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/blob"
	"github.com/serk-group/serk-construction/pkg/docscan"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

func (w *docWorld) svcWith(d DocumentStore) *Service {
	return New(w.f.app, auth.NewAuthorizer(nil, true)).UseApprovalSigner(attest.Signer{Master: testdb.ApprovalMaster}).UseBankDataKeys(testdb.BankDataKeys).UseDocumentStore(d)
}

func (w *docWorld) secAdmin(fresh bool) auth.Principal {
	p := auth.Principal{UserID: "u-sec-admin", TenantID: w.f.tenant, Roles: []string{"security_admin"}, IssuedAt: time.Now().Add(-time.Minute), Kind: auth.KindHuman}
	if fresh {
		p.MFA, p.AuthTime = true, time.Now()
	}
	return p
}

func (w *docWorld) doc(t *testing.T, ref string) string {
	t.Helper()
	id, err := w.f.svc.CreateDocument(w.f.ctx, w.eng, DocumentInput{ProjectID: w.f.project, Reference: ref, DocType: "REPORT", Title: "Report " + ref})
	must(t, "document "+ref, err)
	return id
}

func txt(svc *Service, w *docWorld, doc, rev, body string) (string, error) {
	return svc.AddRevision(w.f.ctx, w.eng, doc, RevisionInput{Revision: rev, FileName: "note-" + rev + ".txt", MediaType: "text/plain", ChangeNote: "Site note " + rev, Content: []byte(body)})
}

func TestDocSecurity_FailClosedQuarantine(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	noScanner := w.svcWith(DocumentStore{Key: testdb.DocumentKey})
	doc := w.doc(t, "R-1")

	// No scanner: the file is stored but goes nowhere while scanning is required (the default).
	rev, err := txt(noScanner, w, doc, "A", "an ordinary note")
	must(t, "upload without a scanner", err)
	d, err := f.svc.GetDocument(f.ctx, w.eng, doc)
	must(t, "get", err)
	if d.Revisions[0].ScanStatus != "NOT_SCANNED" {
		t.Fatalf("scan status %s", d.Revisions[0].ScanStatus)
	}
	if err := noScanner.SubmitRevision(f.ctx, w.eng, rev); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("an unscanned revision was submitted: %v", err)
	}
	if _, err := noScanner.DownloadRevision(f.ctx, w.eng, rev); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("an unscanned revision was downloaded: %v", err)
	}
	if _, _, err := noScanner.IssueDownloadToken(f.ctx, w.eng, rev); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a download link was issued for an unscanned revision: %v", err)
	}
	// A verdict written by raw SQL is accepted by the database's shape rules but does not count: no proof.
	forged := strings.Repeat("ab", 32)
	must(t, "raw verdict", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE document_revisions SET scan_status = 'CLEAN', scan_engine = 'forged', scan_proof = $2 WHERE id = $1::uuid`, rev, forged)
		return err
	}))
	if err := f.svc.SubmitRevision(f.ctx, w.eng, rev); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a forged verdict let a revision through: %v", err)
	}
	if _, err := f.svc.DownloadRevision(f.ctx, w.eng, rev); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a forged verdict let a download through: %v", err)
	}
	rawFails(t, f, "overturn a verdict", `UPDATE document_revisions SET scan_status = 'ERROR', scan_proof = NULL WHERE id = '`+rev+`'`)
	rawFails(t, f, "change a file's hash while recording a scan", `UPDATE document_revisions SET scan_status = 'PENDING', sha256 = '`+forged+`' WHERE id = '`+rev+`'`)

	// The policy: only a person with tenant.settings.manage, fresh MFA, a reason; never raw SQL.
	if err := f.svc.SetDocumentPolicy(f.ctx, w.pmFresh, false, nil, "Scanner procurement pending"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager changed the scan policy: %v", err)
	}
	if err := f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(false), false, nil, "Scanner procurement pending"); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("the scan policy was changed without fresh MFA: %v", err)
	}
	if err := f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(true), false, nil, "no"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("the scan policy was changed without a reason: %v", err)
	}
	ai := w.secAdmin(true)
	ai.Kind = auth.KindAI
	if err := f.svc.SetDocumentPolicy(f.ctx, ai, false, nil, "Scanner procurement pending"); err == nil {
		t.Fatal("an AI principal changed the scan policy")
	}
	rawFails(t, f, "turn scanning off by SQL", `INSERT INTO document_policies (scan_required, reason, updated_by) VALUES (false, 'forged decision', 'u-forger')`)
	pol, err := f.svc.GetDocumentPolicy(f.ctx, w.eng)
	must(t, "policy", err)
	if !pol.ScanRequired || !pol.Default || pol.ScannerConfigured {
		t.Fatalf("default policy: %+v (the test double must not count as a configured scanner)", pol)
	}

	// A second document: unscanned, then the policy is relaxed by the right person.
	doc2 := w.doc(t, "R-2")
	rev2, err := txt(noScanner, w, doc2, "A", "another note")
	must(t, "upload 2", err)
	rawFails(t, f, "submit an unscanned revision by SQL", `UPDATE document_revisions SET status = 'SUBMITTED' WHERE id = '`+rev2+`'`)
	must(t, "relax policy", f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(true), false, nil, "No scanner on the pilot site until November"))
	must(t, "submit unscanned under the relaxed policy", noScanner.SubmitRevision(f.ctx, w.eng, rev2))
	must(t, "publish unscanned under the relaxed policy", noScanner.DecideRevision(f.ctx, w.pmFresh, rev2, true, ""))
	if _, err := noScanner.DownloadRevision(f.ctx, w.planner, rev2); err != nil {
		t.Fatalf("download under the relaxed policy: %v", err)
	}
	// Requiring scans again quarantines the published, unscanned file until it is scanned.
	must(t, "require scans", f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(true), true, nil, "Scanner installed and verified"))
	if _, err := f.svc.DownloadRevision(f.ctx, w.planner, rev2); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a published unscanned file was downloaded while scanning is required: %v", err)
	}
	if _, err := noScanner.RescanRevision(f.ctx, w.eng, rev2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rescan without a scanner: %v", err)
	}
	if _, err := f.svc.RescanRevision(f.ctx, w.outsideEng, rev2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rescan from another project: %v", err)
	}
	scanned, err := f.svc.RescanRevision(f.ctx, w.eng, rev2)
	must(t, "rescan", err)
	if scanned.ScanStatus != "CLEAN" {
		t.Fatalf("rescan: %+v", scanned)
	}
	if _, err := f.svc.DownloadRevision(f.ctx, w.planner, rev2); err != nil {
		t.Fatalf("download after a clean rescan: %v", err)
	}
	if _, err := f.svc.RescanRevision(f.ctx, w.eng, rev2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a final verdict was rescanned: %v", err)
	}

	// Scanner outage: ERROR, quarantined, then rescanned once the scanner is back.
	doc3 := w.doc(t, "R-3")
	rev3, err := txt(f.svc, w, doc3, "A", "note written during SCANNER-DOWN")
	must(t, "upload during an outage", err)
	if err := f.svc.SubmitRevision(f.ctx, w.eng, rev3); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a revision whose scan failed was submitted: %v", err)
	}
	if d, _ := f.svc.GetDocument(f.ctx, w.eng, doc3); d.Revisions[0].ScanStatus != "ERROR" || d.Revisions[0].ScanDetail == "" {
		t.Fatalf("after an outage: %+v", d.Revisions[0])
	}
	// A verdict cannot be recorded without the key that proves it.
	noKey := w.svcWith(DocumentStore{Scanner: testdb.FakeScanner{}})
	doc4 := w.doc(t, "R-4")
	rev4, err := txt(noKey, w, doc4, "A", "note scanned with no key configured")
	must(t, "upload with no key", err)
	if d, _ := f.svc.GetDocument(f.ctx, w.eng, doc4); d.Revisions[0].ScanStatus != "ERROR" {
		t.Fatalf("a verdict was recorded without a key: %+v", d.Revisions[0])
	}
	if got, err := f.svc.RescanRevision(f.ctx, w.eng, rev4); err != nil || got.ScanStatus != "CLEAN" {
		t.Fatalf("rescan with the key: %+v %v", got, err)
	}
}

func TestDocSecurity_InfectedAndMaliciousUploads(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	doc := w.doc(t, "M-1")

	// Refused before storage: wrong type, disguise, active content, archive bomb.
	for name, in := range map[string]RevisionInput{
		"svg":                   {MediaType: "image/svg+xml", Content: []byte("<svg onload=alert(1)/>")},
		"svg disguised as text": {MediaType: "text/plain", Content: []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")},
		"executable as pdf":     {MediaType: "application/pdf", Content: []byte("MZ\x90\x00\x03")},
		"pdf with javascript":   {MediaType: "application/pdf", Content: []byte("%PDF-1.7\n<< /S /JavaScript /JS (app.alert(1)) >>")},
		"zip as docx":           {MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Content: []byte("PK\x03\x04 not a real archive")},
	} {
		in.Revision, in.FileName, in.ChangeNote = "A", "x.bin", "malicious upload attempt"
		if _, err := f.svc.AddRevision(f.ctx, w.eng, doc, in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
	for _, bad := range []string{"../../etc/passwd", "a/b.txt", "..\\x.txt", ".hidden"} {
		_, err := f.svc.AddRevision(f.ctx, w.eng, doc, RevisionInput{Revision: "A", FileName: bad, MediaType: "text/plain", ChangeNote: "path traversal attempt", Content: []byte("x")})
		if err == nil {
			t.Fatalf("file name %q was accepted", bad)
		}
	}
	var refusals int
	must(t, "refusals audited", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'document.upload.refused'`).Scan(&refusals)
	}))
	if refusals != 4 {
		must(t, "list", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			rows, _ := tx.Query(ctx, `SELECT payload::text FROM audit_log WHERE action = 'document.upload.refused'`)
			defer rows.Close()
			for rows.Next() {
				var p string
				_ = rows.Scan(&p)
				t.Log(p)
			}
			return nil
		}))
		t.Fatalf("%d refused uploads were audited, want 4 (the unlisted type is refused before inspection)", refusals)
	}

	// Infected (the EICAR test file): stored in quarantine, security told, nothing moves, whatever the policy.
	rev, err := txt(f.svc, w, doc, "A", testdb.EICAR)
	must(t, "infected upload is recorded", err)
	d, err := f.svc.GetDocument(f.ctx, w.eng, doc)
	must(t, "get", err)
	if r := d.Revisions[0]; r.ScanStatus != "INFECTED" || r.ScanDetail != "Test.EICAR" || d.InReview != 1 {
		t.Fatalf("infected revision: %+v", r)
	}
	var told int
	must(t, "notification", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'security.document_infected' AND subject_id = $1`, rev).Scan(&told)
	}))
	if told != 1 {
		t.Fatalf("security notifications: %d", told)
	}
	must(t, "relax policy", f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(true), false, nil, "Testing that infected files stay quarantined"))
	if err := f.svc.SubmitRevision(f.ctx, w.eng, rev); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("an infected revision was submitted: %v", err)
	}
	if _, err := f.svc.DownloadRevision(f.ctx, w.eng, rev); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("an infected revision was downloaded: %v", err)
	}
	rawFails(t, f, "clear an infected verdict", `UPDATE document_revisions SET scan_status = 'CLEAN' WHERE id = '`+rev+`'`)
	rawFails(t, f, "submit an infected revision by SQL", `UPDATE document_revisions SET status = 'SUBMITTED' WHERE id = '`+rev+`'`)
	if _, err := f.svc.RescanRevision(f.ctx, w.eng, rev); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an infected verdict was rescanned: %v", err)
	}
	var refused int
	must(t, "refused downloads audited", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'document.download.refused' AND aggregate_id = $1`, rev).Scan(&refused)
	}))
	if refused != 1 {
		t.Fatalf("refused downloads audited: %d", refused)
	}
	// It does not block the document: a clean revision can be uploaded beside it, and the author discards it.
	clean, err := txt(f.svc, w, doc, "B", "the clean note")
	must(t, "clean revision beside a quarantined one", err)
	if err := f.svc.DiscardRevision(f.ctx, w.planner, rev, "Infected upload"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("someone else discarded the author's revision: %v", err)
	}
	if err := f.svc.DiscardRevision(f.ctx, w.eng, clean, "Not infected"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a clean draft was discarded: %v", err)
	}
	must(t, "discard", f.svc.DiscardRevision(f.ctx, w.eng, rev, "Infected upload, replaced by revision B"))
	rawFails(t, f, "revive a discarded revision", `UPDATE document_revisions SET status = 'DRAFT' WHERE id = '`+rev+`'`)

	// A decided or superseded revision cannot be published again.
	must(t, "submit B", f.svc.SubmitRevision(f.ctx, w.eng, clean))
	must(t, "publish B", f.svc.DecideRevision(f.ctx, w.pmFresh, clean, true, ""))
	next, err := txt(f.svc, w, doc, "C", "the next note")
	must(t, "revision C", err)
	must(t, "submit C", f.svc.SubmitRevision(f.ctx, w.eng, next))
	must(t, "publish C", f.svc.DecideRevision(f.ctx, w.pmFresh, next, true, ""))
	for name, id := range map[string]string{"superseded": clean, "current": next, "discarded": rev} {
		if err := f.svc.DecideRevision(f.ctx, w.pmFresh, id, true, ""); !errors.Is(err, ledger.ErrBadTransition) && !errors.Is(err, ErrQuarantined) {
			t.Fatalf("a %s revision was published again: %v", name, err)
		}
	}
}

func TestDocSecurity_ObjectStorageAndMigration(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	dir := t.TempDir()
	sealed := &blob.Sealed{Store: blob.FS{Root: dir}, Master: testdb.DocumentKey}
	obj := w.svcWith(DocumentStore{Blob: sealed, Scanner: testdb.FakeScanner{}, Key: testdb.DocumentKey, MaxBytes: 32 << 20})
	doc := w.doc(t, "B-1")

	// 12 MiB: over the PostgreSQL cap. Refused without an object store, streamed into one with it.
	big := append(pdf("large model export\n"), bytes.Repeat([]byte("0123456789abcdef"), 12<<16)...)
	in := RevisionInput{Revision: "A", FileName: "model.pdf", MediaType: "application/pdf", ChangeNote: "Large file"}
	if _, err := f.svc.AddRevisionStream(f.ctx, w.eng, doc, in, bytes.NewReader(big)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a 12 MiB file was accepted without object storage: %v", err)
	}
	if _, err := obj.AddRevisionStream(f.ctx, w.eng, doc, in, io.LimitReader(neverEnding('x'), 33<<20)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a file over the deployment's limit was accepted: %v", err)
	}
	rev, err := obj.AddRevisionStream(f.ctx, w.eng, doc, in, bytes.NewReader(big))
	must(t, "streamed upload", err)
	sum := sha256.Sum256(big)
	key, _ := blob.ObjectKey(f.tenant, rev, hex.EncodeToString(sum[:]))
	var storage, gotKey string
	var inDB bool
	must(t, "contents row", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT storage, object_key, content IS NOT NULL FROM document_contents WHERE revision_id = $1::uuid`, rev).Scan(&storage, &gotKey, &inDB)
	}))
	if storage != "object" || gotKey != key || inDB {
		t.Fatalf("stored as %s key %s (bytes in the database: %v)", storage, gotKey, inDB)
	}
	path := filepath.Join(dir, filepath.FromSlash(key))
	raw, err := os.ReadFile(path)
	must(t, "object on disk", err)
	if bytes.Contains(raw, []byte("large model export")) || bytes.Contains(raw, []byte("0123456789abcdef0123456789abcdef")) {
		t.Fatal("the object store holds plaintext")
	}
	rawFails(t, f, "repoint an object reference", `UPDATE document_contents SET object_key = 't/`+f.tenant2+`/rev/`+rev+`/`+hex.EncodeToString(sum[:])+`' WHERE revision_id = '`+rev+`'`)
	rawFails(t, f, "pull an object back into the row", `UPDATE document_contents SET object_verified_at = now() WHERE revision_id = '`+rev+`'`)
	meta, rc, err := obj.OpenRevision(f.ctx, w.planner, rev)
	must(t, "open", err)
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, big) || meta.SizeBytes != int64(len(big)) {
		t.Fatalf("streamed download: %v (%d bytes)", err, len(got))
	}
	// Without the object store configured the file cannot be served; nothing else is substituted.
	if _, err := f.svc.DownloadRevision(f.ctx, w.planner, rev); err == nil {
		t.Fatal("an object-stored file was served by a service with no object store")
	}
	// A tampered object is not served.
	tampered := bytes.Clone(raw)
	tampered[len(tampered)/2] ^= 1
	must(t, "tamper", os.WriteFile(path, tampered, 0o600))
	if _, err := obj.DownloadRevision(f.ctx, w.planner, rev); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("a tampered object was served: %v", err)
	}
	must(t, "restore", os.WriteFile(path, raw, 0o600))

	// Migration of files that are in PostgreSQL.
	doc2 := w.doc(t, "B-2")
	small := pdf("kept in the database first")
	oldRev, err := f.svc.AddRevision(f.ctx, w.eng, doc2, RevisionInput{Revision: "A", FileName: "small.pdf", MediaType: "application/pdf", ChangeNote: "Before object storage", Content: small})
	must(t, "upload to postgres", err)
	must(t, "submit", f.svc.SubmitRevision(f.ctx, w.eng, oldRev))
	must(t, "publish", f.svc.DecideRevision(f.ctx, w.pmFresh, oldRev, true, ""))
	before, err := f.svc.GetDocument(f.ctx, w.eng, doc2)
	must(t, "before", err)
	if _, err := obj.MigrateDocumentContents(f.ctx, w.eng, 10); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer ran the storage migration: %v", err)
	}
	if _, err := f.svc.MigrateDocumentContents(f.ctx, w.secAdmin(false), 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("migration without an object store: %v", err)
	}
	// A store that cannot be read back: the file is reported failed and left exactly as it was.
	broken := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: writeOnly{blob.FS{Root: t.TempDir()}}, Master: testdb.DocumentKey}, Key: testdb.DocumentKey})
	res, err := broken.MigrateDocumentContents(f.ctx, w.secAdmin(false), 10)
	must(t, "migration against a broken store", err)
	if res.Copied != 0 || len(res.Failed) != 1 || res.Failed[0] != oldRev || res.Remaining != 1 {
		t.Fatalf("broken store: %+v", res)
	}
	res, err = obj.MigrateDocumentContents(f.ctx, w.secAdmin(false), 10)
	must(t, "migration", err)
	if res.Copied != 1 || len(res.Failed) != 0 || res.Remaining != 0 || res.Awaiting != 1 {
		t.Fatalf("migration: %+v", res)
	}
	if res, err = obj.MigrateDocumentContents(f.ctx, w.secAdmin(false), 10); err != nil || res.Copied != 0 || res.Awaiting != 1 {
		t.Fatalf("second run: %+v %v", res, err)
	}
	// The bytes are still in the database; no tenant login can remove them.
	rawFails(t, f, "release a migrated file as the tenant", `UPDATE document_contents SET storage = 'object', content = NULL WHERE revision_id = '`+oldRev+`'`)
	rawFails(t, f, "record a copy that was not verified", `UPDATE document_contents SET object_key = 't/x' WHERE revision_id = '`+rev+`'`)
	if file, err := f.svc.DownloadRevision(f.ctx, w.planner, oldRev); err != nil || !bytes.Equal(file.Content, small) {
		t.Fatalf("download between the two steps: %v", err)
	}
	// Step 2: the schema owner releases the bytes.
	n, err := postgres.ReleaseMigratedDocumentContents(f.ctx, f.admin, 0)
	must(t, "release", err)
	if n != 1 {
		t.Fatalf("released %d files", n)
	}
	file, err := obj.DownloadRevision(f.ctx, w.planner, oldRev)
	if err != nil || !bytes.Equal(file.Content, small) {
		t.Fatalf("download after release: %v", err)
	}
	after, err := f.svc.GetDocument(f.ctx, w.eng, doc2)
	must(t, "after", err)
	b, a := before.Revisions[0], after.Revisions[0]
	if a.SHA256 != b.SHA256 || a.Number != b.Number || a.Status != "APPROVED" || a.ApprovedBy != b.ApprovedBy || a.ScanStatus != b.ScanStatus || !a.CreatedAt.Equal(b.CreatedAt) {
		t.Fatalf("the revision changed during migration:\n%+v\n%+v", b, a)
	}
	var audit int
	must(t, "audit", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE aggregate_type = 'document_revision' AND aggregate_id = $1`, oldRev).Scan(&audit)
	}))
	if audit < 3 {
		t.Fatalf("audit entries of the migrated revision: %d", audit)
	}
	if n, err := postgres.ReleaseMigratedDocumentContents(f.ctx, f.admin, 0); err != nil || n != 0 {
		t.Fatalf("second release: %d %v", n, err)
	}

	// Quota.
	one := int64(64)
	must(t, "quota", f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(true), true, &one, "Pilot quota for the storage test"))
	if _, err := txt(obj, w, w.doc(t, "B-3"), "A", "over the quota"); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("an upload over the quota was accepted: %v", err)
	}
	var orphans int
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			orphans++
		}
		return nil
	})
	if orphans != 2 {
		t.Fatalf("%d objects in the store, want 2 (a refused upload must not leave an object behind)", orphans)
	}
}

type neverEnding byte

func (b neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// writeOnly is a store whose objects cannot be read back.
type writeOnly struct{ blob.FS }

func (writeOnly) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, errors.New("simulated read failure")
}

func TestDocSecurity_DownloadLinksAndExportBudget(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	doc := w.doc(t, "L-1")
	rev, err := txt(f.svc, w, doc, "A", "a note to download")
	must(t, "upload", err)

	tok, exp, err := f.svc.IssueDownloadToken(f.ctx, w.planner, rev)
	must(t, "issue", err)
	if d := time.Until(exp); d <= 0 || d > 2*time.Minute+time.Second {
		t.Fatalf("link lifetime %s", d)
	}
	meta, rc, err := f.svc.RedeemDownloadToken(f.ctx, tok)
	must(t, "redeem", err)
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "a note to download" || meta.ID != rev {
		t.Fatalf("redeemed %q", got)
	}
	// Not for people outside the project; not forgeable; not transferable to another file; not after expiry.
	if _, _, err := f.svc.IssueDownloadToken(f.ctx, w.outsideEng, rev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a link was issued outside the project: %v", err)
	}
	if _, _, err := f.svc.IssueDownloadToken(f.ctx, f.outsider, rev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a link was issued to another tenant: %v", err)
	}
	body, mac, _ := strings.Cut(tok, ".")
	for name, bad := range map[string]string{"empty": "", "no signature": body, "flipped signature": body + "." + strings.Repeat("0", len(mac)), "another payload": "e30." + mac,
		"signed with another key": body + "." + otherKeyMAC(t, body)} {
		if _, _, err := f.svc.RedeemDownloadToken(f.ctx, bad); !errors.Is(err, ErrDownloadToken) {
			t.Fatalf("%s link was honoured: %v", name, err)
		}
	}
	short := w.svcWith(DocumentStore{Key: testdb.DocumentKey, Scanner: testdb.FakeScanner{}, TokenTTL: time.Millisecond})
	old, _, err := short.IssueDownloadToken(f.ctx, w.planner, rev)
	must(t, "short link", err)
	time.Sleep(1100 * time.Millisecond)
	if _, _, err := f.svc.RedeemDownloadToken(f.ctx, old); !errors.Is(err, ErrDownloadToken) {
		t.Fatalf("an expired link was honoured: %v", err)
	}
	var used int
	must(t, "link use audited", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'document.download.link_used' AND aggregate_id = $1 AND actor_id = $2`, rev, w.planner.UserID).Scan(&used)
	}))
	if used != 1 {
		t.Fatalf("link uses audited: %d", used)
	}

	// The export budget covers downloads and links alike: 3 a day here, 3 already spent.
	_, err = f.svc.SaveTenantSettings(f.ctx, w.secAdmin(true), postgres.TenantSettings{DisplayName: "Budget Co", ShortCode: "budget", Country: "ET", Timezone: "UTC", Locale: "en", ExportDailyLimit: 3})
	must(t, "settings", err)
	if _, err := f.svc.DownloadRevision(f.ctx, w.planner, rev); err != nil {
		t.Fatalf("third export of the day: %v", err)
	}
	if _, err := f.svc.DownloadRevision(f.ctx, w.planner, rev); !errors.Is(err, ErrExportBudget) {
		t.Fatalf("download over the budget: %v", err)
	}
	if _, _, err := f.svc.IssueDownloadToken(f.ctx, w.planner, rev); !errors.Is(err, ErrExportBudget) {
		t.Fatalf("a link was issued over the budget: %v", err)
	}
	if _, _, err := f.svc.OpenRevision(f.ctx, w.planner, rev); !errors.Is(err, ErrExportBudget) {
		t.Fatalf("a stream was opened over the budget: %v", err)
	}
	// Another person's budget is their own.
	if _, err := f.svc.DownloadRevision(f.ctx, w.eng, rev); err != nil {
		t.Fatalf("another person's download: %v", err)
	}
	// A link issued before the file was quarantined dies with the quarantine.
	doc2 := w.doc(t, "L-2")
	noScanner := w.svcWith(DocumentStore{Key: testdb.DocumentKey})
	must(t, "relax", f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(true), false, nil, "Issue a link for an unscanned file"))
	rev2, err := txt(noScanner, w, doc2, "A", "unscanned note")
	must(t, "unscanned upload", err)
	link, _, err := noScanner.IssueDownloadToken(f.ctx, w.eng, rev2)
	must(t, "link under the relaxed policy", err)
	must(t, "require", f.svc.SetDocumentPolicy(f.ctx, w.secAdmin(true), true, nil, "Scanning is required again"))
	if _, _, err := f.svc.RedeemDownloadToken(f.ctx, link); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a link outlived the quarantine: %v", err)
	}
}

func otherKeyMAC(t *testing.T, body string) string {
	t.Helper()
	m, err := DocumentStore{Key: []byte(strings.Repeat("k", 32))}.mac("download", body)
	must(t, "mac", err)
	return m
}

// TestDocSecurity_RealScannerAndObjectStore runs the upload and download path
// against a real ClamAV daemon (SERK_TEST_CLAMD) and a real S3-compatible
// server (SERK_TEST_S3_ENDPOINT, _ACCESS_KEY, _SECRET_KEY, _BUCKET). Without
// them it is skipped, and the integration is NOT verified by that run.
func TestDocSecurity_RealScannerAndObjectStore(t *testing.T) {
	clamd, ep := os.Getenv("SERK_TEST_CLAMD"), os.Getenv("SERK_TEST_S3_ENDPOINT")
	if clamd == "" || ep == "" {
		t.Skip("SERK_TEST_CLAMD and SERK_TEST_S3_ENDPOINT are not both set: the real scanner and object store were not exercised")
	}
	w := newDocWorld(t)
	f := w.f
	scanner := docscan.Clamd{Network: "tcp", Address: clamd}
	s3 := &blob.S3{Endpoint: ep, Region: "us-east-1", Bucket: os.Getenv("SERK_TEST_S3_BUCKET"), AccessKey: os.Getenv("SERK_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("SERK_TEST_S3_SECRET_KEY"), PartSize: 5 << 20}
	// The bucket is provisioned before the suite (tools/ci/s3provision); this
	// test neither creates it nor relies on another test having done so.
	if err := s3.CheckBucket(f.ctx); err != nil {
		t.Fatalf("the test bucket must be provisioned before the suite (go run ./tools/ci/s3provision): %v", err)
	}
	// An upload into a bucket that does not exist is reported as that, not as a missing file.
	gone := *s3
	gone.Bucket = "serk-absent-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	noBucket := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: &gone, Master: testdb.DocumentKey}, Scanner: scanner, Key: testdb.DocumentKey, MaxBytes: 64 << 20})
	if _, err := noBucket.AddRevisionStream(f.ctx, w.eng, w.doc(t, "REAL-0"), RevisionInput{Revision: "A", FileName: "x.pdf", MediaType: "application/pdf", ChangeNote: "No bucket"}, bytes.NewReader(pdf("x\n"))); !errors.Is(err, blob.ErrBucketNotFound) || errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("upload into a missing bucket: %v", err)
	}
	svc := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: s3, Master: testdb.DocumentKey}, Scanner: scanner, Key: testdb.DocumentKey, MaxBytes: 64 << 20})
	if pol, err := svc.GetDocumentPolicy(f.ctx, w.eng); err != nil || !pol.ScannerConfigured || pol.Storage != "s3" || !pol.ScanRequired {
		t.Fatalf("policy with real services: %+v %v", pol, err)
	}
	doc := w.doc(t, "REAL-1")
	// 7 MiB clean PDF: scanned by clamd, multipart to the object store, published, downloaded.
	big := append(pdf("real integration\n"), bytes.Repeat([]byte("clean content. "), 7<<20/15)...)
	rev, err := svc.AddRevisionStream(f.ctx, w.eng, doc, RevisionInput{Revision: "A", FileName: "real.pdf", MediaType: "application/pdf", ChangeNote: "Real scanner and store"}, bytes.NewReader(big))
	must(t, "upload", err)
	d, err := svc.GetDocument(f.ctx, w.eng, doc)
	must(t, "get", err)
	if r := d.Revisions[0]; r.ScanStatus != "CLEAN" || !strings.HasPrefix(r.ScanEngine, "ClamAV ") {
		t.Fatalf("scan by clamd: %+v", r)
	}
	must(t, "submit", svc.SubmitRevision(f.ctx, w.eng, rev))
	must(t, "publish", svc.DecideRevision(f.ctx, w.pmFresh, rev, true, ""))
	file, err := svc.DownloadRevision(f.ctx, w.planner, rev)
	if err != nil || !bytes.Equal(file.Content, big) {
		t.Fatalf("download from the object store: %v", err)
	}
	sum := sha256.Sum256(big)
	key, _ := blob.ObjectKey(f.tenant, rev, hex.EncodeToString(sum[:]))
	raw, err := s3.Get(f.ctx, key)
	must(t, "object exists under the tenant-scoped key", err)
	head := make([]byte, 4096)
	n, _ := io.ReadFull(raw, head)
	raw.Close()
	if bytes.Contains(head[:n], []byte("real integration")) || bytes.Contains(head[:n], []byte("%PDF")) {
		t.Fatal("the object store holds plaintext")
	}
	// The EICAR test file: found by clamd, quarantined.
	bad, err := svc.AddRevision(f.ctx, w.eng, w.doc(t, "REAL-2"), RevisionInput{Revision: "A", FileName: "eicar.txt", MediaType: "text/plain", ChangeNote: "Antivirus test file", Content: []byte(testdb.EICAR)})
	must(t, "infected upload", err)
	if err := svc.SubmitRevision(f.ctx, w.eng, bad); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("clamd-detected file was submitted: %v", err)
	}
	if _, err := svc.DownloadRevision(f.ctx, w.eng, bad); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("clamd-detected file was downloaded: %v", err)
	}
	// The daemon unreachable: the upload is recorded as a failed scan and stays quarantined.
	down := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: s3, Master: testdb.DocumentKey}, Scanner: docscan.Clamd{Network: "tcp", Address: "127.0.0.1:1"}, Key: testdb.DocumentKey})
	pending, err := txt(down, w, w.doc(t, "REAL-3"), "A", "uploaded while the scanner was down")
	must(t, "upload with the scanner down", err)
	if err := down.SubmitRevision(f.ctx, w.eng, pending); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a file was released while the scanner was down: %v", err)
	}
	if got, err := svc.RescanRevision(f.ctx, w.eng, pending); err != nil || got.ScanStatus != "CLEAN" {
		t.Fatalf("rescan once the scanner is back: %+v %v", got, err)
	}
}

// TestDocSecurity_KeyRotation (ADR 0021): after the document key is rotated,
// files stored and verdicts recorded under the previous key remain usable
// while that key is supplied; new files are written under the new key; and a
// service that is given neither key fails closed (quarantine, key unavailable)
// without damaging anything, so supplying the key again recovers the file.
func TestDocSecurity_KeyRotation(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	store := blob.FS{Root: t.TempDir()}
	oldKey, newKey := testdb.DocumentKey, bytes.Repeat([]byte{0x5C}, 32)
	scanner := testdb.FakeScanner{}
	before := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: store, Master: oldKey, KeyID: "d1"}, Scanner: scanner, Key: oldKey, MaxBytes: 32 << 20})
	body := append(pdf("before rotation\n"), bytes.Repeat([]byte("kept. "), 40000)...)
	doc := w.doc(t, "ROT-1")
	rev1, err := before.AddRevisionStream(f.ctx, w.eng, doc, RevisionInput{Revision: "A", FileName: "a.pdf", MediaType: "application/pdf", ChangeNote: "Before rotation"}, bytes.NewReader(body))
	must(t, "upload before rotation", err)

	// Rotated: new current key d2, the old one listed as previous.
	after := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: store, Master: newKey, KeyID: "d2", Previous: map[string][]byte{"d1": oldKey}},
		Scanner: scanner, Key: newKey, PreviousKeys: [][]byte{oldKey}, MaxBytes: 32 << 20})
	if file, err := after.DownloadRevision(f.ctx, w.eng, rev1); err != nil || !bytes.Equal(file.Content, body) {
		t.Fatalf("file and verdict from before the rotation: %v", err)
	}
	doc2 := w.doc(t, "ROT-2")
	body2 := append(pdf("after rotation\n"), bytes.Repeat([]byte("new. "), 40000)...)
	rev2, err := after.AddRevisionStream(f.ctx, w.eng, doc2, RevisionInput{Revision: "A", FileName: "b.pdf", MediaType: "application/pdf", ChangeNote: "After rotation"}, bytes.NewReader(body2))
	must(t, "upload after rotation", err)
	if file, err := after.DownloadRevision(f.ctx, w.eng, rev2); err != nil || !bytes.Equal(file.Content, body2) {
		t.Fatalf("file written under the new key: %v", err)
	}
	// The service from before the rotation cannot open the new file, and says why.
	if _, err := before.DownloadRevision(f.ctx, w.eng, rev2); err == nil || errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("a file under the new key was served by a service holding only the old key: %v", err)
	}

	// The old key is withheld: the old verdict is no longer proven (quarantine), nothing is served...
	without := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: store, Master: newKey, KeyID: "d2"}, Scanner: scanner, Key: newKey, MaxBytes: 32 << 20})
	if _, err := without.DownloadRevision(f.ctx, w.eng, rev1); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("a verdict from a key the service lacks was accepted: %v", err)
	}
	// ...with the proof key but not the file's key, the read reports the missing key rather than a damaged file...
	noFileKey := w.svcWith(DocumentStore{Blob: &blob.Sealed{Store: store, Master: newKey, KeyID: "d2"}, Scanner: scanner, Key: newKey, PreviousKeys: [][]byte{oldKey}, MaxBytes: 32 << 20})
	if _, err := noFileKey.DownloadRevision(f.ctx, w.eng, rev1); !errors.Is(err, blob.ErrKeyUnavailable) || errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("read without the file's key: %v", err)
	}
	// ...and with the key supplied again the file is served unchanged (recovery).
	if file, err := after.DownloadRevision(f.ctx, w.eng, rev1); err != nil || !bytes.Equal(file.Content, body) {
		t.Fatalf("recovery after the key was supplied again: %v", err)
	}
}
