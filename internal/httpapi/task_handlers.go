package httpapi

// Assignable tasks (CNX-07.2; 0087).
//
//	GET  /v1/tasks?view=mine|created; POST /v1/tasks {project_id, title, detail, assigned_to, due_on, priority}
//	POST /v1/tasks/{id}/done {note}; POST /v1/tasks/{id}/cancel {reason}; POST /v1/tasks/{id}/reassign {assigned_to, due_on}

import (
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
)

func (s *Server) registerTasks() {
	m, k := s.mux, s.cfg.Kernel
	m.Handle("GET /v1/tasks", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.Tasks(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("view"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}))
	m.Handle("POST /v1/tasks", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.TaskInput
		if !decode(w, r, &b) {
			return
		}
		if b.ProjectID != "" && !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.CreateTask(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	step := func(path string, run func(r *http.Request, id string, b map[string]string) error) {
		m.Handle("POST /v1/tasks/{id}/"+path, s.authed(func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			var b map[string]string
			if !validIDs(w, id) || !decode(w, r, &b) {
				return
			}
			if err := run(r, id, b); err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": id})
		}))
	}
	step("done", func(r *http.Request, id string, b map[string]string) error {
		return k.CompleteTask(r.Context(), principalFrom(r.Context()), id, b["note"])
	})
	step("cancel", func(r *http.Request, id string, b map[string]string) error {
		return k.CancelTask(r.Context(), principalFrom(r.Context()), id, b["reason"])
	})
	step("reassign", func(r *http.Request, id string, b map[string]string) error {
		return k.ReassignTask(r.Context(), principalFrom(r.Context()), id, b["assigned_to"], b["due_on"])
	})
}
