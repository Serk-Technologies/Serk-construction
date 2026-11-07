// Authority matrix, delegation of authority and duty-conflict exceptions
// (WP-1.2; SRS IAM-09, IAM-13, IAM-14). Database side: migration 0040.
//
// Three company policies are data here, each a governed document that one
// person prepares and a different person decides, with a signed attestation
// at every step:
//
//	authority matrix  who may approve how much (by document type and amount band)
//	delegation        a person lends part of their approval authority for a
//	                  limited time; a third person approves; the delegator may
//	                  revoke at any time
//	SoD exception     a named person may hold two permissions that the
//	                  conflict catalogue (auth.DefaultSoDConflicts) forbids,
//	                  with a written control and an expiry
//
// How they take effect:
//
//   - ApplyDelegations adds a person's current, approved delegations to
//     their Principal (the API does this on every request). A delegation
//     never exceeds what the delegator holds, never lends the permissions in
//     auth.NonDelegable, and is dropped if it would give the delegate a
//     conflicting set of permissions (unless an approved exception covers
//     that person).
//   - assertAuthority applies the approved matrix before a decision is signed.
//     Budget approvals are also checked by the database itself.
//   - SoDReport lists conflicts that exist now: roles in the catalogue and
//     people holding active delegations.
package kernel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ---------------------------------------------------------------- matrix

// AuthorityRule is one band of the matrix: a document type, an amount band
// [Min, Max) in a currency, the permission that approving in this band needs,
// and whether a fresh multi-factor sign-in is also needed. Max nil = no limit.
type AuthorityRule struct {
	DocType      string           `json:"doc_type"`
	Min          decimal.Decimal  `json:"min_amount"`
	Max          *decimal.Decimal `json:"max_amount,omitempty"`
	Currency     string           `json:"currency"`
	RequiredPerm string           `json:"required_perm"`
	RequiresMFA  bool             `json:"requires_mfa"`
}

// CreateAuthorityMatrix prepares a new matrix version (DRAFT) with its rules.
func (s *Service) CreateAuthorityMatrix(ctx context.Context, p auth.Principal, reason string, rules []AuthorityRule) (string, error) {
	if err := s.authz.Require(p, auth.PermAuthorityManage, p.TenantID); err != nil {
		return "", err
	}
	if len(rules) == 0 {
		return "", fmt.Errorf("%w: a matrix needs at least one rule", ErrInvalidInput)
	}
	known := s.authz.KnownPermissions()
	for i, r := range rules {
		if !known[auth.Permission(r.RequiredPerm)] {
			return "", fmt.Errorf("%w: rule %d requires %q, which no role grants", ErrInvalidInput, i+1, r.RequiredPerm)
		}
		if r.Min.IsNegative() || (r.Max != nil && r.Max.LessThanOrEqual(r.Min)) {
			return "", fmt.Errorf("%w: rule %d has an empty or negative band", ErrInvalidInput, i+1)
		}
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `
			INSERT INTO authority_matrices (version, reason, created_by)
			SELECT COALESCE(max(version), 0) + 1, $1, $2 FROM authority_matrices RETURNING id::text`, reason, p.UserID)
		if err != nil {
			return err
		}
		for _, r := range rules {
			if _, err := tx.Exec(ctx, `
				INSERT INTO authority_rules (matrix_id, doc_type, min_amount, max_amount, currency, required_perm, requires_mfa)
				VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`, id, r.DocType, r.Min, r.Max, r.Currency, r.RequiredPerm, r.RequiresMFA); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "authority.matrix.create", "authority_matrix", id, "authority.matrix.created", map[string]any{"authority_matrix_id": id, "rules": len(rules)})
	})
	return id, err
}

// SubmitAuthorityMatrix submits a draft matrix (by its author).
func (s *Service) SubmitAuthorityMatrix(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermAuthorityManage, "authority_matrix", "authority_matrices", id, "authority.matrix.submit", "authority.matrix.submitted")
}

