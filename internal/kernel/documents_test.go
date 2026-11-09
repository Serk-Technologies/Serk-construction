package kernel

// Tests for document control, RFIs, submittals and transmittals (WP-3.5, migration 0066, kernel/documents.go).

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestDocuments_RevisionsPublishTransmitRFI(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	now := time.Now()
	person := func(id, role string, projects []string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, ProjectIDs: projects, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
	}
	proj := []string{f.project}
	dc := person("u-doc-controller", "document_controller", nil)
	eng, pm := c.siteEng, c.pm
	pdf := []byte("%PDF-1.4 drawing A-102 revision A")
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

	if _, err := f.svc.CreateDocument(f.ctx, eng, DocumentIn{ProjectID: f.project, Number: "A-102", Title: "Ground floor plan", Discipline: "Architecture", DocType: "DRAWING", FileName: "A-102.pdf", Mime: "application/pdf", ContentB64: b64(pdf)}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer created a document: %v", err)
	}
	if _, err := f.svc.CreateDocument(f.ctx, dc, DocumentIn{ProjectID: f.project, Number: "A-102", Title: "Ground floor plan", Discipline: "Architecture", DocType: "DRAWING", FileName: "../evil.exe", Mime: "application/pdf", ContentB64: b64(pdf)}); err == nil {
		t.Fatal("a path in the file name was accepted")
	}
	if _, err := f.svc.CreateDocument(f.ctx, dc, DocumentIn{ProjectID: f.project, Number: "A-102", Title: "Ground floor plan", Discipline: "Architecture", DocType: "DRAWING", FileName: "A-102.pdf", Mime: "text/html", ContentB64: b64(pdf)}); err == nil {
		t.Fatal("an HTML file was accepted")
	}
	doc, err := f.svc.CreateDocument(f.ctx, dc, DocumentIn{ProjectID: f.project, Number: "a-102", Title: "Ground floor plan", Discipline: "Architecture", DocType: "DRAWING", FileName: "A-102.pdf", Mime: "application/pdf", ContentB64: b64(pdf), Note: "Issued for tender"})
	must(t, "document", err)

	// Work in progress is invisible to the site; only the document side reads it.
	if _, _, _, err := f.svc.DownloadRevision(f.ctx, eng, doc, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the site read a work-in-progress revision: %v", err)
	}
	if err := f.svc.PublishRevision(f.ctx, dc, doc); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the document controller published: %v", err)
	}
	if _, err := f.svc.SendTransmittal(f.ctx, dc, TransmittalIn{ProjectID: f.project, Subject: "Tender set", ToParty: "Client", Purpose: "for_review", DocumentIDs: []string{doc}}); err == nil {
		t.Fatal("an unpublished document was transmitted")
	}
	must(t, "publish", f.svc.PublishRevision(f.ctx, pm, doc))
	name, mime, got, err := f.svc.DownloadRevision(f.ctx, eng, doc, 1)
	must(t, "download", err)
	if name != "A-102.pdf" || mime != "application/pdf" || !bytes.Equal(got, pdf) {
		t.Fatalf("download: %s %s %q", name, mime, got)
	}

	// A second revision: the uploader cannot publish it; publishing supersedes revision 1.
	rev2, err := f.svc.UploadRevision(f.ctx, dc, doc, RevisionIn{FileName: "A-102-B.pdf", Mime: "application/pdf", ContentB64: b64([]byte("%PDF-1.4 revision B")), Note: "Slab raised 50 mm"})
	must(t, "rev 2", err)
	if rev2 != 2 {
		t.Fatalf("revision %d", rev2)
	}
	if _, err := f.svc.UploadRevision(f.ctx, dc, doc, RevisionIn{FileName: "x.pdf", Mime: "application/pdf", ContentB64: b64([]byte("x"))}); err == nil {
		t.Fatal("two revisions awaiting publication")
	}
	selfPM := pm
	selfPM.UserID = dc.UserID
	if err := f.svc.PublishRevision(f.ctx, selfPM, doc); err == nil {
		t.Fatal("the uploader published their own revision")
	}
	must(t, "publish 2", f.svc.PublishRevision(f.ctx, pm, doc))
	var status string
	must(t, "superseded", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM document_revisions WHERE document_id = $1::uuid AND rev = 1`, doc).Scan(&status)
	}))
	if status != "SUPERSEDED" {
		t.Fatalf("revision 1 is %s", status)
	}
	rawFails(t, f, "edit a stored file", `UPDATE document_revisions SET content = '\x00' WHERE rev = 1`)
	rawFails(t, f, "tamper with a hash", `UPDATE document_revisions SET sha256 = repeat('0', 64) WHERE rev = 2`)

	tr, err := f.svc.SendTransmittal(f.ctx, dc, TransmittalIn{ProjectID: f.project, Subject: "Issued for construction", ToParty: "Client representative", Purpose: "for_construction", DocumentIDs: []string{doc}})
	must(t, "transmittal", err)
	_ = tr
	dv, err := f.svc.DocsView(f.ctx, dc, "ground")
	must(t, "view", err)
	if len(dv["documents"].([]map[string]any)) != 1 || len(dv["transmittals"].([]map[string]any)) != 1 {
		t.Fatalf("view: %v", dv)
	}
	if v2, _ := f.svc.DocsView(f.ctx, dc, "no such"); len(v2["documents"].([]map[string]any)) != 0 {
		t.Fatal("search found a non-matching document")
	}

	// RFI: raised by the engineer, answered by the project manager, closed by the raiser.
	rfi, err := f.svc.RaiseRFI(f.ctx, eng, RFIIn{ProjectID: f.project, Subject: "Slab level at gridline C", Question: "Drawing A-102 shows +0.150 but the structural drawing shows +0.100. Which governs?", BallInCourt: "Architect", DueOn: today().AddDate(0, 0, 5).Format("2006-01-02"), DocumentID: doc})
	must(t, "rfi", err)
	if err := f.svc.AnswerRFI(f.ctx, eng, rfi, "I know", false, false, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer answered an RFI: %v", err)
	}
	must(t, "answer", f.svc.AnswerRFI(f.ctx, pm, rfi, "The architectural level +0.150 governs; revision B of A-102 issued.", true, false, ""))
	if err := f.svc.CloseRFI(f.ctx, pm, rfi); err == nil {
		t.Fatal("the responder closed their own answered RFI")
	}
	must(t, "close rfi", f.svc.CloseRFI(f.ctx, eng, rfi))

	// Submittal: reviewed by someone else with a code; C and D need a note.
	sm, err := f.svc.SubmitSubmittal(f.ctx, eng, f.project, "03 30 00", "Concrete mix design C25")
	must(t, "submittal", err)
	if err := f.svc.ReviewSubmittal(f.ctx, pm, sm, "c", ""); err == nil {
		t.Fatal("code C without a note")
	}
	must(t, "review", f.svc.ReviewSubmittal(f.ctx, pm, sm, "b", "Approved as noted: use 0.45 water-cement ratio"))
	if _, err := f.svc.DocsView(f.ctx, w.cast.buyer, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer read the documents: %v", err)
	}
	_ = proj
}
