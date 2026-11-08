package httpapi

// People and access (0048): the company directory, company roles, role
// grants and the signed-in person's own profile. The kernel decides every
// permission; these handlers only parse and answer.

import (
	"net/http"
	"regexp"

	"github.com/serk-group/serk-construction/internal/kernel"
)

var personIDRe = regexp.MustCompile(`^[A-Za-z0-9._@:+|-]{1,200}$`)
var roleCodeRe = regexp.MustCompile(`^(custom:)?[a-z][a-z0-9_]{1,60}$`)

type accessBody struct {
	Reason string `json:"reason"`
	Role   string `json:"role"`
}

func (s *Server) registerPeople() {
	m := s.mux
	k := s.cfg.Kernel
	personID := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		id := r.PathValue("uid")
		if !personIDRe.MatchString(id) {
			writeErr(w, http.StatusNotFound, "not_found", "not found")
			return "", false
		}
		return id, true
	}
	reply := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	m.Handle("GET /v1/people", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.People(r.Context(), principalFrom(r.Context()))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/people", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.PersonInput
		if !decode(w, r, &b) {
			return
		}
		out, err := k.AddPerson(r.Context(), principalFrom(r.Context()), b)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}))
	m.Handle("GET /v1/people/{uid}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := personID(w, r)
		if !ok {
			return
		}
		x, hist, events, err := k.GetPerson(r.Context(), principalFrom(r.Context()), id)
		reply(w, map[string]any{"person": x, "grant_history": hist, "history": events}, err)
	}))
	m.Handle("PUT /v1/people/{uid}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := personID(w, r)
		if !ok {
			return
		}
		var b kernel.PersonInput
		if !decode(w, r, &b) {
			return
		}
		out, err := k.UpdatePerson(r.Context(), principalFrom(r.Context()), id, b)
		reply(w, out, err)
	}))
	personAction := func(fn func(w http.ResponseWriter, r *http.Request, id string, b accessBody) (any, error)) http.Handler {
		return s.authed(func(w http.ResponseWriter, r *http.Request) {
			id, ok := personID(w, r)
			if !ok {
				return
			}
			var b accessBody
			if !decode(w, r, &b) {
				return
			}
			out, err := fn(w, r, id, b)
			reply(w, out, err)
		})
	}
	m.Handle("POST /v1/people/{uid}/manage", personAction(func(w http.ResponseWriter, r *http.Request, id string, b accessBody) (any, error) {
		return k.ManagePerson(r.Context(), principalFrom(r.Context()), id, b.Reason)
	}))
	m.Handle("POST /v1/people/{uid}/disable", personAction(func(w http.ResponseWriter, r *http.Request, id string, b accessBody) (any, error) {
		return k.SetPersonStatus(r.Context(), principalFrom(r.Context()), id, false, b.Reason)
	}))
	m.Handle("POST /v1/people/{uid}/enable", personAction(func(w http.ResponseWriter, r *http.Request, id string, b accessBody) (any, error) {
		return k.SetPersonStatus(r.Context(), principalFrom(r.Context()), id, true, b.Reason)
	}))
	m.Handle("POST /v1/people/{uid}/roles", personAction(func(w http.ResponseWriter, r *http.Request, id string, b accessBody) (any, error) {
		return k.GrantRole(r.Context(), principalFrom(r.Context()), id, b.Role, b.Reason)
	}))
	m.Handle("POST /v1/role-grants/{id}/revoke", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b accessBody
		if !decode(w, r, &b) {
			return
		}
		out, err := k.RevokeRole(r.Context(), principalFrom(r.Context()), id, b.Reason)
		reply(w, out, err)
	}))
	m.Handle("POST /v1/roles", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RoleInput
		if !decode(w, r, &b) {
			return
		}
		b.Version = 0
		out, err := k.SaveRole(r.Context(), principalFrom(r.Context()), b)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}))
	m.Handle("PUT /v1/roles/{code}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")
		if !roleCodeRe.MatchString(code) {
			writeErr(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		var b kernel.RoleInput
		if !decode(w, r, &b) {
			return
		}
		if b.Version < 1 {
			writeErr(w, http.StatusUnprocessableEntity, "validation", "version is required to change a role")
			return
		}
		b.Code = code
		out, err := k.SaveRole(r.Context(), principalFrom(r.Context()), b)
		reply(w, out, err)
	}))
	m.Handle("POST /v1/roles/{code}/retire", s.authed(func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")
		if !roleCodeRe.MatchString(code) {
			writeErr(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		var b accessBody
		if !decode(w, r, &b) {
			return
		}
		err := k.RetireRole(r.Context(), principalFrom(r.Context()), code, b.Reason)
		reply(w, map[string]any{"code": code, "status": "RETIRED"}, err)
	}))
	m.Handle("GET /v1/me/profile", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.MyProfile(r.Context(), principalFrom(r.Context()))
		reply(w, out, err)
	}))
	m.Handle("PUT /v1/me/profile", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ProfileInput
		if !decode(w, r, &b) {
			return
		}
		out, err := k.UpdateMyProfile(r.Context(), principalFrom(r.Context()), b)
		reply(w, out, err)
	}))
}
