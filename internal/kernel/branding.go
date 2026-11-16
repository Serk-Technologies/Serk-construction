// Company branding: name, motto, address, contact details, theme and logo (migration 0097).
package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/brand"
)

// Branding is what a company shows of itself.
type Branding struct {
	BrandName   string      `json:"brand_name"`
	BrandSuffix string      `json:"brand_suffix"`
	Eyebrow     string      `json:"eyebrow"`
	Motto       string      `json:"motto"`
	Description string      `json:"description"`
	Address     string      `json:"address"`
	Phone       string      `json:"phone"`
	Email       string      `json:"email"`
	Website     string      `json:"website"`
	Theme       brand.Theme `json:"theme"`
	// Read-only: the logo's identity and the record's history.
	Logo      *LogoInfo  `json:"logo,omitempty"`
	Version   int        `json:"version"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// LogoInfo identifies the current logo (not its bytes).
type LogoInfo struct {
	ContentType string    `json:"content_type"`
	SHA256      string    `json:"sha256"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Bytes       int       `json:"bytes"`
	UpdatedBy   string    `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func readBranding(ctx context.Context, tx pgx.Tx) (Branding, error) {
	var b Branding
	var theme []byte
	var by string
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT brand_name, brand_suffix, eyebrow, motto, description, address, phone, email, website, theme, version, updated_by, updated_at
		FROM tenant_branding`).Scan(&b.BrandName, &b.BrandSuffix, &b.Eyebrow, &b.Motto, &b.Description, &b.Address, &b.Phone, &b.Email, &b.Website, &theme, &b.Version, &by, &at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return b, err
	default:
		b.UpdatedBy, b.UpdatedAt = by, &at
		if err := json.Unmarshal(theme, &b.Theme); err != nil {
			return b, err
		}
	}
	var l LogoInfo
	var ct, sum *string
	var w, h *int
	err = tx.QueryRow(ctx, `SELECT content_type, sha256, width, height, COALESCE(octet_length(data), 0), updated_by, updated_at FROM tenant_logos`).
		Scan(&ct, &sum, &w, &h, &l.Bytes, &l.UpdatedBy, &l.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return b, err
	case ct != nil:
		l.ContentType, l.SHA256, l.Width, l.Height = *ct, *sum, *w, *h
		b.Logo = &l
	}
	return b, nil
}

// Branding returns the company's branding (anyone signed in to it).
func (s *Service) Branding(ctx context.Context, p auth.Principal) (Branding, error) {
	var out Branding
	err := s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = readBranding(ctx, tx)
		return err
	})
	return out, err
}

// PublicBranding returns a company's branding without a principal, for the
// sign-in page of a deployment configured to show that company.
func (s *Service) PublicBranding(ctx context.Context, tenantID string) (Branding, error) {
	var out Branding
	err := s.store.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = readBranding(ctx, tx)
		return err
	})
	out.UpdatedBy = ""
	if out.Logo != nil {
		out.Logo.UpdatedBy = ""
	}
	return out, err
}

func (s *Service) brandTx(ctx context.Context, p auth.Principal, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := s.authz.Require(p, auth.PermTenantSettingsManage, p.TenantID); err != nil {
		return err
	}
	if !s.tenantWide(p) {
		return fmt.Errorf("%w: branding needs company-wide scope", auth.ErrForbidden)
	}
	return s.ownTx(ctx, p, fn)
}

func clip(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > max || strings.ContainsAny(v, "<>") {
		return "", fmt.Errorf("%w: %s is at most %d characters, without < or >", ErrInvalidInput, field, max)
	}
	return v, nil
}