// DecideAuthorityMatrix approves (retiring the matrix in force) or rejects.
// Approval needs a fresh multi-factor sign-in.
func (s *Service) DecideAuthorityMatrix(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermAuthorityApprove, "authority_matrix", "authority_matrices", id, "authority.matrix.decide", "authority matrix", approve, reason,
		func(ctx context.Context, tx pgx.Tx) error {
			// The matrix in force is retired by the successor's attested approval,
			// in the same transaction (the database requires that).
			_, err := tx.Exec(ctx, `UPDATE authority_matrices SET status = 'RETIRED' WHERE status = 'APPROVED' AND id <> $1::uuid`, id)
			return err
		})
}

// decideGovernance is the shared approve/reject path of the governance
// documents (matrix, delegation, SoD exception): permission and scope, status
// SUBMITTED, maker/checker, MFA step-up on approval, attestation, update, audit.
// beforeApprove runs inside the transaction before the approving update.
func (s *Service) decideGovernance(ctx context.Context, p auth.Principal, perm auth.Permission, docType, table, id, action, what string,
	approve bool, reason string, beforeApprove func(ctx context.Context, tx pgx.Tx) error) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, docType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, what); err != nil {
			return err
		}
		if approve {
			if err := s.authz.RequireStepUp(p); err != nil {
				return err
			}
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, action, docType, id, outcome); err != nil {
			return err
		}
		if approve && beforeApprove != nil {
			if err := beforeApprove(ctx, tx); err != nil {
				return err
			}
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE `+table+` SET status = $4, approved_by = $2, decision_reason = NULLIF($3, '') WHERE id = $1::uuid`,
			id, p.UserID, reason, outcome); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, docType, id, strings.TrimSuffix(action, ".decide")+"."+strings.ToLower(outcome), map[string]any{"id": id, "reason": reason})
	})
}

// CurrentAuthorityMatrix returns the matrix in force with its rules, or nil.
func (s *Service) CurrentAuthorityMatrix(ctx context.Context, p auth.Principal) (map[string]any, error) {
	// Anyone who reads procurement documents (their limits apply to them) or
	// who maintains or approves the matrix may read the matrix in force.
	if !s.authz.Has(p, auth.PermP2PRead) && !s.authz.Has(p, auth.PermAuthorityManage) && !s.authz.Has(p, auth.PermAuthorityApprove) {
		return nil, auth.ErrForbidden
	}
	var out map[string]any
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		var version int
		var approvedBy *string
		var approvedAt *time.Time
		err := tx.QueryRow(ctx, `SELECT id::text, version, approved_by, approved_at FROM authority_matrices WHERE status = 'APPROVED'`).Scan(&id, &version, &approvedBy, &approvedAt)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		}
		rows, err := tx.Query(ctx, `SELECT doc_type, min_amount, max_amount, currency, required_perm, requires_mfa
			FROM authority_rules WHERE matrix_id = $1::uuid ORDER BY doc_type, currency, min_amount`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		var rules []AuthorityRule
		for rows.Next() {
			var r AuthorityRule
			if err := rows.Scan(&r.DocType, &r.Min, &r.Max, &r.Currency, &r.RequiredPerm, &r.RequiresMFA); err != nil {
				return err
			}
			rules = append(rules, r)
		}
		out = map[string]any{"id": id, "version": version, "approved_by": approvedBy, "approved_at": approvedAt, "rules": rules}
		return rows.Err()
	})
	return out, err
}

// assertAuthority applies the approved matrix to a decision the principal is
// about to make on a document: the principal's permissions (roles plus
// delegations) must include what the amount's band requires. No matrix, or no
// band covering the amount, means no extra requirement.
func (s *Service) assertAuthority(ctx context.Context, tx pgx.Tx, p auth.Principal, docType, id string) error {
	var amount decimal.Decimal
	var currency *string
	if err := tx.QueryRow(ctx, `SELECT amount, currency FROM authority_doc_amount($1, $2::uuid)`, docType, id).Scan(&amount, &currency); err != nil {
		return err
	}
	if currency == nil {
		return nil // unknown or amount-less document
	}
	perms := make([]string, 0, 8)
	for _, perm := range s.authz.Permissions(p) {
		perms = append(perms, string(perm))
	}
	fresh := s.authz.RequireStepUp(p) == nil
	var msg *string
	if err := tx.QueryRow(ctx, `SELECT authority_check($1, $2, $3, $4, $5)`, docType, amount, *currency, perms, fresh).Scan(&msg); err != nil {
		return err
	}
	if msg != nil {
		return fmt.Errorf("%w: %s", auth.ErrForbidden, *msg)
	}
	return nil
}

// ------------------------------------------------------------ delegation

// DelegationRequest lends Perms from the requesting person to Delegate for a period.
type DelegationRequest struct {
	Delegate  string    `json:"delegate"`
	Perms     []string  `json:"perms"`
	ValidFrom time.Time `json:"valid_from"`
	ValidTo   time.Time `json:"valid_to"`
	Reason    string    `json:"reason"`
}

// CreateDelegation drafts a delegation from the requester (the delegator).
// The delegator must hold every permission lent, by role, and it must be delegable.
func (s *Service) CreateDelegation(ctx context.Context, p auth.Principal, req DelegationRequest) (string, error) {
	if err := s.authz.Require(p, auth.PermDelegationCreate, p.TenantID); err != nil {
		return "", err
	}
	if req.Delegate == "" || req.Delegate == p.UserID {
		return "", fmt.Errorf("%w: choose a colleague other than yourself", ErrInvalidInput)
	}
	if len(req.Perms) == 0 {
		return "", fmt.Errorf("%w: choose at least one permission to lend", ErrInvalidInput)
	}
	for _, perm := range req.Perms {
		if err := s.authz.DelegationAllowed(p, auth.Permission(perm)); err != nil {
			return "", err
		}
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `
			INSERT INTO delegations (delegator, delegate, perms, valid_from, valid_to, reason, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $1) RETURNING id::text`,
			p.UserID, req.Delegate, req.Perms, req.ValidFrom, req.ValidTo, req.Reason)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "delegation.create", "delegation", id, "delegation.created", map[string]any{"delegation_id": id, "delegate": req.Delegate, "perms": req.Perms})
	})
	return id, err
}

// SubmitDelegation submits a draft delegation (by the delegator).
func (s *Service) SubmitDelegation(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermDelegationCreate, "delegation", "delegations", id, "delegation.submit", "delegation.submitted")
}

// DecideDelegation approves or rejects a delegation. The approver is neither
// the delegator nor the delegate, and signs with a fresh multi-factor sign-in.
func (s *Service) DecideDelegation(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermDelegationApprove, "delegation", "delegations", id, "delegation.decide", "delegation", approve, reason,
		func(ctx context.Context, tx pgx.Tx) error {
			var delegate string
			if err := tx.QueryRow(ctx, `SELECT delegate FROM delegations WHERE id = $1::uuid`, id).Scan(&delegate); err != nil {
				return err
			}
			if delegate == p.UserID {
				return fmt.Errorf("%w: the delegate cannot approve their own delegation", auth.ErrSoD)
			}
			return nil
		})
}

// RevokeDelegation ends a delegation now. The delegator may always revoke
// their own; a holder of delegation.approve may revoke anyone's.
func (s *Service) RevokeDelegation(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var delegator, status string
		if err := tx.QueryRow(ctx, `SELECT delegator, status FROM delegations WHERE id = $1::uuid FOR UPDATE`, id).Scan(&delegator, &status); err != nil {
			return mapNoRows(err)
		}
		own := delegator == p.UserID && s.authz.Has(p, auth.PermDelegationCreate)
		if !own && !s.authz.Has(p, auth.PermDelegationApprove) {
			return ErrNotFound // indistinguishable from absence
		}
		if status != "SUBMITTED" && status != "APPROVED" {
			return fmt.Errorf("%w: delegation is %s", ErrInvalidInput, status)
		}
		if err := s.authorize(ctx, tx, p, "delegation.revoke", "delegation", id, ""); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE delegations SET status = 'REVOKED', revoked_by = $2, decision_reason = $3 WHERE id = $1::uuid`, id, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "delegation.revoke", "delegation", id, "delegation.revoked", map[string]any{"delegation_id": id, "reason": reason})
	})
}

