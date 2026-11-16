// Package brand validates a company's theme and logo (migration 0097). It
// does no I/O.
//
// A theme customises the console's colour tokens for light and dark mode,
// the corner radius and the font. Every colour is #rrggbb; the pairs people
// read (text on its background) must keep WCAG 2.2 contrast of at least
// 4.5:1, and large or non-text pairs (accent on page, status colours) at
// least 3:1, so a company cannot make its own system unreadable.
package brand

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register the decoders DecodeConfig needs
	_ "image/png"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Tokens are the customisable colours, in the console's stylesheet names.
var Tokens = []string{"bg", "panel", "panel-2", "ink", "muted", "line", "accent", "accent-ink", "band", "band-ink", "band-muted", "ok", "warn", "bad", "info"}

// Defaults are the product's own colours (the stylesheet's), per mode.
var Defaults = map[string]map[string]string{
	"light": {"bg": "#f7f9f8", "panel": "#ffffff", "panel-2": "#eef3f0", "ink": "#14201c", "muted": "#52635d", "line": "#e0e7e3",
		"accent": "#103e32", "accent-ink": "#ffffff", "band": "#103e32", "band-ink": "#ffffff", "band-muted": "#c9ddd3",
		"ok": "#16794a", "warn": "#946000", "bad": "#b42318", "info": "#175cd3"},
	"dark": {"bg": "#0e1512", "panel": "#151e1a", "panel-2": "#1c2823", "ink": "#eaf1ed", "muted": "#a9b8b1", "line": "#2a3a33",
		"accent": "#8fd9b6", "accent-ink": "#0e1512", "band": "#0a100e", "band-ink": "#eaf1ed", "band-muted": "#a9b8b1",
		"ok": "#7fd6a4", "warn": "#f2c879", "bad": "#f7a59b", "info": "#9cc2ff"},
}

// Fonts are the font choices (keys) and their CSS stacks.
var Fonts = map[string]string{
	"manrope": `"Manrope", system-ui, -apple-system, "Segoe UI", Roboto, "Noto Sans", "Noto Sans Ethiopic", sans-serif`,
	"system":  `system-ui, -apple-system, "Segoe UI", Roboto, "Noto Sans", "Noto Sans Ethiopic", sans-serif`,
	"serif":   `Georgia, "Times New Roman", "Noto Serif", "Noto Serif Ethiopic", serif`,
	"rounded": `ui-rounded, "SF Pro Rounded", "Nunito", system-ui, sans-serif`,
}

// Theme is a company's theme: per-mode colour overrides, radius and font.
type Theme struct {
	Light  map[string]string `json:"light,omitempty"`
	Dark   map[string]string `json:"dark,omitempty"`
	Radius *int              `json:"radius,omitempty"` // px, 0..24
	Font   string            `json:"font,omitempty"`   // a key of Fonts
}

var hexRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// pairs are the foreground/background pairs checked, with their minimum ratio.
var pairs = []struct {
	fg, bg string
	min    float64
}{
	{"ink", "bg", 4.5}, {"ink", "panel", 4.5}, {"ink", "panel-2", 4.5}, {"muted", "panel", 4.5}, {"muted", "bg", 4.5},
	{"accent-ink", "accent", 4.5}, {"band-ink", "band", 4.5}, {"band-muted", "band", 3},
	{"accent", "panel", 3}, {"ok", "panel", 3}, {"warn", "panel", 3}, {"bad", "panel", 3}, {"info", "panel", 3},
}

