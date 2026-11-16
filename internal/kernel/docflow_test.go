package kernel

import (
	"errors"
	"testing"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestDocflow_TransmittalsAndSubmittals (0077): a transmittal carries the
// approved revisions of the project's documents and never changes; its items
// cannot be added later from another transaction; a submittal is reviewed
// once, with a code, by someone other than the submitter, and the review is an
// attested decision the tenant login cannot forge.
func TestDocflow_TransmittalsAndSubmittals(t *testing.T) {
	w := newDocWorld(t)
	f := w.f
	doc, err := f.svc.CreateDocument(f.ctx, w.eng, DocumentInput{ProjectID: f.project, Reference: "S-201", DocType: "drawing", Title: "Foundation layout", Discipline: "Structure"})
	must(t, "document", err)
	draft, err := f.svc.CreateDocument(f.ctx, w.eng, DocumentInput{ProjectID: f.project, Reference: "S-202", DocType: "drawing", Title: "Pile schedule", Discipline: "Structure"})
	must(t, "document 2", err)
	rev := w.revision(t, doc, "A", pdf("foundation layout A"))
	must(t, "submit", f.svc.SubmitRevision(f.ctx, w.eng, rev))
	must(t, "approve", f.svc.DecideRevision(f.ctx, w.pmFresh, rev, true, ""))

	in := TransmittalInput{ProjectID: f.project, Subject: "Foundation drawings for construction", ToParty: "Abay Piling PLC", Purpose: "for_construction", DocumentIDs: []string{doc}}
	if _, err := f.svc.SendTransmittal(f.ctx, w.cast.buyer, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer sent a transmittal: %v", err)
	}
	if _, err := f.svc.SendTransmittal(f.ctx, w.eng, TransmittalInput{ProjectID: f.project, Subject: "Unapproved drawing", ToParty: "Abay Piling PLC", Purpose: "FOR_INFORMATION", DocumentIDs: []string{draft}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a document without an approved revision was transmitted: %v", err)
	}
	tr, err := f.svc.SendTransmittal(f.ctx, w.eng, in)
	must(t, "transmittal", err)
	rawRefusedWith(t, f, "add an item to a transmittal already sent", "in the transaction that sends it",
		`INSERT INTO transmittal_items (transmittal_id, revision_id) VALUES ($1::uuid, $2::uuid)`, tr, rev)
	rawFails(t, f, "edit a sent transmittal", `UPDATE transmittals SET to_party = 'Someone else' WHERE id = '`+tr+`'`)

	sb, err := f.svc.SubmitSubmittal(f.ctx, w.eng, SubmittalInput{ProjectID: f.project, SpecSection: "03 30 00", Title: "Concrete mix design C30"})
	must(t, "submittal", err)
	engAlsoPM := w.pmFresh
	engAlsoPM.UserID = w.eng.UserID
	if err := f.svc.ReviewSubmittal(f.ctx, engAlsoPM, sb, "A", ""); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the submitter reviewed their own submittal: %v", err)
	}
	attestedWriteRefusedWith(t, f, engAlsoPM, "submittal.review", "submittal", sb, "A", "someone other than the submitter",
		`UPDATE submittals SET status = 'REVIEWED', review_code = 'A', reviewed_by = $2 WHERE id = $1::uuid`, sb, w.eng.UserID)
	rawRefusedWith(t, f, "review a submittal under a forged identity", "approval attestation",
		`UPDATE submittals SET status = 'REVIEWED', review_code = 'A', reviewed_by = $2 WHERE id = $1::uuid`, sb, w.pm.UserID)
	if err := f.svc.ReviewSubmittal(f.ctx, w.pm, sb, "C", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("code C without a note: %v", err)
	}
	must(t, "review", f.svc.ReviewSubmittal(f.ctx, w.pm, sb, "C", "Increase the cement content to meet the 28-day strength"))
	if err := f.svc.ReviewSubmittal(f.ctx, w.pm, sb, "A", ""); err == nil {
		t.Fatal("a submittal was reviewed twice")
	}
	list, err := f.svc.ListP2PDocs(f.ctx, w.pm, "submittal", "", 10)
	must(t, "list", err)
	if len(list) != 1 || list[0].Status != "REVIEWED/C" {
		t.Fatalf("submittal list: %+v", list)
	}
}