// ApplyDelegations returns the principal with their approved, current
// delegations added. It never fails the request: on a lookup error the
// principal is returned unchanged (fewer permissions, never more).
//
// A delegated permission is dropped when (a) the delegator no longer holds it
// by role (the delegator's role is not stored here, so it is re-checked when
// the delegation was created and again by the matrix and the database at
// decision time), or (b) it would give this person a conflicting set of
// permissions without an approved exception.
func (s *Service) ApplyDelegations(ctx context.Context, p auth.Principal) auth.Principal {
	if p.UserID == "" || p.TenantID == "" || p.Kind != auth.KindHuman {
		return p
	}
	type lent struct {
		perm, from string
	}
	var got []lent
	accepted := map[[2]auth.Permission]bool{}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT unnest(perms), delegator FROM delegations
			WHERE delegate = $1 AND status = 'APPROVED' AND now() >= valid_from AND now() < valid_to`, p.UserID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var l lent
			if err := rows.Scan(&l.perm, &l.from); err != nil {
				rows.Close()
				return err
			}
			got = append(got, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		ex, err := tx.Query(ctx, `SELECT perm_a, perm_b FROM sod_exceptions WHERE user_id = $1 AND status = 'APPROVED' AND expires_at > now()`, p.UserID)
		if err != nil {
			return err
		}
		defer ex.Close()
		for ex.Next() {
			var a, b string
			if err := ex.Scan(&a, &b); err != nil {
				return err
			}
			accepted[[2]auth.Permission{auth.Permission(a), auth.Permission(b)}] = true
		}
		return ex.Err()
	})
	if err != nil || len(got) == 0 {
		return p
	}
	held := map[auth.Permission]bool{}
	for _, perm := range s.authz.Permissions(auth.Principal{Roles: p.Roles}) {
		held[perm] = true
	}
	out := p
	out.Delegated = map[auth.Permission][]string{}
	sort.Slice(got, func(i, j int) bool { return got[i].perm+got[i].from < got[j].perm+got[j].from })
	for _, l := range got {
		perm := auth.Permission(l.perm)
		if auth.NonDelegable[perm] {
			continue
		}
		// Would holding this create a conflict that was not already there?
		before := auth.SoDViolations(held, accepted)
		held[perm] = true
		if len(auth.SoDViolations(held, accepted)) > len(before) {
			if _, already := out.Delegated[perm]; !already {
				delete(held, perm)
			}
			continue
		}
		out.Delegated[perm] = append(out.Delegated[perm], l.from)
	}
	if len(out.Delegated) == 0 {
		out.Delegated = nil
	}
	return out
}

// MyDelegations lists delegations the person lent or was lent (and, for an
// approver, all of them).
func (s *Service) MyDelegations(ctx context.Context, p auth.Principal) ([]map[string]any, error) {
	all := s.authz.Has(p, auth.PermDelegationApprove)
	var out []map[string]any
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, delegator, delegate, perms, valid_from, valid_to, status, reason
			FROM delegations WHERE $2 OR delegator = $1 OR delegate = $1 ORDER BY created_at DESC LIMIT 200`, p.UserID, all)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, from, to, status, reason string
			var perms []string
			var vf, vt time.Time
			if err := rows.Scan(&id, &from, &to, &perms, &vf, &vt, &status, &reason); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": id, "delegator": from, "delegate": to, "perms": perms, "valid_from": vf, "valid_to": vt, "status": status, "reason": reason})
		}
		return rows.Err()
	})
	return out, err
}

