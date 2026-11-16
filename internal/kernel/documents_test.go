package kernel

// Tests for controlled documents, revisions and RFIs (requirements CNX-01,
// CNX-02, CNX-03; migration 0052, kernel/documents.go).
// Proven: only allow-listed files whose bytes match their type are accepted;
// the database hashes the stored file and refuses a revision without one; a
// revision's file and description never change; a revision is published by a
// different person with fresh MFA, never by an AI principal and never by raw
// SQL; publishing supersedes the previous revision so a document has exactly
// one current revision, also under concurrent attempts; a superseded revision
// is not offered as current; documents are invisible outside their project and
// tenant; downloads return the attested bytes and are recorded; an RFI is
// answered once by someone other than its author and closed by its author.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

func pdf(body string) []byte { return []byte("%PDF-1.7\n" + body) }

type docWorld struct {
	*p2pWorld
	eng, planner, pm, pmFresh, outsideEng auth.Principal
}

func newDocWorld(t *testing.T) *docWorld {
	t.Helper()
	w := &docWorld{p2pWorld: newP2PWorld(t)}
	f := w.f
	now := time.Now()
	mk := func(id string, project string, roles ...string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: roles, ProjectIDs: []string{project}, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
	}
	w.eng, w.pm = w.cast.siteEng, w.cast.pm
	w.planner = mk("u-planner", f.project, "planner")
	w.pmFresh = w.pm
	w.pmFresh.MFA, w.pmFresh.AuthTime = true, now
	w.outsideEng = mk("u-eng-elsewhere", "00000000-0000-0000-0000-000000000001", "site_engineer", "project_manager")
	return w
}

func (w *docWorld) revision(t *testing.T, doc, rev string, content []byte) string {
	t.Helper()
	id, err := w.f.svc.AddRevision(w.f.ctx, w.eng, doc, RevisionInput{Revision: rev, FileName: "GA-plan-" + rev + ".pdf", MediaType: "application/pdf",
		ChangeNote: "Issued for construction, revision " + rev, Content: content})
	must(t, "revision "+rev, err)
	return id
}

