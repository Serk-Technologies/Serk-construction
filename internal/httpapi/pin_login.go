package httpapi

// E-mail + 6-digit PIN sign-in with an optional authenticator (migration 0095).
//
//	GET  /auth/methods                                  which sign-ins this deployment offers (public)
//	POST /auth/login {email, pin, code?, new_pin?}       a session token
//	GET  /v1/me/login                                   my sign-in state
//	POST /v1/me/pin {current_pin, new_pin}
//	POST /v1/me/authenticator/begin                     {secret, otpauth_uri}
//	POST /v1/me/authenticator/confirm {secret, code}
//	POST /v1/me/authenticator/off {pin}
//	GET  /v1/logins                                     the company's sign-ins (user.manage, role.manage, audit.read)
//	GET  /v1/login-trail?user=&limit=                   the sign-in trail
//	POST /v1/people/{uid}/login {email}                 issue or re-issue a sign-in: {pin}, shown once
//	POST /v1/people/{uid}/login/unlock {reason?}
//	POST /v1/people/{uid}/login/reset-authenticator {reason}
//	POST /v1/people/{uid}/login/disable {reason}
//	POST /v1/people/{uid}/login/enable {reason?}
//
// Like the staging sign-in this issues HS256 session tokens, so it runs in
// development and staging only until an ADR approves a production signer.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/serk-group/serk-construction/internal/kernel"
)

// PinLogin enables the e-mail + PIN sign-in.
type PinLogin struct {
	Store   kernel.LoginStore
	Secrets kernel.PinSecrets
	Signer  HS256Verifier
	TTL     time.Duration
}

