package httpapi

// Document control, RFIs, submittals, transmittals (0066).
//
//	GET  /v1/documents[?q=]; POST /v1/documents; POST /v1/documents/{id}/revisions; POST /v1/documents/{id}/publish
//	GET  /v1/documents/{id}/revisions/{rev}/file
//	POST /v1/transmittals; POST /v1/rfis; POST /v1/rfis/{id}/answer|close; POST /v1/submittals; POST /v1/submittals/{id}/review

import (
	"context"
	"net/http"
	"strconv"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerDocs() {
	m := s.mux
	k := s.cfg.Kernel
	dec := func(fn func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error)) http.Handler {
		return s.authed(s.p2pDecision(fn))
	}
	reply := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	m.Handle("GET /v1/documents", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.DocsView(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("q"))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/documents", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.DocumentIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.CreateDocument(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/documents/{id}/revisions", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b kernel.RevisionIn
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		rev, err := k.UploadRevision(r.Context(), principalFrom(r.Context()), id, b)
		reply(w, map[string]any{"id": id, "rev": rev}, err)
	}))
	m.Handle("POST /v1/documents/{id}/publish", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.PublishRevision(ctx, p, id)
	}))
	m.Handle("GET /v1/documents/{id}/revisions/{rev}/file", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		rev, err := strconv.Atoi(r.PathValue("rev"))
		if err != nil || !validIDs(w, id) {
			writeErr(w, http.StatusNotFound, "not_found", "revision not found")
			return
		}
		name, mime, content, err := k.DownloadRevision(r.Context(), principalFrom(r.Context()), id, rev)
		if err != nil {
			s.fail(w, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", mime)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	m.Handle("POST /v1/transmittals", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.TransmittalIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		for _, d := range b.DocumentIDs {
			if !validIDs(w, d) {
				return
			}
		}
		id, err := k.SendTransmittal(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/rfis", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RFIIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		if b.DocumentID != "" && !validIDs(w, b.DocumentID) {
			return
		}
		id, err := k.RaiseRFI(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/rfis/{id}/answer", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Response       string `json:"response"`
			CostImpact     bool   `json:"cost_impact"`
			ScheduleImpact bool   `json:"schedule_impact"`
			ChangeEventID  string `json:"change_event_id"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		if b.ChangeEventID != "" && !validIDs(w, b.ChangeEventID) {
			return
		}
		reply(w, map[string]any{"id": id}, k.AnswerRFI(r.Context(), principalFrom(r.Context()), id, b.Response, b.CostImpact, b.ScheduleImpact, b.ChangeEventID))
	}))
	m.Handle("POST /v1/rfis/{id}/close", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CloseRFI(ctx, p, id)
	}))
	m.Handle("POST /v1/submittals", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Project string `json:"project_id"`
			Section string `json:"spec_section"`
			Title   string `json:"title"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Project) {
			return
		}
		id, err := k.SubmitSubmittal(r.Context(), principalFrom(r.Context()), b.Project, b.Section, b.Title)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/submittals/{id}/review", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Code string `json:"code"`
			Note string `json:"note"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		reply(w, map[string]any{"id": id}, k.ReviewSubmittal(r.Context(), principalFrom(r.Context()), id, b.Code, b.Note))
	}))
}