func TestDocuments_RevisionsAndPublication(t *testing.T) {
	w := newDocWorld(t)
	f := w.f

	// Who may register a document, and what a document is.
	in := DocumentInput{ProjectID: f.project, Reference: "A-101", DocType: "drawing", Title: "General arrangement plan, level 1", Discipline: "Architecture"}
	if _, err := f.svc.CreateDocument(f.ctx, w.pm, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the approver registered a document: %v", err)
	}
	if _, err := f.svc.CreateDocument(f.ctx, w.outsideEng, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer of another project registered a document: %v", err)
	}
	bad := in
	bad.DocType = "MEMO"
	if _, err := f.svc.CreateDocument(f.ctx, w.eng, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown document type: %v", err)
	}
	doc, err := f.svc.CreateDocument(f.ctx, w.eng, in)
	must(t, "document", err)
	if _, err := f.svc.CreateDocument(f.ctx, w.eng, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a second document with the same reference: %v", err)
	}
	rawFails(t, f, "retitle a register entry", `UPDATE documents SET title = 'changed' WHERE id = '`+doc+`'`)
	rawFails(t, f, "delete a register entry", `DELETE FROM documents WHERE id = '`+doc+`'`)

	// Files: allow-list, signature, size.
	up := RevisionInput{Revision: "A", FileName: "GA-plan-A.pdf", MediaType: "application/pdf", ChangeNote: "First issue", Content: pdf("rev A")}
	for name, mut := range map[string]func(*RevisionInput){
		"an executable type":       func(r *RevisionInput) { r.MediaType = "application/x-msdownload" },
		"a file that is not a PDF": func(r *RevisionInput) { r.Content = []byte("MZ\x90\x00 not a pdf") },
		"an empty file":            func(r *RevisionInput) { r.Content = nil },
		"an oversized file":        func(r *RevisionInput) { r.Content = append(pdf(""), make([]byte, MaxDocumentBytes)...) },
		"binary as text": func(r *RevisionInput) {
			r.MediaType, r.Content = "text/plain", []byte{'a', 0, 'b'}
		},
		"no change note": func(r *RevisionInput) { r.ChangeNote = "" },
	} {
		r := up
		mut(&r)
		if _, err := f.svc.AddRevision(f.ctx, w.eng, doc, r); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
	if _, err := f.svc.AddRevision(f.ctx, w.outsideEng, doc, up); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an engineer of another project uploaded a revision: %v", err)
	}
	revA := w.revision(t, doc, "A", pdf("rev A"))
	if _, err := f.svc.AddRevision(f.ctx, w.eng, doc, RevisionInput{Revision: "B", FileName: "b.pdf", MediaType: "application/pdf", ChangeNote: "Second", Content: pdf("rev B")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a second revision in preparation: %v", err)
	}

	// The database owns the hash: a revision without its file, or with other bytes, is refused.
	sum := sha256.Sum256(pdf("x"))
	rawFails(t, f, "replace a stored file", `UPDATE document_contents SET content = 'x' WHERE revision_id = '`+revA+`'`)
	rawFails(t, f, "delete a stored file", `DELETE FROM document_contents WHERE revision_id = '`+revA+`'`)
	rawFails(t, f, "rename a revision's file", `UPDATE document_revisions SET file_name = 'other.pdf' WHERE id = '`+revA+`'`)
	rawFails(t, f, "change a revision's hash", `UPDATE document_revisions SET sha256 = '`+hex.EncodeToString(sum[:])+`' WHERE id = '`+revA+`'`)

	// Before publication the document has no current revision.
	d, err := f.svc.GetDocument(f.ctx, w.eng, doc)
	must(t, "get", err)
	if d.Current != nil || d.InReview != 1 || len(d.Revisions) != 1 {
		t.Fatalf("draft only: %+v", d)
	}

	// Submission by the author; publication by another person, fresh MFA, human, attested.
	if err := f.svc.SubmitRevision(f.ctx, w.planner, revA); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("someone else submitted the author's revision: %v", err)
	}
	if err := f.svc.DecideRevision(f.ctx, w.pmFresh, revA, true, ""); !errors.Is(err, ledger.ErrBadTransition) {
		t.Fatalf("a draft was published: %v", err)
	}
	must(t, "submit A", f.svc.SubmitRevision(f.ctx, w.eng, revA))
	if err := f.svc.DecideRevision(f.ctx, w.eng, revA, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the uploader published without the permission: %v", err)
	}
	both := w.eng
	both.Roles = []string{"site_engineer", "project_manager"}
	both.MFA, both.AuthTime = true, time.Now()
	if err := f.svc.DecideRevision(f.ctx, both, revA, true, ""); err == nil {
		t.Fatal("the author published their own revision")
	}
	if err := f.svc.DecideRevision(f.ctx, w.pm, revA, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("publication without fresh MFA: %v", err)
	}
	ai := w.pmFresh
	ai.Kind = auth.KindAI
	if err := f.svc.DecideRevision(f.ctx, ai, revA, true, ""); err == nil {
		t.Fatal("an AI principal published a revision")
	}
	rawFails(t, f, "publish without an attestation", `UPDATE document_revisions SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+revA+`'`)
	must(t, "publish A", f.svc.DecideRevision(f.ctx, w.pmFresh, revA, true, ""))
	d, err = f.svc.GetDocument(f.ctx, w.eng, doc)
	must(t, "get", err)
	if d.Current == nil || d.Current.ID != revA || d.Current.ApprovedBy != w.pm.UserID || d.InReview != 0 {
		t.Fatalf("after publishing A: %+v", d)
	}
	// The database owns the hash: with no revision in preparation, a raw
	// revision without its file, or with other bytes than it declares, is refused.
	forge := `INSERT INTO document_revisions (document_id, number, revision, file_name, media_type, size_bytes, sha256, change_note, created_by)
		VALUES ('` + doc + `', 'forged/Z', 'Z', 'z.pdf', 'application/pdf', 10, '` + hex.EncodeToString(sum[:]) + `', 'forged', 'u-forger') RETURNING id::text`
	rawFails(t, f, "a revision without a file", forge)
	err = f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, forge).Scan(&id); err != nil {
			return errors.New("setup: " + err.Error())
		}
		_, err := tx.Exec(ctx, `INSERT INTO document_contents (revision_id, content) VALUES ($1::uuid, 'ten bytes!')`, id)
		return err
	})
	if err == nil || strings.Contains(err.Error(), "setup:") || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("a file with other bytes than its revision declares: %v", err)
	}
	rawFails(t, f, "supersede with no successor", `UPDATE document_revisions SET status = 'SUPERSEDED' WHERE id = '`+revA+`'`)
	rawFails(t, f, "edit a published revision", `UPDATE document_revisions SET change_note = 'changed' WHERE id = '`+revA+`'`)

	// A rejected revision needs a reason, changes nothing, and frees the slot.
	revB := w.revision(t, doc, "B", pdf("rev B"))
	must(t, "submit B", f.svc.SubmitRevision(f.ctx, w.eng, revB))
	if err := f.svc.DecideRevision(f.ctx, w.pmFresh, revB, false, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("rejection without a reason: %v", err)
	}
	must(t, "reject B", f.svc.DecideRevision(f.ctx, w.pmFresh, revB, false, "Grid line C is missing"))
	rawFails(t, f, "revive a rejected revision", `UPDATE document_revisions SET status = 'SUBMITTED' WHERE id = '`+revB+`'`)
	if d, _ = f.svc.GetDocument(f.ctx, w.eng, doc); d.Current == nil || d.Current.ID != revA {
		t.Fatalf("a rejection changed the current revision: %+v", d.Current)
	}
	if _, err := f.svc.AddRevision(f.ctx, w.eng, doc, RevisionInput{Revision: "B", FileName: "b.pdf", MediaType: "application/pdf", ChangeNote: "Again", Content: pdf("rev B2")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a revision code was reused: %v", err)
	}

	// Publishing C supersedes A in the same transaction: exactly one current.
	revC := w.revision(t, doc, "C", pdf("rev C"))
	must(t, "submit C", f.svc.SubmitRevision(f.ctx, w.eng, revC))
	must(t, "publish C", f.svc.DecideRevision(f.ctx, w.pmFresh, revC, true, ""))
	d, err = f.svc.GetDocument(f.ctx, w.eng, doc)
	must(t, "get", err)
	if d.Current == nil || d.Current.ID != revC || d.Superseded != 1 || len(d.Revisions) != 3 {
		t.Fatalf("after publishing C: current %+v, superseded %d, revisions %d", d.Current, d.Superseded, len(d.Revisions))
	}
	for _, r := range d.Revisions {
		if r.ID == revA && (r.Status != "SUPERSEDED" || r.SupersededAt == nil) {
			t.Fatalf("revision A after C: %+v", r)
		}
	}
	rawFails(t, f, "restore a superseded revision", `UPDATE document_revisions SET status = 'APPROVED' WHERE id = '`+revA+`'`)
	list, err := f.svc.ListDocuments(f.ctx, w.eng, f.project, 0)
	must(t, "list", err)
	if len(list) != 1 || list[0].Current == nil || list[0].Current.Revision != "C" {
		t.Fatalf("register: %+v", list)
	}

	// The file comes back exactly as attested, with its status, and the download is recorded.
	file, err := f.svc.DownloadRevision(f.ctx, w.planner, revA)
	must(t, "download A", err)
	want := sha256.Sum256(pdf("rev A"))
	if string(file.Content) != string(pdf("rev A")) || file.SHA256 != hex.EncodeToString(want[:]) || file.Status != "SUPERSEDED" {
		t.Fatalf("download A: status %s, sha %s, %d bytes", file.Status, file.SHA256, len(file.Content))
	}
	var events int
	must(t, "access events", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM data_access_events WHERE user_id = $1 AND kind = 'export' AND subject = $2`, w.planner.UserID, "document_revision:"+revA).Scan(&events)
	}))
	if events != 1 {
		t.Fatalf("download events: %d", events)
	}

	// The revision is a document of the generic framework too.
	g, err := f.svc.GetP2PDoc(f.ctx, w.eng, "document_revision", revC)
	must(t, "generic read", err)
	if g.Status != "APPROVED" {
		t.Fatalf("generic read: %+v", g)
	}
}

func TestDocuments_ConcurrentPublicationKeepsOneCurrent(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	doc, err := f.svc.CreateDocument(f.ctx, w.eng, DocumentInput{ProjectID: f.project, Reference: "S-200", DocType: "DRAWING", Title: "Foundation layout"})
	must(t, "document", err)
	revA := w.revision(t, doc, "A", pdf("A"))
	must(t, "submit A", f.svc.SubmitRevision(f.ctx, w.eng, revA))
	must(t, "publish A", f.svc.DecideRevision(f.ctx, w.pmFresh, revA, true, ""))
	revB := w.revision(t, doc, "B", pdf("B"))
	must(t, "submit B", f.svc.SubmitRevision(f.ctx, w.eng, revB))

	// Eight project managers decide revision B at once: half publish, half reject.
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := w.pmFresh
			p.UserID = "u-pm-" + string(rune('a'+i))
			errs[i] = f.svc.DecideRevision(f.ctx, p, revB, i%2 == 0, "Not coordinated with structure")
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d concurrent decisions took effect: %v", won, n, errs)
	}
	var current, decided int
	must(t, "count", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'APPROVED'), count(*) FILTER (WHERE id = $2::uuid AND status IN ('APPROVED', 'REJECTED'))
			FROM document_revisions WHERE document_id = $1::uuid`, doc, revB).Scan(&current, &decided)
	}))
	if current != 1 || decided != 1 {
		t.Fatalf("current revisions %d, decisions on B %d", current, decided)
	}
}