// Validate normalises a theme (lower-case colours) and checks it.
func Validate(t *Theme) error {
	known := map[string]bool{}
	for _, k := range Tokens {
		known[k] = true
	}
	for _, mode := range []string{"light", "dark"} {
		m := t.Light
		if mode == "dark" {
			m = t.Dark
		}
		for k, v := range m {
			if !known[k] {
				return fmt.Errorf("unknown colour %q in %s mode", k, mode)
			}
			v = strings.ToLower(strings.TrimSpace(v))
			if !hexRe.MatchString(v) {
				return fmt.Errorf("%s colour %q is not #rrggbb", mode, k)
			}
			m[k] = v
		}
		eff := Effective(*t, mode)
		var bad []string
		for _, p := range pairs {
			if r := Contrast(eff[p.fg], eff[p.bg]); r < p.min {
				bad = append(bad, fmt.Sprintf("%s on %s is %.2f:1 (needs %.1f:1)", p.fg, p.bg, r, p.min))
			}
		}
		if len(bad) > 0 {
			sort.Strings(bad)
			return fmt.Errorf("%s mode is hard to read: %s", mode, strings.Join(bad, "; "))
		}
	}
	if t.Radius != nil && (*t.Radius < 0 || *t.Radius > 24) {
		return errors.New("corner radius is 0 to 24 px")
	}
	if t.Font != "" {
		if _, ok := Fonts[t.Font]; !ok {
			return fmt.Errorf("unknown font %q", t.Font)
		}
	}
	return nil
}

// Effective is a mode's colours: the defaults with the company's overrides.
func Effective(t Theme, mode string) map[string]string {
	out := map[string]string{}
	for k, v := range Defaults[mode] {
		out[k] = v
	}
	over := t.Light
	if mode == "dark" {
		over = t.Dark
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func channel(c uint8) float64 {
	v := float64(c) / 255
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func luminance(hex string) float64 {
	var r, g, b uint8
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return 0
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// Contrast is the WCAG contrast ratio of two #rrggbb colours.
func Contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// MaxLogoBytes is the largest logo accepted: 10 MB.
const MaxLogoBytes = 10 << 20

// Logo identifies an uploaded logo by its content, never by the name or the
// type the client claims. It returns the media type and the pixel size.
func Logo(data []byte) (contentType string, width, height int, err error) {
	switch {
	case len(data) == 0:
		return "", 0, 0, errors.New("the logo file is empty")
	case len(data) > MaxLogoBytes:
		return "", 0, 0, fmt.Errorf("the logo is %.1f MB; the limit is 10 MB", float64(len(data))/(1<<20))
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		contentType = "image/png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		contentType = "image/jpeg"
	case len(data) >= 30 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		w, h, err := webpSize(data)
		if err != nil {
			return "", 0, 0, err
		}
		return "image/webp", w, h, checkSize(w, h)
	default:
		return "", 0, 0, errors.New("the logo must be a PNG, JPEG or WebP image (SVG and other formats are refused)")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, fmt.Errorf("the logo is not a readable %s image", strings.TrimPrefix(contentType, "image/"))
	}
	return contentType, cfg.Width, cfg.Height, checkSize(cfg.Width, cfg.Height)
}

func checkSize(w, h int) error {
	if w < 16 || h < 16 || w > 20000 || h > 20000 {
		return fmt.Errorf("the logo is %dx%d pixels; it must be 16 to 20,000 pixels on each side", w, h)
	}
	return nil
}

// webpSize reads the canvas size from a WebP header (VP8, VP8L or VP8X).
func webpSize(d []byte) (int, int, error) {
	bad := errors.New("the logo is not a readable WebP image")
	switch string(d[12:16]) {
	case "VP8X":
		w := 1 + (int(d[24]) | int(d[25])<<8 | int(d[26])<<16)
		h := 1 + (int(d[27]) | int(d[28])<<8 | int(d[29])<<16)
		return w, h, nil
	case "VP8L":
		if d[20] != 0x2f {
			return 0, 0, bad
		}
		b := binary.LittleEndian.Uint32(d[21:25])
		return int(b&0x3fff) + 1, int((b>>14)&0x3fff) + 1, nil
	case "VP8 ":
		if d[23] != 0x9d || d[24] != 0x01 || d[25] != 0x2a {
			return 0, 0, bad
		}
		return int(binary.LittleEndian.Uint16(d[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(d[28:30]) & 0x3fff), nil
	}
	return 0, 0, bad
}