// ------------------------------------------------------- SoD exceptions

// SoDExceptionRequest accepts, for one person, two permissions the conflict
// catalogue normally forbids together, under a written mitigating control.
type SoDExceptionRequest struct {
	UserID            string    `json:"user_id"`
	PermA             string    `json:"perm_a"`
	PermB             string    `json:"perm_b"`
	MitigatingControl string    `json:"mitigating_control"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// CreateSoDException drafts an exception. The pair must be a real conflict.
func (s *Service) CreateSoDException(ctx context.Context, p auth.Principal, req SoDExceptionRequest) (string, error) {
	if err := s.authz.Require(p, auth.PermAuthorityManage, p.TenantID); err != nil {
		return "", err
	}
	a, b := req.PermA, req.PermB
	if a > b {
		a, b = b, a
	}
	listed := false
	for _, c := range auth.DefaultSoDConflicts {
		has := map[string]bool{}
		for _, perm := range c.Perms {
			has[string(perm)] = true
		}
		if a != b && has[a] && has[b] {
			listed = true
		}
	}
	if !listed {
		return "", fmt.Errorf("%w: %s and %s are not a listed conflict, so no exception is needed", ErrInvalidInput, a, b)
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `
			INSERT INTO sod_exceptions (user_id, perm_a, perm_b, mitigating_control, expires_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, req.UserID, a, b, req.MitigatingControl, req.ExpiresAt, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "sod.exception.create", "sod_exception", id, "sod.exception.created", map[string]any{"sod_exception_id": id, "user_id": req.UserID, "perm_a": a, "perm_b": b})
	})
	return id, err
}