func TestDocuments_ScopeAndTenantIsolation(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	doc, err := f.svc.CreateDocument(f.ctx, w.eng, DocumentInput{ProjectID: f.project, Reference: "SP-01", DocType: "SPECIFICATION", Title: "Concrete specification"})
	must(t, "document", err)
	rev := w.revision(t, doc, "0", pdf("spec"))

	// Another project of the same tenant: absence, not refusal.
	if _, err := f.svc.GetDocument(f.ctx, w.outsideEng, doc); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read from another project: %v", err)
	}
	if _, err := f.svc.DownloadRevision(f.ctx, w.outsideEng, rev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("download from another project: %v", err)
	}
	if l, err := f.svc.ListDocuments(f.ctx, w.outsideEng, "", 0); err != nil || len(l) != 0 {
		t.Fatalf("list from another project: %v %v", l, err)
	}
	if err := f.svc.SubmitRevision(f.ctx, w.outsideEng, rev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("submit from another project: %v", err)
	}

	// Another tenant, with every reading role: nothing, by any route.
	if _, err := f.svc.GetDocument(f.ctx, f.outsider, doc); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read from another tenant: %v", err)
	}
	if _, err := f.svc.DownloadRevision(f.ctx, f.outsider, rev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("download from another tenant: %v", err)
	}
	if l, err := f.svc.ListDocuments(f.ctx, f.outsider, "", 0); err != nil || len(l) != 0 {
		t.Fatalf("list from another tenant: %v %v", l, err)
	}
	var seen int
	must(t, "raw read as the other tenant", f.app.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM documents) + (SELECT count(*) FROM document_revisions) + (SELECT count(*) FROM document_contents) + (SELECT count(*) FROM rfis)`).Scan(&seen)
	}))
	if seen != 0 {
		t.Fatalf("the other tenant's login sees %d rows", seen)
	}
	// The other tenant cannot attach a file or an RFI to this tenant's revision.
	err = f.app.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO document_contents (revision_id, content) VALUES ($1::uuid, 'x')`, rev)
		return err
	})
	if err == nil {
		t.Fatal("the other tenant stored a file against this tenant's revision")
	}
}

