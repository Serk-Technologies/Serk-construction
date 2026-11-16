package httpapi

// Company branding (0097).
//
//	GET  /v1/branding                     the company's branding (anyone signed in)
//	PUT  /v1/branding                     words and theme (tenant.settings.manage)
//	PUT  /v1/branding/logo                the logo as the raw body: PNG, JPEG or WebP, at most 10 MB
//	POST /v1/branding/logo/remove
//	GET  /v1/branding/logo                the logo's bytes
//	GET  /branding                        the sign-in page's company (public; the deployment's configured company)
//	GET  /branding/logo                   its logo (public)

import (
	"errors"
	"io"
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/brand"
)

func (s *Server) registerBranding() {
	m, k := s.mux, s.cfg.Kernel
	m.Handle("GET /v1/branding", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.Branding(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("PUT /v1/branding", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.Branding
		if !decode(w, r, &b) {
			return
		}
		out, err := k.SaveBranding(r.Context(), principalFrom(r.Context()), b)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("PUT /v1/branding/logo", s.authed(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, brand.MaxLogoBytes+1))
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "the logo is larger than 10 MB")
				return
			}
			writeErr(w, http.StatusBadRequest, "validation", "could not read the logo")
			return
		}
		out, err := k.PutLogo(r.Context(), principalFrom(r.Context()), data)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/branding/logo/remove", s.authed(func(w http.ResponseWriter, r *http.Request) {
		if err := k.RemoveLogo(r.Context(), principalFrom(r.Context())); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	serveLogo := func(w http.ResponseWriter, r *http.Request, tenant string) {
		ct, sum, data, err := k.LogoBytes(r.Context(), tenant)
		if err != nil {
			if errors.Is(err, kernel.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "not_found", "no logo")
				return
			}
			s.fail(w, err)
			return
		}
		etag := `"` + sum + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=300")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// Only image types the server identified itself; never sniffed, never run.
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Disposition", "inline")
		_, _ = w.Write(data)
	}
	m.Handle("GET /v1/branding/logo", s.authed(func(w http.ResponseWriter, r *http.Request) {
		serveLogo(w, r, principalFrom(r.Context()).TenantID)
	}))
	public := s.cfg.BrandTenant
	m.HandleFunc("GET /branding", func(w http.ResponseWriter, r *http.Request) {
		if public == "" {
			writeJSON(w, http.StatusOK, kernel.Branding{})
			return
		}
		out, err := k.PublicBranding(r.Context(), public)
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		writeJSON(w, http.StatusOK, out)
	})
	m.HandleFunc("GET /branding/logo", func(w http.ResponseWriter, r *http.Request) {
		if public == "" {
			writeErr(w, http.StatusNotFound, "not_found", "no logo")
			return
		}
		serveLogo(w, r, public)
	})
}
