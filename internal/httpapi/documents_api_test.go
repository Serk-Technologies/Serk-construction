package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Controlled documents and RFIs through the API (0052): roles reach only
// their routes; a file larger than the ordinary body limit is accepted on the
// upload route only; publication needs fresh MFA and a second person; the file
// is served as an attachment with its revision status and hash; other
// projects get "not found"; an RFI is answered by someone else and closed by
// its author.
func TestAPIDocumentsAndRFIs(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	eng := a.tok("u-doc-eng", P, false, "site_engineer")
	planner := a.tok("u-doc-planner", P, false, "planner")
	pm, pmMFA := a.tok("u-doc-pm", P, false, "project_manager"), a.tok("u-doc-pm", P, true, "project_manager")
	elsewhere := a.tok("u-doc-else", []string{uuid.NewString()}, true, "site_engineer", "project_manager")

	doc := map[string]any{"project_id": a.project, "reference": "A-101", "doc_type": "DRAWING", "title": "General arrangement plan"}
	if code, out := a.do("POST", "/v1/documents", pm, doc); code != 403 {
		t.Fatalf("the approver registered a document: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/documents", elsewhere, doc); code != 403 {
		t.Fatalf("an engineer of another project registered a document: %d %v", code, out)
	}
	code, out := a.do("POST", "/v1/documents", eng, doc)
	if code != 201 {
		t.Fatalf("document: %d %v", code, out)
	}
	id := out["id"].(string)

	// A 2 MiB file: over the ordinary 1 MiB body limit, accepted on the upload route.
	file := append([]byte("%PDF-1.7\n"), make([]byte, 2<<20)...)
	sum := sha256.Sum256(file)
	rev := map[string]any{"revision": "A", "file_name": "A-101-A.pdf", "media_type": "application/pdf", "change_note": "First issue", "content": base64.StdEncoding.EncodeToString(file)}
	if code, out := a.do("POST", "/v1/rfis", eng, map[string]any{"project_id": a.project, "subject": "Too large", "question": strings.Repeat("x", 2<<20), "assigned_to": "u-doc-planner", "due_on": "2099-01-01"}); code != 413 {
		t.Fatalf("an ordinary route accepted a 2 MiB body: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/documents/"+id+"/revisions", elsewhere, rev); code != 404 {
		t.Fatalf("upload from another project: %d %v", code, out)
	}
	bad := map[string]any{"revision": "A", "file_name": "a.pdf", "media_type": "application/pdf", "change_note": "First issue", "content": base64.StdEncoding.EncodeToString([]byte("MZ not a pdf"))}
	if code, out := a.do("POST", "/v1/documents/"+id+"/revisions", eng, bad); code != 422 {
		t.Fatalf("a file that is not what it claims: %d %v", code, out)
	}
	code, out = a.do("POST", "/v1/documents/"+id+"/revisions", eng, rev)
	if code != 201 {
		t.Fatalf("revision: %d %v", code, out)
	}
	revID := out["id"].(string)

	if code, out := a.do("POST", "/v1/document-revisions/"+revID+"/submit", eng, nil); code != 200 {
		t.Fatalf("submit: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/document-revisions/"+revID+"/approve", eng, nil); code != 403 {
		t.Fatalf("the uploader published: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/document-revisions/"+revID+"/approve", pm, nil); code != 401 {
		t.Fatalf("published without fresh MFA: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/document-revisions/"+revID+"/approve", elsewhere, nil); code != 404 {
		t.Fatalf("published from another project: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/document-revisions/"+revID+"/approve", pmMFA, nil); code != 200 {
		t.Fatalf("publish: %d %v", code, out)
	}

	// The register shows the current revision; the generic document routes know the type.
	code, out = a.do("GET", "/v1/documents?project="+a.project, planner, nil)
	docs, _ := out["documents"].([]any)
	if code != 200 || len(docs) != 1 {
		t.Fatalf("register: %d %v", code, out)
	}
	cur, _ := docs[0].(map[string]any)["current"].(map[string]any)
	if cur["id"] != revID || cur["sha256"] != hex.EncodeToString(sum[:]) || cur["status"] != "APPROVED" {
		t.Fatalf("current revision: %v", cur)
	}
	if code, out := a.do("GET", "/v1/documents/"+id, elsewhere, nil); code != 404 {
		t.Fatalf("read from another project: %d %v", code, out)
	}
	if code, out := a.do("GET", "/v1/p2p/document_revision/"+revID, planner, nil); code != 200 || out["status"] != "APPROVED" {
		t.Fatalf("revision as a document: %d %v", code, out)
	}
	if code, out := a.do("GET", "/v1/p2p/document_revision?status=APPROVED", planner, nil); code != 200 || len(out["items"].([]any)) != 1 {
		t.Fatalf("worklist: %d %v", code, out)
	}
	if code, out := a.do("GET", "/v1/p2p/document_revision?status=APPROVED", elsewhere, nil); code != 200 || out["items"] != nil && len(out["items"].([]any)) != 0 {
		t.Fatalf("worklist from another project: %d %v", code, out)
	}

	// The file: an attachment, the attested bytes, its status in a header.
	resp, body := a.raw("/v1/document-revisions/"+revID+"/content", planner)
	got := sha256.Sum256(body)
	if resp.StatusCode != http.StatusOK || got != sum || resp.Header.Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) ||
		resp.Header.Get("X-Serk-Revision-Status") != "APPROVED" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), `attachment; filename="A-101-A.pdf"`) ||
		resp.Header.Get("Content-Type") != "application/pdf" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v (%d bytes)", resp.StatusCode, resp.Header, len(body))
	}
	if resp, _ := a.raw("/v1/document-revisions/"+revID+"/content", elsewhere); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("download from another project: %d", resp.StatusCode)
	}
	if resp, _ := a.raw("/v1/document-revisions/"+revID+"/content", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("download without a token: %d", resp.StatusCode)
	}

	// RFI about that revision.
	due := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	rfi := map[string]any{"project_id": a.project, "subject": "Slab thickness at C-4", "question": "The section and the schedule disagree. Which governs?",
		"revision_id": revID, "assigned_to": "u-doc-planner", "due_on": due}
	if code, out := a.do("POST", "/v1/rfis", pm, rfi); code != 403 {
		t.Fatalf("an RFI without the permission: %d %v", code, out)
	}
	code, out = a.do("POST", "/v1/rfis", eng, rfi)
	if code != 201 {
		t.Fatalf("rfi: %d %v", code, out)
	}
	rfiID := out["id"].(string)
	if code, out := a.do("POST", "/v1/rfis/"+rfiID+"/answer", eng, map[string]any{"answer": "The schedule governs."}); code == 200 {
		t.Fatalf("the author answered their own RFI: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/rfis/"+rfiID+"/answer", elsewhere, map[string]any{"answer": "The schedule governs."}); code != 404 {
		t.Fatalf("answered from another project: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/rfis/"+rfiID+"/answer", planner, map[string]any{"answer": "The schedule governs: 250 mm."}); code != 200 {
		t.Fatalf("answer: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/rfis/"+rfiID+"/close", planner, nil); code != 403 {
		t.Fatalf("someone else closed the RFI: %d %v", code, out)
	}
	if code, out := a.do("POST", "/v1/rfis/"+rfiID+"/close", eng, nil); code != 200 {
		t.Fatalf("close: %d %v", code, out)
	}
	code, out = a.do("GET", "/v1/rfis?project="+a.project+"&status=CLOSED", planner, nil)
	rfis, _ := out["rfis"].([]any)
	if code != 200 || len(rfis) != 1 || rfis[0].(map[string]any)["answered_by"] != "u-doc-planner" {
		t.Fatalf("rfis: %d %v", code, out)
	}
	if code, out := a.do("GET", "/v1/rfis?status=NOPE", planner, nil); code != 422 {
		t.Fatalf("unknown status: %d %v", code, out)
	}
	if code, out := a.do("GET", "/v1/rfis", elsewhere, nil); code != 200 || len(out["rfis"].([]any)) != 0 {
		t.Fatalf("RFIs seen from another project: %d %v", code, out)
	}
}