// SaveBranding saves the company's words and theme (tenant.settings.manage).
// The theme must stay readable (brand.Validate).
func (s *Service) SaveBranding(ctx context.Context, p auth.Principal, in Branding) (Branding, error) {
	var err error
	for _, f := range []struct {
		name string
		v    *string
		max  int
	}{{"brand name", &in.BrandName, 80}, {"brand suffix", &in.BrandSuffix, 40}, {"eyebrow", &in.Eyebrow, 80}, {"motto", &in.Motto, 120},
		{"description", &in.Description, 300}, {"address", &in.Address, 500}, {"phone", &in.Phone, 40}, {"e-mail", &in.Email, 254}, {"website", &in.Website, 200}} {
		if *f.v, err = clip(f.name, *f.v, f.max); err != nil {
			return Branding{}, err
		}
	}
	if in.Website != "" && !strings.HasPrefix(in.Website, "https://") {
		return Branding{}, fmt.Errorf("%w: the website starts with https://", ErrInvalidInput)
	}
	if err := brand.Validate(&in.Theme); err != nil {
		return Branding{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	theme, err := json.Marshal(in.Theme)
	if err != nil {
		return Branding{}, err
	}
	var out Branding
	err = s.brandTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_branding (brand_name, brand_suffix, eyebrow, motto, description, address, phone, email, website, theme, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (tenant_id) DO UPDATE SET brand_name = EXCLUDED.brand_name, brand_suffix = EXCLUDED.brand_suffix, eyebrow = EXCLUDED.eyebrow,
			  motto = EXCLUDED.motto, description = EXCLUDED.description, address = EXCLUDED.address, phone = EXCLUDED.phone, email = EXCLUDED.email,
			  website = EXCLUDED.website, theme = EXCLUDED.theme, updated_by = EXCLUDED.updated_by`,
			in.BrandName, in.BrandSuffix, in.Eyebrow, in.Motto, in.Description, in.Address, in.Phone, in.Email, in.Website, theme, p.UserID); err != nil {
			return err
		}
		var err error
		if out, err = readBranding(ctx, tx); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "branding.save", "tenant_branding", p.TenantID, "branding.saved", map[string]any{"version": out.Version, "motto": out.Motto})
	})
	return out, err
}

// PutLogo replaces the company's logo: PNG, JPEG or WebP, at most 10 MB,
// identified by its content (brand.Logo), never by the name or claimed type.
func (s *Service) PutLogo(ctx context.Context, p auth.Principal, data []byte) (LogoInfo, error) {
	ct, w, h, err := brand.Logo(data)
	if err != nil {
		return LogoInfo{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	var out LogoInfo
	err = s.brandTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_logos (content_type, data, sha256, width, height, updated_by) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id) DO UPDATE SET content_type = EXCLUDED.content_type, data = EXCLUDED.data, sha256 = EXCLUDED.sha256,
			  width = EXCLUDED.width, height = EXCLUDED.height, updated_by = EXCLUDED.updated_by`, ct, data, digest, w, h, p.UserID); err != nil {
			return err
		}
		out = LogoInfo{ContentType: ct, SHA256: digest, Width: w, Height: h, Bytes: len(data), UpdatedBy: p.UserID, UpdatedAt: time.Now()}
		return s.record(ctx, tx, p, "branding.logo", "tenant_branding", p.TenantID, "branding.logo_replaced", map[string]any{"sha256": digest, "bytes": len(data), "width": w, "height": h})
	})
	return out, err
}

// RemoveLogo removes the company's logo (the product mark is shown instead).
func (s *Service) RemoveLogo(ctx context.Context, p auth.Principal) error {
	return s.brandTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE tenant_logos SET content_type = NULL, data = NULL, sha256 = NULL, width = NULL, height = NULL, updated_by = $1`, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "branding.logo", "tenant_branding", p.TenantID, "branding.logo_removed", nil)
	})
}

// LogoBytes returns a company's logo, or ErrNotFound when it has none. With
// an empty principal it serves the sign-in page's configured company.
func (s *Service) LogoBytes(ctx context.Context, tenantID string) (contentType, digest string, data []byte, err error) {
	err = s.store.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var ct, sum *string
		if err := tx.QueryRow(ctx, `SELECT content_type, sha256, data FROM tenant_logos`).Scan(&ct, &sum, &data); err != nil {
			return mapNoRows(err)
		}
		if ct == nil {
			return ErrNotFound
		}
		contentType, digest = *ct, *sum
		return nil
	})
	return
}
