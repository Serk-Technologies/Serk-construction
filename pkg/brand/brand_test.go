package brand

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestDefaultsAreReadable(t *testing.T) {
	if err := Validate(&Theme{}); err != nil {
		t.Fatalf("the product's own colours fail the checks: %v", err)
	}
}

func TestValidate(t *testing.T) {
	ok := Theme{Light: map[string]string{"accent": "#7A1F2B", "band": "#111111", "band-ink": "#FFFFFF"}, Font: "serif"}
	if err := Validate(&ok); err != nil || ok.Light["accent"] != "#7a1f2b" {
		t.Fatalf("a readable theme: %v %v", err, ok.Light)
	}
	r30 := 30
	for name, th := range map[string]Theme{
		"grey text on white":  {Light: map[string]string{"ink": "#bbbbbb"}},
		"white on yellow":     {Light: map[string]string{"accent": "#ffe066", "accent-ink": "#ffffff"}},
		"unknown token":       {Light: map[string]string{"sidebar-glow": "#000000"}},
		"named colour":        {Light: map[string]string{"accent": "red"}},
		"short hex":           {Light: map[string]string{"accent": "#123"}},
		"unreadable dark":     {Dark: map[string]string{"ink": "#1c2823"}},
		"radius out of range": {Radius: &r30},
		"unknown font":        {Font: "comic"},
	} {
		th := th
		if err := Validate(&th); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if c := Contrast("#000000", "#ffffff"); c < 20.9 || c > 21.1 {
		t.Fatalf("black on white is %.2f:1", c)
	}
}

func TestLogo(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 320, 120))); err != nil {
		t.Fatal(err)
	}
	ct, w, h, err := Logo(buf.Bytes())
	if err != nil || ct != "image/png" || w != 320 || h != 120 {
		t.Fatalf("png: %s %dx%d %v", ct, w, h, err)
	}
	// A VP8X WebP header for a 640x200 canvas.
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00"), 0x7f, 0x02, 0x00, 0xc7, 0x00, 0x00)
	if ct, w, h, err := Logo(webp); err != nil || ct != "image/webp" || w != 640 || h != 200 {
		t.Fatalf("webp: %s %dx%d %v", ct, w, h, err)
	}
	for name, d := range map[string][]byte{
		"svg":        []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"empty":      nil,
		"too large":  append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxLogoBytes)...),
		"html":       []byte("<html>"),
		"broken png": []byte("\x89PNG\r\n\x1a\nnot really"),
	} {
		if _, _, _, err := Logo(d); err == nil {
			t.Errorf("%s: accepted", name)
		} else if name == "too large" && !strings.Contains(err.Error(), "10 MB") {
			t.Errorf("too large: %v", err)
		}
	}
	var tiny bytes.Buffer
	_ = png.Encode(&tiny, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if _, _, _, err := Logo(tiny.Bytes()); err == nil {
		t.Error("an 8x8 logo was accepted")
	}
}