// SubmitSoDException submits a draft exception (by its author).
func (s *Service) SubmitSoDException(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermAuthorityManage, "sod_exception", "sod_exceptions", id, "sod.exception.submit", "sod.exception.submitted")
}

// DecideSoDException approves or rejects an exception (fresh MFA; never the
// subject of the exception, which the database also enforces).
func (s *Service) DecideSoDException(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermAuthorityApprove, "sod_exception", "sod_exceptions", id, "sod.exception.decide", "SoD exception", approve, reason, nil)
}

// SoDFinding is one conflicting pair held by a role or by a delegate.
type SoDFinding struct {
	Subject string `json:"subject"` // a role name, or "delegate <user>"
	Control string `json:"control"`
	PermA   string `json:"perm_a"`
	PermB   string `json:"perm_b"`
	Kind    string `json:"kind"` // role | delegation
}

// SoDReport lists the duty conflicts built into the role catalogue now: roles
// that hold a conflicting set of permissions. (People's role assignments live
// in the identity provider, so conflicts a person creates by combining roles
// or by accepting a delegation are stopped when the permissions are applied:
// see ApplyDelegations.) Reading it needs authority.manage, authority.approve
// or audit.read.
func (s *Service) SoDReport(ctx context.Context, p auth.Principal) ([]SoDFinding, error) {
	if !s.authz.Has(p, auth.PermAuthorityManage) && !s.authz.Has(p, auth.PermAuthorityApprove) && !s.authz.Has(p, auth.PermAuditRead) {
		return nil, auth.ErrForbidden
	}
	var out []SoDFinding
	cat := s.authz.Catalogue()
	names := make([]string, 0, len(cat))
	for n := range cat {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		held := map[auth.Permission]bool{}
		for _, perm := range cat[n] {
			held[perm] = true
		}
		for _, v := range auth.SoDViolations(held, nil) {
			out = append(out, SoDFinding{Subject: n, Control: v.Control, PermA: string(v.A), PermB: string(v.B), Kind: "role"})
		}
	}
	return out, nil
}
