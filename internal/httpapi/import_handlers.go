package httpapi

// Staged imports and the setup checklist (WP-1.8). Rules live in
// internal/kernel/imports.go, setup.go and migration 0043.
//
//	GET  /v1/setup                         the company setup checklist (state read from the records)
//	GET  /v1/imports/templates/{kind}      CSV template: supplier | customer | stock_item
//	GET  /v1/imports                       staged batches
//	POST /v1/imports                       stage a file: {kind, legal_entity_id, filename, content} (nothing is created)
//	GET  /v1/imports/{id}                  a batch with every row and its errors
//	POST /v1/imports/{id}/commit           {expected_rows}: a different person creates the valid rows
//	POST /v1/imports/{id}/discard          {reason}

import (
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
)

func (s *Server) registerImports() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/setup", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.SetupChecklist(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"steps": out})
	}))
	m.Handle("GET /v1/imports/templates/{kind}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		csv, help, ok := kernel.ImportTemplate(r.PathValue("kind"))
		if !ok {
			writeErr(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"kind": r.PathValue("kind"), "csv": csv, "help": help})
	}))
	m.Handle("GET /v1/imports", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.ListImports(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"imports": out})
	}))
	m.Handle("POST /v1/imports", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Kind     string `json:"kind"`
			EntityID string `json:"legal_entity_id"`
			Filename string `json:"filename"`
			Content  string `json:"content"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.EntityID) {
			return
		}
		id, err := k.StageImport(r.Context(), principalFrom(r.Context()), b.Kind, b.EntityID, b.Filename, []byte(b.Content))
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/imports/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.GetImport(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/imports/{id}/commit", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			ExpectedRows int `json:"expected_rows"`
		}
		if !decode(w, r, &b) {
			return
		}
		out, err := k.CommitImport(r.Context(), principalFrom(r.Context()), id, b.ExpectedRows)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/imports/{id}/discard", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			Reason string `json:"reason"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.DiscardImport(r.Context(), principalFrom(r.Context()), id, b.Reason); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
}
