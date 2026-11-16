package kernel

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/brand"
)

// TestBranding_WordsThemeLogo (0097): a settings administrator sets the
// company's words, a readable theme and a logo; an unreadable theme, an SVG
// or an oversized file is refused; everyone in the company reads it, another
// company does not; the stored digest must match the bytes.
func TestBranding_WordsThemeLogo(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	mk := func(id, tenant string, roles ...string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: tenant, Roles: roles, Kind: auth.KindHuman, IssuedAt: now.Add(-time.Minute), MFA: true, AuthTime: now}
	}
	admin, pm := mk("u-sysadmin", f.tenant, "system_admin"), mk("u-pm", f.tenant, "project_manager")
	pm.ProjectIDs = []string{f.project}
	in := Branding{BrandName: "Senselet", BrandSuffix: "Construction PLC", Eyebrow: "Building Ethiopia since 2009", Motto: "Built right. Built on time.",
		Description: "Roads, bridges and buildings across Tigray and Amhara.", Address: "Bole Road, Addis Ababa, Ethiopia", Phone: "+251 11 000 0000",
		Email: "info@example.com", Website: "https://example.com", Theme: brand.Theme{Light: map[string]string{"accent": "#7A1F2B", "band": "#1B1B1B"}, Font: "serif"}}
	if _, err := f.svc.SaveBranding(f.ctx, pm, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager changed the branding: %v", err)
	}
	bad := in
	bad.Theme = brand.Theme{Light: map[string]string{"ink": "#cccccc"}}
	if _, err := f.svc.SaveBranding(f.ctx, admin, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unreadable theme was saved: %v", err)
	}
	bad = in
	bad.Motto = "<script>alert(1)</script>"
	if _, err := f.svc.SaveBranding(f.ctx, admin, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("markup in the motto was saved: %v", err)
	}
	out, err := f.svc.SaveBranding(f.ctx, admin, in)
	must(t, "save", err)
	if out.Version != 1 || out.Theme.Light["accent"] != "#7a1f2b" || out.UpdatedBy != "u-sysadmin" {
		t.Fatalf("saved: %+v", out)
	}
	in.Motto = "Built right."
	out, err = f.svc.SaveBranding(f.ctx, admin, in)
	must(t, "save again", err)
	if out.Version != 2 {
		t.Fatalf("version after the second save: %d", out.Version)
	}

	var img bytes.Buffer
	must(t, "png", png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 400, 160))))
	if _, err := f.svc.PutLogo(f.ctx, admin, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an SVG logo was accepted: %v", err)
	}
	if _, err := f.svc.PutLogo(f.ctx, admin, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, brand.MaxLogoBytes)...)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an 11 MB logo was accepted: %v", err)
	}
	li, err := f.svc.PutLogo(f.ctx, admin, img.Bytes())
	must(t, "logo", err)
	if li.Width != 400 || li.Height != 160 || li.ContentType != "image/png" {
		t.Fatalf("logo: %+v", li)
	}
	got, err := f.svc.Branding(f.ctx, pm)
	must(t, "read as a project manager", err)
	if got.Motto != "Built right." || got.Logo == nil || got.Logo.SHA256 != li.SHA256 {
		t.Fatalf("read back: %+v", got)
	}
	ct, sum, data, err := f.svc.LogoBytes(f.ctx, f.tenant)
	must(t, "logo bytes", err)
	if ct != "image/png" || sum != li.SHA256 || !bytes.Equal(data, img.Bytes()) {
		t.Fatal("logo bytes differ")
	}
	other, err := f.svc.Branding(f.ctx, mk("u-other", f.tenant2, "system_admin"))
	must(t, "other company", err)
	if other.Motto != "" || other.Logo != nil {
		t.Fatalf("another company sees this branding: %+v", other)
	}
	rawRefusedWith(t, f, "a logo whose digest does not match", "digest does not match",
		`UPDATE tenant_logos SET sha256 = repeat('0', 64)`)
	must(t, "remove logo", f.svc.RemoveLogo(f.ctx, admin))
	if _, _, _, err := f.svc.LogoBytes(f.ctx, f.tenant); !errors.Is(err, ErrNotFound) {
		t.Fatalf("logo after removal: %v", err)
	}
}
