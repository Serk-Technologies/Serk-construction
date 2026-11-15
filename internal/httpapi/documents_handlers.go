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
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// maxUploadBodyBytes bounds a JSON revision upload: the file
// (kernel.MaxDocumentBytes) as base64 plus its envelope.
const maxUploadBodyBytes = kernel.MaxDocumentBytes/3*4 + (1 << 20)

// transferTimeout bounds one file transfer (the ordinary request timeout is
// 15 seconds).
const transferTimeout = 30 * time.Minute

func isFileUpload(r *http.Request) bool {
	return r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/documents/") && strings.HasSuffix(r.URL.Path, "/file")
}

func isFileDownload(r *http.Request) bool {
	return r.Method == http.MethodGet && (strings.HasPrefix(r.URL.Path, "/v1/dl/") || (strings.HasPrefix(r.URL.Path, "/v1/document-revisions/") && strings.HasSuffix(r.URL.Path, "/content")))
}

// bodyLimit is the request body limit of a route: 1 MiB everywhere except the
// two routes that carry a file. The streamed route's limit is the kernel's
// (the deployment's largest file); the body is spooled to disk, not memory.
func (s *Server) bodyLimit(r *http.Request) int64 {
	switch {
	case isFileUpload(r):
		return 1 << 40 // the kernel stops reading at the deployment's limit
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/documents/") && strings.HasSuffix(r.URL.Path, "/revisions"):
		return maxUploadBodyBytes
	}
	return maxBodyBytes
}

func routeTimeout(r *http.Request) time.Duration {
	if isFileUpload(r) || isFileDownload(r) {
		return transferTimeout
	}
	return requestTimeout
}

// serveFile streams a revision's file: always a download, never rendered in
// the console's origin. The file name is restricted by the database to
// characters that need no quoting. If the stream fails its integrity check the
// connection is aborted, so a client never sees a complete response.
func serveFile(w http.ResponseWriter, f kernel.DocumentRevision, rc io.ReadCloser) {
	defer rc.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout))
	h := w.Header()
	h.Set("Content-Type", f.MediaType)
	h.Set("Content-Disposition", `attachment; filename="`+f.FileName+`"`)
	h.Set("Content-Length", strconv.FormatInt(f.SizeBytes, 10))
	h.Set("X-Serk-Revision-Status", f.Status)
	h.Set("X-Serk-Revision-Number", f.Number)
	h.Set("X-Serk-Scan-Status", f.ScanStatus)
	h.Set("X-Content-SHA256", f.SHA256)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) registerDocuments() {
	m := s.mux
	k := s.cfg.Kernel
	// Transmittals and submittals (0077); lists and details through /v1/p2p/{transmittal,submittal}.
	m.Handle("POST /v1/transmittals", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.TransmittalInput
		if !decode(w, r, &b) || !validIDs(w, append([]string{b.ProjectID}, b.DocumentIDs...)...) {
			return
		}
		id, err := k.SendTransmittal(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	// Correspondence register (0085); list and detail also through /v1/p2p/correspondence.
	m.Handle("POST /v1/correspondence", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.CorrespondenceInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		for _, opt := range []string{b.ContractID, b.InReplyTo} {
			if opt != "" && !validIDs(w, opt) {
				return
			}
		}
		id, err := k.RecordCorrespondence(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/projects/{id}/correspondence", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		out, err := k.CorrespondenceRegister(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}))
	m.Handle("POST /v1/submittals", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.SubmittalInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.SubmitSubmittal(r.Context(), principalFrom(r.Context()), b)
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
		if err := k.ReviewSubmittal(r.Context(), principalFrom(r.Context()), id, b.Code, b.Note); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "REVIEWED"})
	}))
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
		f, rc, err := k.OpenRevision(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		serveFile(w, f, rc)
	}))
	// Streamed upload: the body is the file; its description is in the query.
	m.Handle("PUT /v1/documents/{id}/revisions/{revision}/file", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(transferTimeout))
		q := r.URL.Query()
		in := kernel.RevisionInput{Revision: r.PathValue("revision"), FileName: q.Get("file_name"), MediaType: r.Header.Get("Content-Type"), ChangeNote: q.Get("change_note")}
		rev, err := k.AddRevisionStream(r.Context(), principalFrom(r.Context()), id, in, r.Body)
		s.created(w, rev, err)
	}))
	// A short-lived link to one file, for a browser to stream it without the bearer token.
	m.Handle("POST /v1/document-revisions/{id}/download-link", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		tok, exp, err := k.IssueDownloadToken(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"url": "/v1/dl/" + tok, "expires_at": exp.UTC().Format(time.RFC3339)})
	}))
	m.Handle("GET /v1/dl/{token}", s.limited(func(w http.ResponseWriter, r *http.Request) {
		f, rc, err := k.RedeemDownloadToken(r.Context(), r.PathValue("token"))
		if err != nil {
			s.fail(w, err)
			return
		}
		serveFile(w, f, rc)
	}))
	m.Handle("POST /v1/document-revisions/{id}/rescan", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.RescanRevision(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/document-revisions/{id}/discard", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DiscardRevision(ctx, p, id, b.Reason)
	})))
	m.Handle("GET /v1/document-policy", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.GetDocumentPolicy(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("PUT /v1/document-policy", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ScanRequired bool   `json:"scan_required"`
			QuotaBytes   *int64 `json:"quota_bytes"`
			Reason       string `json:"reason"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.SetDocumentPolicy(r.Context(), principalFrom(r.Context()), b.ScanRequired, b.QuotaBytes, b.Reason); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	m.Handle("POST /v1/document-storage/migrate", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Limit int `json:"limit"`
		}
		if r.ContentLength != 0 && !decode(w, r, &b) {
			return
		}
		out, err := k.MigrateDocumentContents(r.Context(), principalFrom(r.Context()), b.Limit)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
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

// limited applies the pre-authentication abuse limits to a route that carries
// its own authority (a signed download link) instead of a bearer token.
func (s *Server) limited(next func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := limitKey(clientIP(r, s.cfg.Abuse.TrustedProxies))
		if s.authFail.blocked(ip) || !s.preauth.hit(ip) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "request budget exceeded")
			return
		}
		select {
		case s.inflight <- struct{}{}:
			defer func() { <-s.inflight }()
		default:
			w.Header().Set("Retry-After", "5")
			writeErr(w, http.StatusServiceUnavailable, "overloaded", "server busy")
			return
		}
		next(w, r)
	})
}
