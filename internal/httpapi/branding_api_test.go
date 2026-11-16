package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAPIBranding (0097): words and theme are saved by a settings
// administrator; the logo is uploaded as the raw body, served only as the
// image type the server identified, with nosniff and a sandbox policy; the
// sign-in page reads the configured company's branding before sign-in.
func TestAPIBranding(t *testing.T) {
	e := newEnv(t)
	h := New(Config{Verifier: testVerifier, Kernel: e.svc, Store: e.db.Router, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RatePerMinute: 1000, BrandTenant: e.tenant})
	srv := httptest.NewServer(h)
	defer srv.Close()
	e.srv = srv
	admin := e.tok("u-brand-admin", nil, true, "system_admin")
	auditor := e.tok("u-brand-auditor", nil, false, "auditor")
	body := map[string]any{"brand_name": "Senselet", "brand_suffix": "Construction PLC", "motto": "Built right. Built on time.", "address": "Bole Road, Addis Ababa",
		"theme": map[string]any{"light": map[string]string{"accent": "#7A1F2B"}, "radius": 6, "font": "serif"}}
	if st, _ := e.do("PUT", "/v1/branding", auditor, body); st != 403 {
		t.Fatalf("an auditor changed the branding: %d", st)
	}
	if st, out := e.do("PUT", "/v1/branding", admin, map[string]any{"theme": map[string]any{"light": map[string]string{"ink": "#dddddd"}}}); st != 422 {
		t.Fatalf("unreadable theme: %d %v", st, out)
	}
	if st, out := e.do("PUT", "/v1/branding", admin, body); st != 200 || out["version"] != float64(1) {
		t.Fatalf("save: %d %v", st, out)
	}
	upload := func(tok string, data []byte) *http.Response {
		req, _ := http.NewRequest("PUT", srv.URL+"/v1/branding/logo", bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "image/png")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 300, 100))); err != nil {
		t.Fatal(err)
	}
	if r := upload(admin, []byte(`<svg onload="alert(1)"/>`)); r.StatusCode != 422 {
		t.Fatalf("SVG upload: %d", r.StatusCode)
	}
	if r := upload(admin, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 10<<20+10)...)); r.StatusCode != 413 && r.StatusCode != 422 {
		t.Fatalf("11 MB upload: %d", r.StatusCode)
	}
	if r := upload(admin, img.Bytes()); r.StatusCode != 200 {
		t.Fatalf("logo upload: %d", r.StatusCode)
	}
	for _, path := range []string{"/v1/branding/logo", "/branding/logo"} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if path[1] == 'v' {
			req.Header.Set("Authorization", "Bearer "+auditor)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Equal(got, img.Bytes()) || resp.Header.Get("Content-Type") != "image/png" ||
			resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: %d %s %q", path, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("X-Content-Type-Options"))
		}
	}
	st, pub := e.do("GET", "/branding", "", nil)
	if st != 200 || pub["motto"] != "Built right. Built on time." || pub["updated_by"] != nil && pub["updated_by"] != "" {
		t.Fatalf("public branding: %d %v", st, pub)
	}
	if logo, _ := pub["logo"].(map[string]any); logo == nil || logo["width"] != float64(300) || logo["updated_by"] != nil && logo["updated_by"] != "" {
		t.Fatalf("public logo: %v", pub["logo"])
	}
}
