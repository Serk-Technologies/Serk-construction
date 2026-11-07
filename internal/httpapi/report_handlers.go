package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/serk-group/serk-construction/internal/kernel"
)

// Financial reports (read-only; tenant-wide scope):
//
//	GET /v1/reports/trial-balance?entity=<uuid>&as_of=YYYY-MM-DD
//	GET /v1/reports/ap-aging?entity=<uuid>&as_of=YYYY-MM-DD
//	GET /v1/reports/ar-aging?entity=<uuid>&as_of=YYYY-MM-DD
func (s *Server) registerReports() {
	s.mux.Handle("GET /v1/reports/trial-balance", s.authed(s.reportTB))
	s.mux.Handle("GET /v1/reports/ap-aging", s.authed(s.reportAging(false)))
	s.mux.Handle("GET /v1/reports/ar-aging", s.authed(s.reportAging(true)))
	// Report catalogue (WP-1.5): one registry, one runner (internal/kernel/reportcat.go).
	//
	//	GET /v1/reports/catalogue                      the reports the caller may run
	//	GET /v1/reports/catalogue/{id}?entity=&project=&subject=&from=&to=
	//
	// Under /v1/reports/, so PDF, Word and Excel export works through /v1/export.
	s.mux.Handle("GET /v1/reports/catalogue", s.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"reports": s.cfg.Kernel.ReportCatalogue(principalFrom(r.Context()))})
	}))
	s.mux.Handle("GET /v1/reports/catalogue/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.cfg.Kernel.RunReport(r.Context(), principalFrom(r.Context()), r.PathValue("id"), kernel.ReportFilters{
			EntityID: q.Get("entity"), ProjectID: q.Get("project"), SubjectID: q.Get("subject"), From: q.Get("from"), To: q.Get("to")})
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
}

func reportParams(w http.ResponseWriter, r *http.Request) (string, time.Time, bool) {
	entity := r.URL.Query().Get("entity")
	if _, err := uuid.Parse(entity); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "validation", "entity must be a legal entity id")
		return "", time.Time{}, false
	}
	asOf := time.Now().UTC()
	if v := r.URL.Query().Get("as_of"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil || t.Year() < 1900 || t.Year() > 2200 {
			writeErr(w, http.StatusUnprocessableEntity, "validation", "as_of must be a date (YYYY-MM-DD)")
			return "", time.Time{}, false
		}
		asOf = t
	}
	return entity, asOf, true
}

func (s *Server) reportTB(w http.ResponseWriter, r *http.Request) {
	entity, asOf, ok := reportParams(w, r)
	if !ok {
		return
	}
	out, err := s.cfg.Kernel.TrialBalanceReport(r.Context(), principalFrom(r.Context()), entity, asOf)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reportAging(receivables bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		entity, asOf, ok := reportParams(w, r)
		if !ok {
			return
		}
		p := principalFrom(r.Context())
		fn := s.cfg.Kernel.PayablesAging
		if receivables {
			fn = s.cfg.Kernel.ReceivablesAging
		}
		out, err := fn(r.Context(), p, entity, asOf)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
