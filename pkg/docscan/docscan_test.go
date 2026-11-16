package docscan

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string, store bool) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if store {
			h.Method = zip.Store
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	_ = w.Close()
	return b.Bytes()
}

const docx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func TestInspect(t *testing.T) {
	office := map[string]string{"[Content_Types].xml": "<Types/>", "word/document.xml": "<w:document/>"}
	with := func(extra string, body string) map[string]string {
		m := map[string]string{extra: body}
		for k, v := range office {
			m[k] = v
		}
		return m
	}
	ok := map[string]struct {
		media string
		body  []byte
	}{
		"pdf":   {"application/pdf", []byte("%PDF-1.7\n1 0 obj << /Type /Catalog /OpenAction [3 0 R /Fit] >> endobj")},
		"png":   {"image/png", []byte("\x89PNG\r\n\x1a\nrest")},
		"text":  {"text/plain", []byte("site notes\nline 2")},
		"csv":   {"text/csv", []byte("a,b\n1,2\n")},
		"docx":  {docx, zipOf(t, office, false)},
		"ifc":   {"application/x-step", []byte("ISO-10303-21;\nHEADER;")},
		"large": {"application/pdf", append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 700<<10)...)},
	}
	for name, c := range ok {
		if err := Inspect(c.media, bytes.NewReader(c.body), int64(len(c.body))); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	// A token that straddles the scanner's block boundary is still found.
	straddle := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 256<<10-9-5)...)
	straddle = append(straddle, []byte("/JavaScript (app.alert(1))")...)
	bomb := zipOf(t, with("word/media/a.bin", strings.Repeat("\x00", 40<<20)), false)
	bad := map[string]struct {
		media string
		body  []byte
		why   string
	}{
		"svg is not accepted":            {"image/svg+xml", []byte("<svg onload=alert(1)>"), "not accepted"},
		"html is not accepted":           {"text/html", []byte("<html>"), "not accepted"},
		"zip is not accepted":            {"application/zip", zipOf(t, office, false), "not accepted"},
		"executable named pdf":           {"application/pdf", []byte("MZ\x90\x00"), "not application/pdf"},
		"svg disguised as text":          {"text/plain", []byte("  <svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>"), "not text/plain"},
		"html disguised as csv":          {"text/csv", []byte("<!DOCTYPE html><script>1</script>"), "not text/csv"},
		"binary as text":                 {"text/plain", []byte("a\x00b"), "not text/plain"},
		"empty":                          {"text/plain", nil, "empty"},
		"pdf with javascript":            {"application/pdf", []byte("%PDF-1.7\n<< /S /JavaScript /JS (app.alert(1)) >>"), "active content"},
		"pdf with javascript at a seam":  {"application/pdf", straddle, "active content"},
		"pdf that launches a program":    {"application/pdf", []byte("%PDF-1.7\n<< /S /Launch /F (cmd.exe) >>"), "active content"},
		"pdf with an embedded file":      {"application/pdf", []byte("%PDF-1.7\n<< /Type /EmbeddedFile >>"), "active content"},
		"office file with macros":        {docx, zipOf(t, with("word/vbaProject.bin", "x"), false), "macros"},
		"office file with an OLE object": {docx, zipOf(t, with("word/embeddings/oleObject1.bin", "x"), false), "macros"},
		"path traversal":                 {docx, zipOf(t, with("../../etc/cron.d/x", "x"), false), "leaves its folder"},
		"absolute path":                  {docx, zipOf(t, with("/etc/passwd", "x"), false), "absolute path"},
		"windows path":                   {docx, zipOf(t, with("C:\\Windows\\x", "x"), false), "absolute path"},
		"archive bomb":                   {docx, bomb, "archive bomb"},
		"zip that is not office":         {docx, zipOf(t, map[string]string{"a.txt": "x"}, false), "not an Office file"},
		"truncated zip":                  {docx, zipOf(t, office, false)[:40], "not a readable Office file"},
	}
	for name, c := range bad {
		err := Inspect(c.media, bytes.NewReader(c.body), int64(len(c.body)))
		if !errors.Is(err, ErrUnsafe) || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v (want refusal mentioning %q)", name, err, c.why)
		}
	}
	if len(bomb) > 1<<20 {
		t.Fatalf("the bomb fixture is not small (%d bytes)", len(bomb))
	}
	many := map[string]string{"[Content_Types].xml": "<Types/>"}
	for i := 0; i <= MaxZipEntries; i++ {
		many["p/"+strings.Repeat("a", i%7)+string(rune('a'+i%26))+strings.Repeat("0", 0)+itoa(i)] = ""
	}
	b := zipOf(t, many, true)
	if err := Inspect(docx, bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrUnsafe) || !strings.Contains(err.Error(), "parts") {
		t.Errorf("too many parts: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

// TestClamdAgainstARealDaemon runs against a ClamAV daemon when
// SERK_TEST_CLAMD (host:port) is set. Without it the test is skipped and the
// clamd client is NOT verified by the run. The evidence runs of this project
// use a real clamd with a signature database that holds only the EICAR test
// file's hash (the public signature feed is not reachable from the build
// environment), so they prove the protocol and the fail-closed handling, not
// detection coverage.
func TestClamdAgainstARealDaemon(t *testing.T) {
	addr := os.Getenv("SERK_TEST_CLAMD")
	if addr == "" {
		t.Skip("SERK_TEST_CLAMD is not set: the clamd client was not exercised against a daemon")
	}
	ctx := context.Background()
	c := Clamd{Network: "tcp", Address: addr}
	if !c.Operational() {
		t.Fatal("clamd must be an operational scanner")
	}
	ver, err := c.Version(ctx)
	if err != nil || !strings.HasPrefix(ver, "ClamAV ") {
		t.Fatalf("version: %q %v", ver, err)
	}
	v, err := c.Scan(ctx, strings.NewReader("an ordinary site report"))
	if err != nil || !v.Clean || v.Engine != ver {
		t.Fatalf("clean file: %+v %v", v, err)
	}
	eicar := `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`
	v, err = c.Scan(ctx, strings.NewReader(eicar))
	if err != nil || v.Clean || v.Signature == "" {
		t.Fatalf("EICAR test file: %+v %v", v, err)
	}
	// Larger than one protocol chunk, clean.
	v, err = c.Scan(ctx, bytes.NewReader(bytes.Repeat([]byte("abcdefgh"), 100<<10)))
	if err != nil || !v.Clean {
		t.Fatalf("800 KiB clean file: %+v %v", v, err)
	}
	// Over the daemon's stream limit: an error, never clean. The limit is the
	// daemon's setting (StreamMaxLength), so the check runs when it is stated.
	if mb, _ := strconv.Atoi(os.Getenv("SERK_TEST_CLAMD_STREAM_MAX_MB")); mb > 0 {
		if v, err := c.Scan(ctx, bytes.NewReader(make([]byte, (mb+1)<<20))); err == nil {
			t.Fatalf("a file the daemon refused to scan was reported as %+v", v)
		}
	}
	// No daemon: an error, never clean.
	if v, err := (Clamd{Network: "tcp", Address: "127.0.0.1:1"}).Scan(ctx, strings.NewReader("x")); err == nil {
		t.Fatalf("an unreachable daemon produced %+v", v)
	}
}