func (s *Server) registerPinLogin() {
	pl := s.cfg.PinLogin
	m := s.mux
	s.mux.HandleFunc("GET /auth/methods", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"pin": pl != nil, "staging": s.cfg.Staging != nil})
	})
	if pl == nil {
		return
	}
	if len(pl.Secrets.Pepper) < 32 {
		panic("httpapi: PinLogin needs a pepper of at least 32 bytes")
	}
	if pl.TTL == 0 {
		pl.TTL = 10 * time.Hour
	}
	k := s.cfg.Kernel
	s.mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		ip := limitKey(clientIP(r, s.cfg.Abuse.TrustedProxies))
		if s.authFail.blocked(ip) {
			w.Header().Set("Retry-After", "300")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many failed sign-in attempts from this address; wait a few minutes")
			return
		}
		if !s.preauth.hit(ip) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "request budget exceeded")
			return
		}
		var in struct {
			Email  string `json:"email"`
			PIN    string `json:"pin"`
			Code   string `json:"code,omitempty"`
			NewPIN string `json:"new_pin,omitempty"`
		}
		if !decode(w, r, &in) {
			return
		}
		mac := hmac.New(sha256.New, pl.Secrets.Pepper)
		mac.Write([]byte("serk-login-ip\x00" + ip))
		res, err := k.SignInWithPIN(r.Context(), pl.Store, pl.Secrets, kernel.SignInInput{Email: in.Email, PIN: in.PIN, Code: in.Code, NewPIN: in.NewPIN, IPHash: mac.Sum(nil)})
		if err != nil {
			var lock kernel.SignInLock
			var wrong kernel.SignInWrong
			switch {
			case errors.As(err, &lock):
				s.authFailure(ip, "pin_locked")
				w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(lock.Until).Seconds())+1))
				writeProblem(w, http.StatusLocked, "locked", "This sign-in is locked after 15 wrong attempts. Try again after "+lock.Until.UTC().Format("15:04")+" UTC, or ask your system administrator to unlock it.",
					map[string]any{"locked_until": lock.Until.UTC().Format(time.RFC3339)})
			case errors.As(err, &wrong):
				s.authFailure(ip, "pin_wrong")
				writeProblem(w, http.StatusUnauthorized, "invalid_credentials", wrong.Error(), map[string]any{"attempts_left": wrong.Left})
			case errors.Is(err, kernel.ErrSignIn):
				s.authFailure(ip, "pin_wrong")
				writeErr(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
			case errors.Is(err, kernel.ErrSignInDisabled):
				writeErr(w, http.StatusForbidden, "disabled", err.Error())
			case errors.Is(err, kernel.ErrCodeRequired):
				writeErr(w, http.StatusUnauthorized, "code_required", err.Error())
			case errors.Is(err, kernel.ErrPINChangeRequired):
				writeErr(w, http.StatusConflict, "pin_change_required", err.Error())
			default:
				s.fail(w, err)
			}
			return
		}
		now := time.Now()
		amr := []string{"pin"}
		if res.MFA {
			amr = append(amr, "otp")
		}
		// Roles and projects come from the company's directory on every request (the person is managed there).
		tok, err := pl.Signer.Sign(Claims{Subject: res.UserID, TenantID: res.TenantID, IssuedAt: now.Unix(), ExpiresAt: now.Add(pl.TTL).Unix(),
			Issuer: pl.Signer.Issuer, Audience: pl.Signer.Audience, Kind: "human", TokenID: uuid.NewString(), AMR: amr, AuthTime: now.Unix()})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "could not issue the session")
			return
		}
		s.cfg.Logger.Info("security", "event", "pin_sign_in", "user", res.UserID, "tenant", res.TenantID, "mfa", res.MFA)
		writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_at": now.Add(pl.TTL).UTC().Format(time.RFC3339), "mfa": res.MFA})
	})

	m.Handle("GET /v1/me/login", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.MyLogin(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/me/pin", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Current string `json:"current_pin"`
			New     string `json:"new_pin"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.ChangeMyPIN(r.Context(), principalFrom(r.Context()), pl.Secrets, b.Current, b.New); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	m.Handle("POST /v1/me/authenticator/begin", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.BeginAuthenticator(r.Context(), principalFrom(r.Context()), pl.Secrets)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/me/authenticator/confirm", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Secret string `json:"secret"`
			Code   string `json:"code"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.ConfirmAuthenticator(r.Context(), principalFrom(r.Context()), pl.Secrets, b.Secret, b.Code); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	m.Handle("POST /v1/me/authenticator/off", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			PIN string `json:"pin"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.TurnOffMyAuthenticator(r.Context(), principalFrom(r.Context()), pl.Secrets, b.PIN); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	m.Handle("GET /v1/logins", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.LoginAccounts(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}))
	m.Handle("GET /v1/login-trail", s.authed(func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := k.LoginTrail(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("user"), limit)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}))
	m.Handle("POST /v1/people/{uid}/login", s.authed(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := personID(w, r)
		if !ok {
			return
		}
		var b struct {
			Email string `json:"email"`
		}
		if !decode(w, r, &b) {
			return
		}
		pin, err := k.IssueLogin(r.Context(), principalFrom(r.Context()), pl.Secrets, uid, b.Email)
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"user_id": uid, "pin": pin, "must_change": true})
	}))
	action := func(act string) http.Handler {
		return s.authed(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := personID(w, r)
			if !ok {
				return
			}
			var b struct {
				Reason string `json:"reason"`
			}
			if r.ContentLength != 0 && !decode(w, r, &b) {
				return
			}
			if err := k.LoginAction(r.Context(), principalFrom(r.Context()), uid, act, b.Reason); err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		})
	}
	m.Handle("POST /v1/people/{uid}/login/unlock", action("UNLOCK"))
	m.Handle("POST /v1/people/{uid}/login/reset-authenticator", action("AUTHENTICATOR_RESET"))
	m.Handle("POST /v1/people/{uid}/login/disable", action("LOGIN_DISABLED"))
	m.Handle("POST /v1/people/{uid}/login/enable", action("LOGIN_ENABLED"))
}

// personID reads and checks the {uid} path value.
func personID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("uid")
	if !personIDRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return "", false
	}
	return id, true
}

// writeProblem is writeErr with extension members (RFC 9457 section 3.2).
func writeProblem(w http.ResponseWriter, status int, code, msg string, ext map[string]any) {
	title := problemTitles[code]
	if title == "" {
		title = http.StatusText(status)
	}
	body := map[string]any{"type": ProblemType(code), "title": title, "status": status, "detail": msg, "code": code,
		"error": map[string]string{"code": code, "message": msg}}
	for k, v := range ext {
		if _, taken := body[k]; !taken {
			body[k] = v
		}
	}
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
