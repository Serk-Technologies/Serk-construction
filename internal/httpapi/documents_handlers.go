package httpapi

// Controlled documents, revisions and RFIs (0052; ADR 0018). The kernel
// decides every permission and the database hashes every file; these handlers
// only parse and answer.
//
//	GET  /v1/documents?project=                 the register with each document's current revision
//	POST /v1/documents                          {project_id, reference, doc_type, title, discipline?}
//	GET  /v1/documents/{id}                     one document with every revision
//	POST /v1/documents/{id}/revisions           {revision, file_name, media_type, change_note, content (base64)}
//	POST /v1/document-revisions/{id}/submit|approve|reject
//	GET  /v1/document-revisions/{id}/content    the file, as an attachment, with its revision status
//	GET  /v1/rfis?project=&status=              requests for information with their standing
//	POST /v1/rfis                               {project_id, subject, question, revision_id?, assigned_to, due_on}
//	POST /v1/rfis/{id}/answer                   {answer}
//	POST /v1/rfis/{id}/close                    {note?} (a note is required to withdraw an unanswered RFI)

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// maxUploadBodyBytes bounds a revision upload: the file (kernel.MaxDocumentBytes)
// as base64 plus its JSON envelope.
const maxUploadBodyBytes = kernel.MaxDocumentBytes/3*4 + (1 << 20)

// bodyLimit is the request body limit of a route: 1 MiB everywhere except the
// one route that carries a file.
func bodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/documents/") && strings.HasSuffix(r.URL.Path, "/revisions") {
		return maxUploadBodyBytes
	}
	return maxBodyBytes
}

func (s *Server) registerDocuments() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/documents", s.authed(func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project != "" && !validIDs(w, project) {
			return
		}
		out, err := k.ListDocuments(r.Context(), principalFrom(r.Context()), project, 0)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"documents": out})
	}))
	m.Handle("POST /v1/documents", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.DocumentInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.CreateDocument(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/documents/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.GetDocument(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/documents/{id}/revisions", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b kernel.RevisionInput
		if !decode(w, r, &b) {
			return
		}
		rev, err := k.AddRevision(r.Context(), principalFrom(r.Context()), id, b)
		s.created(w, rev, err)
	}))
	m.Handle("POST /v1/document-revisions/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitRevision(ctx, p, id)
	})))
	m.Handle("POST /v1/document-revisions/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRevision(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/document-revisions/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRevision(ctx, p, id, false, b.Reason)
	})))
	m.Handle("GET /v1/document-revisions/{id}/content", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		f, err := k.DownloadRevision(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		// Always a download, never rendered in the console's origin. The file
		// name is restricted by the database to characters that need no quoting.
		h := w.Header()
		h.Set("Content-Type", f.MediaType)
		h.Set("Content-Disposition", `attachment; filename="`+f.FileName+`"`)
		h.Set("Content-Length", strconv.Itoa(len(f.Content)))
		h.Set("X-Serk-Revision-Status", f.Status)
		h.Set("X-Serk-Revision-Number", f.Number)
		h.Set("X-Content-SHA256", f.SHA256)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(f.Content)
	}))

	m.Handle("GET /v1/rfis", s.authed(func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project != "" && !validIDs(w, project) {
			return
		}
		out, err := k.ListRFIs(r.Context(), principalFrom(r.Context()), project, r.URL.Query().Get("status"), 0)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rfis": out})
	}))
	m.Handle("POST /v1/rfis", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RFIInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		if b.RevisionID != "" && !validIDs(w, b.RevisionID) {
			return
		}
		id, err := k.CreateRFI(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/rfis/{id}/answer", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			Answer string `json:"answer"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.AnswerRFI(r.Context(), principalFrom(r.Context()), id, b.Answer); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
	m.Handle("POST /v1/rfis/{id}/close", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			Note string `json:"note"`
		}
		if r.ContentLength != 0 && !decode(w, r, &b) {
			return
		}
		if err := k.CloseRFI(r.Context(), principalFrom(r.Context()), id, b.Note); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
}
