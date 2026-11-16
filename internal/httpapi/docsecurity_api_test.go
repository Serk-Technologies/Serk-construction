package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/serk-group/serk-construction/internal/testdb"
)

func (e *env) send(method, path, token, contentType string, body []byte) (int, map[string]any, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, raw
}

// Document security through the API (0053, ADR 0019): streamed upload,
// quarantine answers, download links that work without the bearer token and
// only for their file, and who may read and change the scan policy. The
// scanner here is the labelled test double.
func TestAPIDocumentSecurity(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	eng := a.tok("u-ds-eng", P, false, "site_engineer")
	planner := a.tok("u-ds-planner", P, false, "planner")
	pm := a.tok("u-ds-pm", P, true, "project_manager")
	elsewhere := a.tok("u-ds-else", []string{uuid.NewString()}, true, "site_engineer", "project_manager")
	sec, secMFA := a.tok("u-ds-sec", nil, false, "security_admin"), a.tok("u-ds-sec", nil, true, "security_admin")

	code, out := a.do("POST", "/v1/documents", eng, map[string]any{"project_id": a.project, "reference": "N-1", "doc_type": "REPORT", "title": "Site notes"})
	if code != 201 {
		t.Fatalf("document: %d %v", code, out)
	}
	doc := out["id"].(string)
	up := func(rev, token, ctype string, body []byte) (int, map[string]any) {
		q := url.Values{"file_name": {"notes-" + rev + ".txt"}, "change_note": {"Streamed upload " + rev}}
		c, o, _ := a.send("PUT", "/v1/documents/"+doc+"/revisions/"+rev+"/file?"+q.Encode(), token, ctype, body)
		return c, o
	}
	// Streamed upload: 3 MiB of text, well over the ordinary body limit.
	big := bytes.Repeat([]byte("site note line\n"), 3<<20/15)
	if c, o := up("A", "", "text/plain", big); c != 401 {
		t.Fatalf("upload without a token: %d %v", c, o)
	}
	if c, o := up("A", elsewhere, "text/plain", big); c != 404 {
		t.Fatalf("upload from another project: %d %v", c, o)
	}
	if c, o := up("A", pm, "text/plain", big); c != 403 {
		t.Fatalf("upload without the permission: %d %v", c, o)
	}
	if c, o := up("A", eng, "image/svg+xml", []byte("<svg/>")); c != 422 {
		t.Fatalf("an SVG was accepted: %d %v", c, o)
	}
	if c, o := up("A", eng, "text/plain", []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>")); c != 422 {
		t.Fatalf("an SVG disguised as text was accepted: %d %v", c, o)
	}
	c, o := up("A", eng, "text/plain", big)
	if c != 201 {
		t.Fatalf("streamed upload: %d %v", c, o)
	}
	rev := o["id"].(string)

	// Download links.
	if c, o := a.do("POST", "/v1/document-revisions/"+rev+"/download-link", elsewhere, nil); c != 404 {
		t.Fatalf("a link from another project: %d %v", c, o)
	}
	c, o = a.do("POST", "/v1/document-revisions/"+rev+"/download-link", planner, nil)
	if c != 200 || !strings.HasPrefix(o["url"].(string), "/v1/dl/") || o["expires_at"] == "" {
		t.Fatalf("link: %d %v", c, o)
	}
	link := o["url"].(string)
	resp, body := a.raw(link, "") // no bearer token
	if resp.StatusCode != 200 || !bytes.Equal(body, big) || resp.Header.Get("X-Serk-Scan-Status") != "CLEAN" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("link download: %d %v (%d bytes)", resp.StatusCode, resp.Header, len(body))
	}
	flip := map[byte]string{'0': "1"}[link[len(link)-1]]
	if flip == "" {
		flip = "0"
	}
	for name, bad := range map[string]string{"truncated": link[:len(link)-3], "altered": link[:len(link)-1] + flip, "made up": "/v1/dl/abc.def"} {
		if resp, _ := a.raw(bad, ""); resp.StatusCode != 404 {
			t.Fatalf("%s link answered %d", name, resp.StatusCode)
		}
	}
	if resp, _ := a.raw("/v1/document-revisions/"+rev+"/content", ""); resp.StatusCode != 401 {
		t.Fatalf("content without a token: %d", resp.StatusCode)
	}

	// Infected upload: recorded, quarantined everywhere.
	c, o = up("B", eng, "text/plain", []byte(testdb.EICAR))
	if c != 201 {
		t.Fatalf("infected upload: %d %v", c, o)
	}
	bad := o["id"].(string)
	if c, o := a.do("POST", "/v1/document-revisions/"+bad+"/submit", eng, nil); c != 409 || o["error"].(map[string]any)["code"] != "quarantined" {
		t.Fatalf("submit infected: %d %v", c, o)
	}
	if resp, _ := a.raw("/v1/document-revisions/"+bad+"/content", eng); resp.StatusCode != 409 {
		t.Fatalf("download infected: %d", resp.StatusCode)
	}
	if c, o := a.do("POST", "/v1/document-revisions/"+bad+"/download-link", eng, nil); c != 409 {
		t.Fatalf("link for infected: %d %v", c, o)
	}
	if c, o := a.do("POST", "/v1/document-revisions/"+bad+"/rescan", eng, nil); c != 422 {
		t.Fatalf("rescan of a final verdict: %d %v", c, o)
	}
	if c, o := a.do("POST", "/v1/document-revisions/"+bad+"/discard", planner, map[string]any{"reason": "Infected file"}); c != 403 {
		t.Fatalf("discard by someone else: %d %v", c, o)
	}
	if c, o := a.do("POST", "/v1/document-revisions/"+bad+"/discard", eng, map[string]any{"reason": "Infected file"}); c != 200 {
		t.Fatalf("discard: %d %v", c, o)
	}
	c, o = a.do("GET", "/v1/documents/"+doc, planner, nil)
	revs := o["revisions"].([]any)
	if c != 200 || len(revs) != 2 {
		t.Fatalf("document: %d %v", c, o)
	}
	for _, r := range revs {
		m := r.(map[string]any)
		if m["id"] == bad && (m["scan_status"] != "INFECTED" || m["status"] != "REJECTED") || m["id"] == rev && m["scan_status"] != "CLEAN" {
			t.Fatalf("revision: %v", m)
		}
		if _, leaked := m["scan_proof"]; leaked {
			t.Fatal("the scan proof is exposed by the API")
		}
	}

	// Policy: read by project staff; changed only by security administration with fresh MFA.
	c, o = a.do("GET", "/v1/document-policy", eng, nil)
	if c != 200 || o["scan_required"] != true || o["scanner_configured"] != false || o["storage"] != "postgres" {
		t.Fatalf("policy: %d %v", c, o)
	}
	change := map[string]any{"scan_required": false, "reason": "No scanner at the pilot site yet"}
	if c, o := a.do("PUT", "/v1/document-policy", pm, change); c != 403 {
		t.Fatalf("policy changed by a project manager: %d %v", c, o)
	}
	if c, o := a.do("PUT", "/v1/document-policy", sec, change); c != 401 {
		t.Fatalf("policy changed without fresh MFA: %d %v", c, o)
	}
	if c, o := a.do("PUT", "/v1/document-policy", secMFA, map[string]any{"scan_required": false, "reason": "x"}); c != 422 {
		t.Fatalf("policy changed without a reason: %d %v", c, o)
	}
	if c, o := a.do("PUT", "/v1/document-policy", secMFA, change); c != 200 {
		t.Fatalf("policy change: %d %v", c, o)
	}
	if c, o := a.do("GET", "/v1/document-policy", eng, nil); c != 200 || o["scan_required"] != false || o["updated_by"] != "u-ds-sec" {
		t.Fatalf("policy after the change: %d %v", c, o)
	}
	// Storage migration: not for project staff; refused where there is no object store.
	if c, o := a.do("POST", "/v1/document-storage/migrate", eng, nil); c != 403 {
		t.Fatalf("migration by an engineer: %d %v", c, o)
	}
	if c, o := a.do("POST", "/v1/document-storage/migrate", sec, nil); c != 422 {
		t.Fatalf("migration without an object store: %d %v", c, o)
	}
}