func TestDocuments_RFILifecycle(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	day := func(offset int) string { return time.Now().AddDate(0, 0, offset).Format("2006-01-02") }
	doc, err := f.svc.CreateDocument(f.ctx, w.eng, DocumentInput{ProjectID: f.project, Reference: "A-102", DocType: "DRAWING", Title: "Sections"})
	must(t, "document", err)
	rev := w.revision(t, doc, "A", pdf("sections"))

	in := RFIInput{ProjectID: f.project, Subject: "Slab thickness at grid C-4", Question: "Section 2 shows 200 mm, the schedule shows 250 mm. Which governs?",
		RevisionID: rev, AssignedTo: "u-planner", DueOn: day(7)}
	if _, err := f.svc.CreateRFI(f.ctx, w.pm, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("someone without the permission raised an RFI: %v", err)
	}
	past := in
	past.DueOn = day(-1)
	if _, err := f.svc.CreateRFI(f.ctx, w.eng, past); err == nil {
		t.Fatal("an RFI due in the past was accepted")
	}
	foreign := in
	foreign.RevisionID = "00000000-0000-0000-0000-00000000dead"
	if _, err := f.svc.CreateRFI(f.ctx, w.eng, foreign); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an RFI about a revision of another project: %v", err)
	}
	id, err := f.svc.CreateRFI(f.ctx, w.eng, in)
	must(t, "rfi", err)
	soon := in
	soon.DueOn, soon.RevisionID = day(2), ""
	second, err := f.svc.CreateRFI(f.ctx, w.eng, soon)
	must(t, "second rfi", err)

	list, err := f.svc.ListRFIs(f.ctx, w.planner, f.project, "OPEN", 0)
	must(t, "list", err)
	standing := map[string]string{}
	for _, r := range list {
		standing[r.ID] = r.Standing
	}
	if standing[id] != "OPEN" || standing[second] != "DUE_SOON" {
		t.Fatalf("standing: %v", standing)
	}

	// Answer: not the author, a person, once, attested.
	if err := f.svc.AnswerRFI(f.ctx, w.eng, id, "250 mm governs."); err == nil {
		t.Fatal("the author answered their own RFI")
	}
	ai := w.planner
	ai.Kind = auth.KindAI
	if err := f.svc.AnswerRFI(f.ctx, ai, id, "250 mm governs."); err == nil {
		t.Fatal("an AI principal answered an RFI")
	}
	if err := f.svc.AnswerRFI(f.ctx, w.outsideEng, id, "250 mm governs."); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an engineer of another project answered: %v", err)
	}
	rawFails(t, f, "answer without an attestation", `UPDATE rfis SET status = 'ANSWERED', answer = 'forged', answered_by = 'u-forger', answered_at = now() WHERE id = '`+id+`'`)
	if err := f.svc.CloseRFI(f.ctx, w.eng, id, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("an unanswered RFI was withdrawn without a reason: %v", err)
	}
	must(t, "answer", f.svc.AnswerRFI(f.ctx, w.planner, id, "The schedule governs: 250 mm. Section 2 will be corrected in revision B."))
	if err := f.svc.AnswerRFI(f.ctx, w.planner, id, "Another answer"); !errors.Is(err, ledger.ErrBadTransition) {
		t.Fatalf("an RFI was answered twice: %v", err)
	}
	rawFails(t, f, "rewrite an answer", `UPDATE rfis SET answer = 'changed' WHERE id = '`+id+`'`)
	rawFails(t, f, "rewrite a question", `UPDATE rfis SET question = 'a different question entirely' WHERE id = '`+second+`'`)

	// Close: by the author only; a closed RFI never changes.
	if err := f.svc.CloseRFI(f.ctx, w.planner, id, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("someone else closed the author's RFI: %v", err)
	}
	must(t, "close", f.svc.CloseRFI(f.ctx, w.eng, id, ""))
	must(t, "withdraw", f.svc.CloseRFI(f.ctx, w.eng, second, "Superseded by the answer to the first RFI"))
	rawFails(t, f, "reopen a closed RFI", `UPDATE rfis SET status = 'OPEN' WHERE id = '`+id+`'`)
	if err := f.svc.CloseRFI(f.ctx, w.eng, id, ""); !errors.Is(err, ledger.ErrBadTransition) {
		t.Fatalf("an RFI was closed twice: %v", err)
	}
	all, err := f.svc.ListRFIs(f.ctx, w.planner, f.project, "", 0)
	must(t, "list all", err)
	if len(all) != 2 || all[0].Status != "CLOSED" || all[1].Status != "CLOSED" {
		t.Fatalf("after closing: %+v", all)
	}
	if _, err := f.svc.ListRFIs(f.ctx, w.planner, f.project, "PENDING", 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown status filter: %v", err)
	}
	if l, err := f.svc.ListRFIs(f.ctx, w.outsideEng, "", "", 0); err != nil || len(l) != 0 {
		t.Fatalf("RFIs seen from another project: %v %v", l, err)
	}
}
