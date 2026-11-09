package kernel

// People and access (0048): the company directory, the company's own roles,
// who holds which role, and each person's own profile.
//
// Where roles come from. A person signs in through the identity provider and
// the token states their roles and projects. The first request records the
// person in the directory (Touch). While the person is not "managed" the
// token stays the authority. Once an administrator takes the person under
// management, the roles and projects given here replace the token's on the
// very next request (ApplyDirectory, IAM-11), and a disabled person is
// refused outright.
//
// Controls. Nobody changes their own access (kernel and database). A role
// is never given if it would let the person hold both halves of a control,
// unless an approved duty-conflict exception covers that pair (IAM-09). A
// company role is composed only from the fixed permission list and never
// holds both halves of a control itself (IAM-05). Every change needs a
// reason and is written to the audit history.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ErrAccountDisabled refuses every request of a person disabled in the
// company directory.
var ErrAccountDisabled = errors.New("kernel: this account is disabled in this company")

const customPrefix = "custom:"

var (
	userIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._@:+|-]{1,200}$`)
	roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,40}$`)
	emailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	canonUUID       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Person is one row of the directory with what it means: the roles in
// force, where they come from, the permissions they give and any duty
// conflicts they create.
type Person struct {
	UserID       string            `json:"user_id"`
	DisplayName  string            `json:"display_name"`
	Email        string            `json:"email"`
	JobTitle     string            `json:"job_title"`
	Phone        string            `json:"phone"`
	Managed      bool              `json:"managed"`
	RoleSource   string            `json:"role_source"` // "directory" or "sign-in"
	Status       string            `json:"status"`
	StatusReason *string           `json:"status_reason,omitempty"`
	Roles        []string          `json:"roles"`
	Grants       []RoleGrant       `json:"grants"`
	ProjectIDs   []string          `json:"project_ids"`
	Permissions  []auth.Permission `json:"permissions"`
	Stage        string            `json:"stage"`
	Conflicts    []auth.SoDPair    `json:"conflicts"`
	FirstSeen    *time.Time        `json:"first_seen,omitempty"`
	LastSeen     *time.Time        `json:"last_seen,omitempty"`
	CreatedBy    string            `json:"created_by"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedBy    *string           `json:"updated_by,omitempty"`
	UpdatedAt    *time.Time        `json:"updated_at,omitempty"`
	Version      int               `json:"version"`
	idpRoles     []string          // what the token last said
	accepted     map[[2]auth.Permission]bool
}

// RoleGrant is one role given to a person.
type RoleGrant struct {
	ID           string     `json:"id"`
	Role         string     `json:"role"`
	Reason       string     `json:"reason"`
	GrantedBy    string     `json:"granted_by"`
	GrantedAt    time.Time  `json:"granted_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokedBy    *string    `json:"revoked_by,omitempty"`
	RevokeReason *string    `json:"revoke_reason,omitempty"`
}

// RoleInfo is a role of the catalogue: a seed role (fixed, from the product)
// or one of the company's own.
type RoleInfo struct {
	Code        string            `json:"code"`
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Kind        string            `json:"kind"` // "seed" or "company"
	Status      string            `json:"status"`
	Permissions []auth.Permission `json:"permissions"`
	Stage       string            `json:"stage"`
	Holders     int               `json:"holders"`
	Version     int               `json:"version,omitempty"`
	CreatedBy   string            `json:"created_by,omitempty"`
	UpdatedBy   *string           `json:"updated_by,omitempty"`
	UpdatedAt   *time.Time        `json:"updated_at,omitempty"`
}

// AccessEvent is one entry of the access history.
type AccessEvent struct {
	At      time.Time      `json:"at"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Subject string         `json:"subject"`
	Detail  map[string]any `json:"detail"`
}

// PeopleView is everything the people and access page shows.
type PeopleView struct {
	People      []Person              `json:"people"`
	Roles       []RoleInfo            `json:"roles"`
	Permissions []auth.PermissionInfo `json:"permissions"`
	Controls    []auth.SoDConflict    `json:"controls"`
	Projects    []postgres.RefItem    `json:"projects"`
	History     []AccessEvent         `json:"history"`
	Me          struct {
		UserID         string `json:"user_id"`
		CanManageUsers bool   `json:"can_manage_users"`
		CanManageRoles bool   `json:"can_manage_roles"`
	} `json:"me"`
}

// ------------------------------------------------- request-time resolution

// Touch records a signed-in person in the directory (first and last seen,
// and the roles and projects their token states). It is best effort: a
// failure never blocks the request.
func (s *Service) Touch(ctx context.Context, p auth.Principal) error {
	if p.UserID == "" || p.TenantID == "" || !userIDPattern.MatchString(p.UserID) {
		return nil
	}
	roles := append([]string{}, p.Roles...)
	projects := append([]string{}, p.ProjectIDs...)
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO directory_users (user_id, created_by, first_seen, last_seen, idp_roles, idp_project_ids)
			VALUES ($1, 'sign-in', now(), now(), $2, $3)
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET first_seen = COALESCE(directory_users.first_seen, now()), last_seen = now(),
			  idp_roles = EXCLUDED.idp_roles, idp_project_ids = EXCLUDED.idp_project_ids`,
			p.UserID, roles, projects)
		return err
	})
}

// NameIfBlank gives a person a display name if they have none yet (test
// personas are named after their duty).
func (s *Service) NameIfBlank(ctx context.Context, tenantID, userID, name string) error {
	return s.store.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO directory_users (user_id, display_name, created_by) VALUES ($1, $2, 'sign-in')
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET display_name = EXCLUDED.display_name WHERE directory_users.display_name = ''`, userID, name)
		return err
	})
}

// ApplyDirectory applies the directory to a verified principal: a disabled
// person is refused; a managed person's roles and projects are replaced by
// those given here. Errors fail closed (the caller refuses the request).
func (s *Service) ApplyDirectory(ctx context.Context, p auth.Principal) (auth.Principal, error) {
	if p.UserID == "" || p.TenantID == "" {
		return p, nil
	}
	out := p
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var managed bool
		var status string
		var projects []string
		err := tx.QueryRow(ctx, `SELECT managed, status, project_ids::text[] FROM directory_users WHERE user_id = $1`, p.UserID).
			Scan(&managed, &status, &projects)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != "ACTIVE" {
			return ErrAccountDisabled
		}
		if !managed {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT g.role, COALESCE(r.permissions, '{}') FROM role_grants g
			LEFT JOIN custom_roles r ON r.tenant_id = g.tenant_id AND 'custom:' || r.code = g.role AND r.status = 'ACTIVE'
			WHERE g.user_id = $1 AND g.revoked_at IS NULL ORDER BY g.role`, p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Roles = []string{}
		out.Custom = nil
		for rows.Next() {
			var role string
			var perms []string
			if err := rows.Scan(&role, &perms); err != nil {
				return err
			}
			if !strings.HasPrefix(role, customPrefix) {
				out.Roles = append(out.Roles, role)
				continue
			}
			if out.Custom == nil {
				out.Custom = map[auth.Permission][]string{}
			}
			code := strings.TrimPrefix(role, customPrefix)
			for _, perm := range perms {
				out.Custom[auth.Permission(perm)] = append(out.Custom[auth.Permission(perm)], code)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.ProjectIDs = projects
		return nil
	})
	if err != nil {
		return p, err
	}
	return out, nil
}

// ------------------------------------------------------------ the catalogue

// roleBook resolves role codes to permissions: seed roles from the product
// catalogue, "custom:<code>" from the company's own roles.
type roleBook struct {
	seed   map[string][]auth.Permission
	custom map[string]*RoleInfo // by "custom:<code>"
}

func (b roleBook) perms(role string) ([]auth.Permission, bool) {
	if strings.HasPrefix(role, customPrefix) {
		r, ok := b.custom[role]
		if !ok || r.Status != "ACTIVE" {
			return nil, false
		}
		return r.Permissions, true
	}
	p, ok := b.seed[role]
	return p, ok
}

func (b roleBook) heldBy(roles []string) map[auth.Permission]bool {
	held := map[auth.Permission]bool{}
	for _, r := range roles {
		ps, _ := b.perms(r)
		for _, p := range ps {
			held[p] = true
		}
	}
	return held
}

func (s *Service) loadRoleBook(ctx context.Context, tx pgx.Tx) (roleBook, error) {
	b := roleBook{seed: s.authz.Catalogue(), custom: map[string]*RoleInfo{}}
	rows, err := tx.Query(ctx, `SELECT id::text, code, name, description, permissions, status, created_by, updated_by, updated_at, version
		FROM custom_roles ORDER BY name`)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		r := &RoleInfo{Kind: "company"}
		var perms []string
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Description, &perms, &r.Status, &r.CreatedBy, &r.UpdatedBy, &r.UpdatedAt, &r.Version); err != nil {
			return b, err
		}
		for _, p := range perms {
			r.Permissions = append(r.Permissions, auth.Permission(p))
		}
		r.Stage = auth.HighestStage(r.Permissions)
		r.Code = customPrefix + r.Code
		b.custom[r.Code] = r
	}
	return b, rows.Err()
}

func acceptedPairs(ctx context.Context, tx pgx.Tx, userID string) (map[[2]auth.Permission]bool, error) {
	out := map[[2]auth.Permission]bool{}
	rows, err := tx.Query(ctx, `SELECT perm_a, perm_b FROM sod_exceptions WHERE user_id = $1 AND status = 'APPROVED' AND expires_at > now()`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		x, y := auth.Permission(a), auth.Permission(b)
		if x > y {
			x, y = y, x
		}
		out[[2]auth.Permission{x, y}] = true
	}
	return out, rows.Err()
}

func sortedPerms(held map[auth.Permission]bool) []auth.Permission {
	out := make([]auth.Permission, 0, len(held))
	for p := range held {
		if !strings.HasPrefix(string(p), auth.ReservedPermissionPrefix) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

const personCols = `user_id, display_name, email, job_title, phone, managed, status, status_reason,
	project_ids::text[], idp_roles, idp_project_ids, first_seen, last_seen, created_by, created_at, updated_by, updated_at, version`

func scanPerson(row pgx.Row) (Person, error) {
	var x Person
	var projects, idpProjects []string
	err := row.Scan(&x.UserID, &x.DisplayName, &x.Email, &x.JobTitle, &x.Phone, &x.Managed, &x.Status, &x.StatusReason,
		&projects, &x.idpRoles, &idpProjects, &x.FirstSeen, &x.LastSeen, &x.CreatedBy, &x.CreatedAt, &x.UpdatedBy, &x.UpdatedAt, &x.Version)
	if x.Managed {
		x.ProjectIDs = projects
	} else {
		x.ProjectIDs = idpProjects
	}
	return x, err
}

// fill derives roles, permissions, stage and conflicts of a person.
func (x *Person) fill(b roleBook, grants []RoleGrant) {
	x.Grants = []RoleGrant{}
	if x.Managed {
		x.RoleSource = "directory"
		x.Roles = []string{}
		for _, g := range grants {
			if g.RevokedAt == nil {
				x.Grants = append(x.Grants, g)
				x.Roles = append(x.Roles, g.Role)
			}
		}
	} else {
		x.RoleSource = "sign-in"
		x.Roles = append([]string{}, x.idpRoles...)
	}
	if x.ProjectIDs == nil {
		x.ProjectIDs = []string{}
	}
	held := b.heldBy(x.Roles)
	x.Permissions = sortedPerms(held)
	x.Stage = auth.HighestStage(x.Permissions)
	x.Conflicts = auth.SoDViolations(held, x.accepted)
	if x.Conflicts == nil {
		x.Conflicts = []auth.SoDPair{}
	}
}

func (s *Service) canSeePeople(p auth.Principal) error {
	for _, perm := range []auth.Permission{auth.PermUserManage, auth.PermRoleManage, auth.PermAuditRead} {
		if s.authz.Has(p, perm) {
			return nil
		}
	}
	return fmt.Errorf("%w: user.manage, role.manage or audit.read", auth.ErrForbidden)
}

// People returns the directory, the role catalogue and the access history.
func (s *Service) People(ctx context.Context, p auth.Principal) (PeopleView, error) {
	var v PeopleView
	if err := s.canSeePeople(p); err != nil {
		return v, err
	}
	v.Me.UserID = p.UserID
	v.Me.CanManageUsers = s.authz.Has(p, auth.PermUserManage)
	v.Me.CanManageRoles = s.authz.Has(p, auth.PermRoleManage)
	v.Permissions = s.authz.PermissionCatalog()
	v.Controls = auth.DefaultSoDConflicts
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkSessionTx(ctx, tx, p); err != nil {
			return err
		}
		b, err := s.loadRoleBook(ctx, tx)
		if err != nil {
			return err
		}
		grants := map[string][]RoleGrant{}
		gr, err := tx.Query(ctx, `SELECT id::text, user_id, role, reason, granted_by, granted_at, revoked_at, revoked_by, revoke_reason
			FROM role_grants ORDER BY granted_at`)
		if err != nil {
			return err
		}
		for gr.Next() {
			var g RoleGrant
			var uid string
			if err := gr.Scan(&g.ID, &uid, &g.Role, &g.Reason, &g.GrantedBy, &g.GrantedAt, &g.RevokedAt, &g.RevokedBy, &g.RevokeReason); err != nil {
				gr.Close()
				return err
			}
			grants[uid] = append(grants[uid], g)
		}
		gr.Close()
		if err := gr.Err(); err != nil {
			return err
		}
		accepted := map[string]map[[2]auth.Permission]bool{}
		ex, err := tx.Query(ctx, `SELECT user_id, perm_a, perm_b FROM sod_exceptions WHERE status = 'APPROVED' AND expires_at > now()`)
		if err != nil {
			return err
		}
		for ex.Next() {
			var u, a, c string
			if err := ex.Scan(&u, &a, &c); err != nil {
				ex.Close()
				return err
			}
			x, y := auth.Permission(a), auth.Permission(c)
			if x > y {
				x, y = y, x
			}
			if accepted[u] == nil {
				accepted[u] = map[[2]auth.Permission]bool{}
			}
			accepted[u][[2]auth.Permission{x, y}] = true
		}
		ex.Close()
		if err := ex.Err(); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+personCols+` FROM directory_users ORDER BY lower(COALESCE(NULLIF(display_name, ''), user_id))`)
		if err != nil {
			return err
		}
		v.People = []Person{}
		for rows.Next() {
			x, err := scanPerson(rows)
			if err != nil {
				rows.Close()
				return err
			}
			x.accepted = accepted[x.UserID]
			x.fill(b, grants[x.UserID])
			v.People = append(v.People, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		holders := map[string]int{}
		for _, x := range v.People {
			if x.Status == "ACTIVE" {
				for _, r := range x.Roles {
					holders[r]++
				}
			}
		}
		v.Roles = []RoleInfo{}
		for code, perms := range b.seed {
			v.Roles = append(v.Roles, RoleInfo{Code: code, Name: roleName(code), Kind: "seed", Status: "ACTIVE",
				Description: "Product role. Fixed: the company cannot change it, only give it to people.",
				Permissions: perms, Stage: auth.HighestStage(perms), Holders: holders[code]})
		}
		for _, r := range b.custom {
			r.Holders = holders[r.Code]
			v.Roles = append(v.Roles, *r)
		}
		sort.Slice(v.Roles, func(i, j int) bool {
			if v.Roles[i].Kind != v.Roles[j].Kind {
				return v.Roles[i].Kind == "company"
			}
			return v.Roles[i].Name < v.Roles[j].Name
		})
		if v.Projects, err = projectRefs(ctx, tx); err != nil {
			return err
		}
		v.History, err = accessHistory(ctx, tx, "", 150)
		return err
	})
	return v, err
}

func roleName(code string) string {
	s := strings.ReplaceAll(code, "_", " ")
	for _, a := range []string{"ap ", "ar ", "cfo", "qs"} {
		if strings.HasPrefix(s, a) {
			s = strings.ToUpper(strings.TrimSpace(a)) + s[len(strings.TrimSpace(a)):]
			return s
		}
	}
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func projectRefs(ctx context.Context, tx pgx.Tx) ([]postgres.RefItem, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, code, name FROM projects ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []postgres.RefItem{}
	for rows.Next() {
		var r postgres.RefItem
		if err := rows.Scan(&r.ID, &r.Code, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func accessHistory(ctx context.Context, tx pgx.Tx, who string, limit int) ([]AccessEvent, error) {
	q := `SELECT occurred_at, actor_id, action, aggregate_id, payload FROM audit_log WHERE action LIKE 'people.%'`
	args := []any{limit}
	if who != "" {
		q += ` AND (aggregate_id = $2 OR actor_id = $2)`
		args = append(args, who)
	}
	rows, err := tx.Query(ctx, q+` ORDER BY seq DESC LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessEvent{}
	for rows.Next() {
		var e AccessEvent
		if err := rows.Scan(&e.At, &e.Actor, &e.Action, &e.Subject, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------- directory

// PersonInput adds a person or changes their details.
type PersonInput struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	JobTitle    string    `json:"job_title"`
	Phone       string    `json:"phone"`
	Roles       []string  `json:"roles"`
	ProjectIDs  *[]string `json:"project_ids"`
	Version     int       `json:"version"`
	Reason      string    `json:"reason"`
}

func (in *PersonInput) clean() error {
	in.UserID, in.DisplayName, in.Email = strings.TrimSpace(in.UserID), strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.Email)
	in.JobTitle, in.Phone, in.Reason = strings.TrimSpace(in.JobTitle), strings.TrimSpace(in.Phone), strings.TrimSpace(in.Reason)
	switch {
	case len(in.DisplayName) > 200 || len(in.JobTitle) > 120 || len(in.Phone) > 40:
		return fmt.Errorf("%w: name, job title or phone is too long", ErrInvalidInput)
	case in.Email != "" && (len(in.Email) > 254 || !emailPattern.MatchString(in.Email)):
		return fmt.Errorf("%w: the email address is not valid", ErrInvalidInput)
	}
	if in.ProjectIDs != nil {
		for _, id := range *in.ProjectIDs {
			if !canonUUID.MatchString(id) {
				return fmt.Errorf("%w: project ids must be canonical UUIDs", ErrInvalidInput)
			}
		}
	}
	return nil
}

func (s *Service) peopleTx(ctx context.Context, p auth.Principal, perm auth.Permission, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if p.Kind != auth.KindHuman {
		return fmt.Errorf("%w: only a person may change access", auth.ErrForbidden)
	}
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	// Changing who may do what is a security action: fresh MFA (IAM-04).
	if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkSessionTx(ctx, tx, p); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

func notSelf(p auth.Principal, userID string) error {
	if p.UserID == userID {
		return fmt.Errorf("%w: nobody changes their own access; ask another administrator", auth.ErrSoD)
	}
	return nil
}

// AddPerson registers a person before their first sign-in, with roles and
// projects managed here from the start.
func (s *Service) AddPerson(ctx context.Context, p auth.Principal, in PersonInput) (Person, error) {
	if err := in.clean(); err != nil {
		return Person{}, err
	}
	if !userIDPattern.MatchString(in.UserID) {
		return Person{}, fmt.Errorf("%w: the sign-in id is the person's identity-provider subject: letters, digits and . _ @ : + | - only", ErrInvalidInput)
	}
	if in.DisplayName == "" {
		return Person{}, fmt.Errorf("%w: give the person's name", ErrInvalidInput)
	}
	if err := requireReason(in.Reason); err != nil {
		return Person{}, err
	}
	if err := notSelf(p, in.UserID); err != nil {
		return Person{}, err
	}
	projects := []string{}
	if in.ProjectIDs != nil {
		projects = *in.ProjectIDs
	}
	var out Person
	err := s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM directory_users WHERE user_id = $1)`, in.UserID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: %s is already in the directory; open them to change their access", ErrInvalidInput, in.UserID)
		}
		if err := checkProjects(ctx, tx, projects); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO directory_users (user_id, display_name, email, job_title, phone, managed, project_ids, created_by, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, true, $6::uuid[], $7, $7, now())`,
			in.UserID, in.DisplayName, in.Email, in.JobTitle, in.Phone, projects, p.UserID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, p, "people.person_added", "person", in.UserID, "people.person_added.v1",
			map[string]any{"name": in.DisplayName, "projects": projects, "reason": in.Reason}); err != nil {
			return err
		}
		for _, r := range in.Roles {
			if err := s.grantTx(ctx, tx, p, in.UserID, strings.TrimSpace(r), in.Reason); err != nil {
				return err
			}
		}
		var err error
		out, err = s.personTx(ctx, tx, in.UserID)
		return err
	})
	return out, err
}

func checkProjects(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE id = ANY ($1::uuid[])`, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(dedupe(ids)) {
		return fmt.Errorf("%w: one of the projects does not exist", ErrInvalidInput)
	}
	return nil
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// personTx loads one person with roles, permissions and conflicts.
func (s *Service) personTx(ctx context.Context, tx pgx.Tx, userID string) (Person, error) {
	x, err := scanPerson(tx.QueryRow(ctx, `SELECT `+personCols+` FROM directory_users WHERE user_id = $1`, userID))
	if err != nil {
		return x, mapNoRows(err)
	}
	b, err := s.loadRoleBook(ctx, tx)
	if err != nil {
		return x, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text, role, reason, granted_by, granted_at, revoked_at, revoked_by, revoke_reason
		FROM role_grants WHERE user_id = $1 ORDER BY granted_at`, userID)
	if err != nil {
		return x, err
	}
	var grants []RoleGrant
	for rows.Next() {
		var g RoleGrant
		if err := rows.Scan(&g.ID, &g.Role, &g.Reason, &g.GrantedBy, &g.GrantedAt, &g.RevokedAt, &g.RevokedBy, &g.RevokeReason); err != nil {
			rows.Close()
			return x, err
		}
		grants = append(grants, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return x, err
	}
	if x.accepted, err = acceptedPairs(ctx, tx, userID); err != nil {
		return x, err
	}
	x.fill(b, grants)
	return x, nil
}

// GetPerson returns one person, with their full grant history.
func (s *Service) GetPerson(ctx context.Context, p auth.Principal, userID string) (Person, []RoleGrant, []AccessEvent, error) {
	var x Person
	var hist []RoleGrant
	var events []AccessEvent
	if err := s.canSeePeople(p); err != nil {
		return x, nil, nil, err
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if x, err = s.personTx(ctx, tx, userID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, role, reason, granted_by, granted_at, revoked_at, revoked_by, revoke_reason
			FROM role_grants WHERE user_id = $1 ORDER BY granted_at DESC`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		hist = []RoleGrant{}
		for rows.Next() {
			var g RoleGrant
			if err := rows.Scan(&g.ID, &g.Role, &g.Reason, &g.GrantedBy, &g.GrantedAt, &g.RevokedAt, &g.RevokedBy, &g.RevokeReason); err != nil {
				return err
			}
			hist = append(hist, g)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		events, err = accessHistory(ctx, tx, userID, 50)
		return err
	})
	return x, hist, events, err
}

// UpdatePerson changes a person's details and, for a managed person, their
// projects. The expected version guards against a concurrent change.
func (s *Service) UpdatePerson(ctx context.Context, p auth.Principal, userID string, in PersonInput) (Person, error) {
	if err := in.clean(); err != nil {
		return Person{}, err
	}
	if err := requireReason(in.Reason); err != nil {
		return Person{}, err
	}
	var out Person
	err := s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := s.personTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		if cur.Version != in.Version {
			return postgres.ErrVersionConflict
		}
		projects := cur.ProjectIDs
		if in.ProjectIDs != nil {
			if !cur.Managed {
				return fmt.Errorf("%w: projects come from the sign-in until the person's access is managed here", ErrInvalidInput)
			}
			if err := notSelf(p, userID); err != nil {
				return err
			}
			projects = dedupe(*in.ProjectIDs)
			if err := checkProjects(ctx, tx, projects); err != nil {
				return err
			}
		}
		if !cur.Managed {
			projects = nil
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE directory_users SET display_name = $2, email = $3, job_title = $4, phone = $5,
			project_ids = COALESCE($6::uuid[], project_ids), updated_by = $7, updated_at = clock_timestamp() WHERE user_id = $1`,
			userID, in.DisplayName, in.Email, in.JobTitle, in.Phone, nilIfEmpty(projects, cur.Managed), p.UserID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, p, "people.details_changed", "person", userID, "people.details_changed.v1",
			map[string]any{"name": in.DisplayName, "job_title": in.JobTitle, "projects": projects, "reason": in.Reason}); err != nil {
			return err
		}
		out, err = s.personTx(ctx, tx, userID)
		return err
	})
	return out, err
}

func nilIfEmpty(projects []string, managed bool) any {
	if !managed {
		return nil
	}
	if projects == nil {
		return []string{}
	}
	return projects
}

// ManagePerson takes a person's access under management: from now on the
// roles and projects given here apply, starting from exactly what their
// sign-in gave them, so nothing changes at the moment of the switch.
func (s *Service) ManagePerson(ctx context.Context, p auth.Principal, userID, reason string) (Person, error) {
	if err := requireReason(reason); err != nil {
		return Person{}, err
	}
	if err := notSelf(p, userID); err != nil {
		return Person{}, err
	}
	var out Person
	err := s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := s.personTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		if cur.Managed {
			return fmt.Errorf("%w: this person's access is already managed here", ErrInvalidInput)
		}
		if cur.Status != "ACTIVE" {
			return fmt.Errorf("%w: enable the person first", ErrInvalidInput)
		}
		var keep []string
		for _, id := range cur.ProjectIDs {
			if canonUUID.MatchString(id) {
				keep = append(keep, id)
			}
		}
		if keep == nil {
			keep = []string{}
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE directory_users SET managed = true,
			project_ids = ARRAY(SELECT p.id FROM projects p WHERE p.id = ANY ($2::uuid[])), updated_by = $3, updated_at = clock_timestamp()
			WHERE user_id = $1`, userID, keep, p.UserID); err != nil {
			return err
		}
		b, err := s.loadRoleBook(ctx, tx)
		if err != nil {
			return err
		}
		var copied []string
		for _, r := range cur.idpRoles {
			if _, ok := b.perms(r); !ok {
				continue // a role the product does not know gives nothing
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_grants (user_id, role, reason, granted_by) VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, userID, r, "Copied from sign-in when access came under management: "+reason, p.UserID); err != nil {
				return err
			}
			copied = append(copied, r)
		}
		if err := s.record(ctx, tx, p, "people.managed", "person", userID, "people.managed.v1",
			map[string]any{"roles": copied, "reason": reason}); err != nil {
			return err
		}
		out, err = s.personTx(ctx, tx, userID)
		return err
	})
	return out, err
}

// SetPersonStatus disables or re-enables a person. Disabling also signs the
// person out everywhere at once.
func (s *Service) SetPersonStatus(ctx context.Context, p auth.Principal, userID string, active bool, reason string) (Person, error) {
	if err := requireReason(reason); err != nil {
		return Person{}, err
	}
	if err := notSelf(p, userID); err != nil {
		return Person{}, err
	}
	var out Person
	err := s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := s.personTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		want := map[bool]string{true: "ACTIVE", false: "DISABLED"}[active]
		if cur.Status == want {
			return fmt.Errorf("%w: the person is already %s", ErrInvalidInput, strings.ToLower(want))
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE directory_users SET status = $2, status_reason = $3, updated_by = $4, updated_at = clock_timestamp()
			WHERE user_id = $1`, userID, want, reason, p.UserID); err != nil {
			return err
		}
		action := "people.enabled"
		if !active {
			action = "people.disabled"
			if _, err := tx.Exec(ctx, `INSERT INTO session_revocations (subject, reason, revoked_by) VALUES ($1, $2, $3)`,
				userID, "Disabled in the company directory: "+reason, p.UserID); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, p, action, "person", userID, action+".v1", map[string]any{"reason": reason}); err != nil {
			return err
		}
		out, err = s.personTx(ctx, tx, userID)
		return err
	})
	return out, err
}

// GrantRole gives a managed person a role.
func (s *Service) GrantRole(ctx context.Context, p auth.Principal, userID, role, reason string) (Person, error) {
	role, reason = strings.TrimSpace(role), strings.TrimSpace(reason)
	if err := requireReason(reason); err != nil {
		return Person{}, err
	}
	if err := notSelf(p, userID); err != nil {
		return Person{}, err
	}
	var out Person
	err := s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.grantTx(ctx, tx, p, userID, role, reason); err != nil {
			return err
		}
		var err error
		out, err = s.personTx(ctx, tx, userID)
		return err
	})
	return out, err
}

func (s *Service) grantTx(ctx context.Context, tx pgx.Tx, p auth.Principal, userID, role, reason string) error {
	if err := notSelf(p, userID); err != nil {
		return err
	}
	cur, err := s.personTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if !cur.Managed {
		return fmt.Errorf("%w: this person's roles come from their sign-in; take their access under management first", ErrInvalidInput)
	}
	if cur.Status != "ACTIVE" {
		return fmt.Errorf("%w: the person is disabled", ErrInvalidInput)
	}
	b, err := s.loadRoleBook(ctx, tx)
	if err != nil {
		return err
	}
	if _, ok := b.perms(role); !ok {
		return fmt.Errorf("%w: there is no active role %q", ErrInvalidInput, role)
	}
	for _, r := range cur.Roles {
		if r == role {
			return fmt.Errorf("%w: the person already holds this role", ErrInvalidInput)
		}
	}
	held := b.heldBy(append(append([]string{}, cur.Roles...), role))
	if v := auth.SoDViolations(held, cur.accepted); len(v) > 0 {
		return conflictError(cur.label(), v)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO role_grants (user_id, role, reason, granted_by) VALUES ($1, $2, $3, $4)`,
		userID, role, reason, p.UserID); err != nil {
		return err
	}
	return s.record(ctx, tx, p, "people.role_granted", "person", userID, "people.role_granted.v1",
		map[string]any{"role": role, "reason": reason})
}

func (x Person) label() string {
	if x.DisplayName != "" {
		return x.DisplayName
	}
	return x.UserID
}

func conflictError(who string, v []auth.SoDPair) error {
	parts := make([]string, 0, len(v))
	for _, c := range v {
		parts = append(parts, fmt.Sprintf("%s (%s + %s)", c.Control, auth.Label(c.A).Label, auth.Label(c.B).Label))
	}
	return fmt.Errorf("%w: this would let %s hold both halves of a control: %s. Choose another role, or record an approved duty-conflict exception for this person first",
		auth.ErrSoD, who, strings.Join(parts, "; "))
}

// RevokeRole ends one role grant. The grant stays in the history.
func (s *Service) RevokeRole(ctx context.Context, p auth.Principal, grantID, reason string) (Person, error) {
	reason = strings.TrimSpace(reason)
	if err := requireReason(reason); err != nil {
		return Person{}, err
	}
	var out Person
	err := s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		var userID, role string
		var revoked *time.Time
		if err := tx.QueryRow(ctx, `SELECT user_id, role, revoked_at FROM role_grants WHERE id = $1::uuid FOR UPDATE`, grantID).
			Scan(&userID, &role, &revoked); err != nil {
			return mapNoRows(err)
		}
		if err := notSelf(p, userID); err != nil {
			return err
		}
		if revoked != nil {
			return fmt.Errorf("%w: this role was already removed", ErrInvalidInput)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE role_grants SET revoked_at = now(), revoked_by = $2, revoke_reason = $3 WHERE id = $1::uuid`,
			grantID, p.UserID, reason); err != nil {
			return err
		}
		if err := s.record(ctx, tx, p, "people.role_removed", "person", userID, "people.role_removed.v1",
			map[string]any{"role": role, "reason": reason}); err != nil {
			return err
		}
		var err error
		out, err = s.personTx(ctx, tx, userID)
		return err
	})
	return out, err
}

// ------------------------------------------------------- company roles

// RoleInput creates or changes a company role.
type RoleInput struct {
	Code        string            `json:"code"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Permissions []auth.Permission `json:"permissions"`
	Version     int               `json:"version"`
	Reason      string            `json:"reason"`
}

func (s *Service) checkRolePerms(perms []auth.Permission) ([]string, error) {
	known := s.authz.KnownPermissions()
	held := map[auth.Permission]bool{}
	for _, perm := range perms {
		if !known[perm] || strings.HasPrefix(string(perm), auth.ReservedPermissionPrefix) {
			return nil, fmt.Errorf("%w: %q is not a permission of the catalogue", ErrInvalidInput, perm)
		}
		held[perm] = true
	}
	if len(held) == 0 {
		return nil, fmt.Errorf("%w: tick at least one permission", ErrInvalidInput)
	}
	if v := auth.SoDViolations(held, nil); len(v) > 0 {
		return nil, conflictError("anyone holding this role", v)
	}
	out := make([]string, 0, len(held))
	for _, p := range sortedPerms(held) {
		out = append(out, string(p))
	}
	return out, nil
}

// SaveRole creates a company role (Version 0) or replaces the name,
// description and permissions of an existing one. A change is refused if
// it would give any present holder a duty conflict.
func (s *Service) SaveRole(ctx context.Context, p auth.Principal, in RoleInput) (RoleInfo, error) {
	in.Code, in.Name, in.Description, in.Reason = strings.TrimSpace(strings.TrimPrefix(in.Code, customPrefix)), strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.Reason)
	if !roleCodePattern.MatchString(in.Code) {
		return RoleInfo{}, fmt.Errorf("%w: the role code is 3 to 41 lowercase letters, digits or underscores, starting with a letter", ErrInvalidInput)
	}
	if _, seed := s.authz.Catalogue()[in.Code]; seed {
		return RoleInfo{}, fmt.Errorf("%w: %q is a product role; choose another code", ErrInvalidInput, in.Code)
	}
	if len(in.Name) < 2 || len(in.Name) > 80 || len(in.Description) > 500 {
		return RoleInfo{}, fmt.Errorf("%w: the name is 2 to 80 characters, the description at most 500", ErrInvalidInput)
	}
	if err := requireReason(in.Reason); err != nil {
		return RoleInfo{}, err
	}
	perms, err := s.checkRolePerms(in.Permissions)
	if err != nil {
		return RoleInfo{}, err
	}
	var out RoleInfo
	err = s.peopleTx(ctx, p, auth.PermRoleManage, func(ctx context.Context, tx pgx.Tx) error {
		action := "people.role_created"
		if in.Version == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO custom_roles (code, name, description, permissions, created_by) VALUES ($1, $2, $3, $4, $5)`,
				in.Code, in.Name, in.Description, perms, p.UserID); err != nil {
				if strings.Contains(err.Error(), "custom_roles_tenant_id_code_key") {
					return fmt.Errorf("%w: a company role with the code %q already exists", ErrInvalidInput, in.Code)
				}
				return err
			}
		} else {
			action = "people.role_changed"
			var version int
			if err := tx.QueryRow(ctx, `SELECT version FROM custom_roles WHERE code = $1 FOR UPDATE`, in.Code).Scan(&version); err != nil {
				return mapNoRows(err)
			}
			if version != in.Version {
				return postgres.ErrVersionConflict
			}
			// Every present holder must stay free of duty conflicts.
			b, err := s.loadRoleBook(ctx, tx)
			if err != nil {
				return err
			}
			newPerms := make([]auth.Permission, len(perms))
			for i, x := range perms {
				newPerms[i] = auth.Permission(x)
			}
			b.custom[customPrefix+in.Code].Permissions = newPerms
			rows, err := tx.Query(ctx, `SELECT DISTINCT user_id FROM role_grants WHERE role = $1 AND revoked_at IS NULL`, customPrefix+in.Code)
			if err != nil {
				return err
			}
			var holders []string
			for rows.Next() {
				var u string
				if err := rows.Scan(&u); err != nil {
					rows.Close()
					return err
				}
				holders = append(holders, u)
			}
			rows.Close()
			for _, u := range holders {
				x, err := s.personTx(ctx, tx, u)
				if err != nil {
					return err
				}
				if v := auth.SoDViolations(b.heldBy(x.Roles), x.accepted); len(v) > 0 {
					return conflictError(x.label(), v)
				}
			}
			if err := postgres.Exec1(ctx, tx, `UPDATE custom_roles SET name = $2, description = $3, permissions = $4, updated_by = $5, updated_at = now()
				WHERE code = $1`, in.Code, in.Name, in.Description, perms, p.UserID); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, p, action, "role", customPrefix+in.Code, action+".v1",
			map[string]any{"name": in.Name, "permissions": perms, "reason": in.Reason}); err != nil {
			return err
		}
		b, err := s.loadRoleBook(ctx, tx)
		if err != nil {
			return err
		}
		out = *b.custom[customPrefix+in.Code]
		return nil
	})
	return out, err
}

// RetireRole retires a company role nobody holds any more.
func (s *Service) RetireRole(ctx context.Context, p auth.Principal, code, reason string) error {
	code = strings.TrimPrefix(strings.TrimSpace(code), customPrefix)
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.peopleTx(ctx, p, auth.PermRoleManage, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_grants WHERE role = $1 AND revoked_at IS NULL`, customPrefix+code).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: %d people still hold this role; remove it from them first", ErrInvalidInput, n)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE custom_roles SET status = 'RETIRED', updated_by = $2, updated_at = now() WHERE code = $1 AND status = 'ACTIVE'`,
			code, p.UserID); err != nil {
			return mapNoRows(err)
		}
		return s.record(ctx, tx, p, "people.role_retired", "role", customPrefix+code, "people.role_retired.v1", map[string]any{"reason": reason})
	})
}

// ------------------------------------------------------------- my profile

// Profile is what a person sees about themselves.
type Profile struct {
	UserID      string                `json:"user_id"`
	TenantID    string                `json:"tenant_id"`
	Kind        string                `json:"kind"`
	DisplayName string                `json:"display_name"`
	Email       string                `json:"email"`
	JobTitle    string                `json:"job_title"`
	Phone       string                `json:"phone"`
	RoleSource  string                `json:"role_source"`
	Roles       []RoleInfo            `json:"roles"`
	Permissions []auth.PermissionInfo `json:"permissions"`
	Lent        []LentPermission      `json:"lent_to_me"`
	TenantWide  bool                  `json:"tenant_wide"`
	Projects    []postgres.RefItem    `json:"projects"`
	Stage       string                `json:"stage"`
	Exceptions  []string              `json:"duty_exceptions"`
	MFA         bool                  `json:"mfa"`
	AuthTime    *time.Time            `json:"auth_time,omitempty"`
	IssuedAt    *time.Time            `json:"issued_at,omitempty"`
	FirstSeen   *time.Time            `json:"first_seen,omitempty"`
	LastSeen    *time.Time            `json:"last_seen,omitempty"`
	Activity    []AccessEvent         `json:"recent_activity"`
	Version     int                   `json:"version"`
}

// LentPermission is authority a colleague has lent the person.
type LentPermission struct {
	Permission auth.PermissionInfo `json:"permission"`
	From       []string            `json:"from"`
}

// MyProfile describes the signed-in person: who they are, what they may do
// and why (roles, company roles, delegations), where, and what they did last.
func (s *Service) MyProfile(ctx context.Context, p auth.Principal) (Profile, error) {
	pr := Profile{UserID: p.UserID, TenantID: p.TenantID, Kind: p.Kind, MFA: p.MFA, RoleSource: "sign-in",
		TenantWide: s.tenantWide(p), Roles: []RoleInfo{}, Lent: []LentPermission{}, Projects: []postgres.RefItem{}, Exceptions: []string{}}
	if !p.AuthTime.IsZero() {
		t := p.AuthTime
		pr.AuthTime = &t
	}
	if !p.IssuedAt.IsZero() {
		t := p.IssuedAt
		pr.IssuedAt = &t
	}
	if p.UserID == "" || p.TenantID == "" {
		return pr, auth.ErrUnauthenticated
	}
	for _, perm := range s.authz.Permissions(p) {
		pr.Permissions = append(pr.Permissions, auth.Label(perm))
	}
	if pr.Permissions == nil {
		pr.Permissions = []auth.PermissionInfo{}
	}
	sort.Slice(pr.Permissions, func(i, j int) bool {
		if pr.Permissions[i].Area != pr.Permissions[j].Area {
			return pr.Permissions[i].Area < pr.Permissions[j].Area
		}
		return pr.Permissions[i].Label < pr.Permissions[j].Label
	})
	perms := s.authz.Permissions(p)
	pr.Stage = auth.HighestStage(perms)
	for perm, from := range p.Delegated {
		pr.Lent = append(pr.Lent, LentPermission{Permission: auth.Label(perm), From: from})
	}
	sort.Slice(pr.Lent, func(i, j int) bool { return pr.Lent[i].Permission.Label < pr.Lent[j].Permission.Label })
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkSessionTx(ctx, tx, p); err != nil {
			return err
		}
		x, err := scanPerson(tx.QueryRow(ctx, `SELECT `+personCols+` FROM directory_users WHERE user_id = $1`, p.UserID))
		if err == nil {
			pr.DisplayName, pr.Email, pr.JobTitle, pr.Phone = x.DisplayName, x.Email, x.JobTitle, x.Phone
			pr.FirstSeen, pr.LastSeen, pr.Version = x.FirstSeen, x.LastSeen, x.Version
			if x.Managed {
				pr.RoleSource = "directory"
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		b, err := s.loadRoleBook(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range p.Roles {
			if ps, ok := b.seed[r]; ok {
				pr.Roles = append(pr.Roles, RoleInfo{Code: r, Name: roleName(r), Kind: "seed", Status: "ACTIVE", Permissions: ps, Stage: auth.HighestStage(ps)})
			}
		}
		seen := map[string]bool{}
		for _, codes := range p.Custom {
			for _, c := range codes {
				if r, ok := b.custom[customPrefix+c]; ok && !seen[c] {
					seen[c] = true
					pr.Roles = append(pr.Roles, *r)
				}
			}
		}
		all, err := projectRefs(ctx, tx)
		if err != nil {
			return err
		}
		scope := s.authz.ScopeOf(p)
		for _, x := range all {
			if !pr.TenantWide && scope.Allows(x.ID) {
				pr.Projects = append(pr.Projects, x)
			}
		}
		rows, err := tx.Query(ctx, `SELECT perm_a, perm_b, expires_at FROM sod_exceptions WHERE user_id = $1 AND status = 'APPROVED' AND expires_at > now()`, p.UserID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, c string
			var until time.Time
			if err := rows.Scan(&a, &c, &until); err != nil {
				rows.Close()
				return err
			}
			pr.Exceptions = append(pr.Exceptions, fmt.Sprintf("%s together with %s, until %s",
				auth.Label(auth.Permission(a)).Label, strings.ToLower(auth.Label(auth.Permission(c)).Label), until.Format("2 Jan 2006")))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		act, err := tx.Query(ctx, `SELECT occurred_at, actor_id, action, aggregate_id, payload FROM audit_log WHERE actor_id = $1 ORDER BY seq DESC LIMIT 15`, p.UserID)
		if err != nil {
			return err
		}
		defer act.Close()
		pr.Activity = []AccessEvent{}
		for act.Next() {
			var e AccessEvent
			if err := act.Scan(&e.At, &e.Actor, &e.Action, &e.Subject, &e.Detail); err != nil {
				return err
			}
			pr.Activity = append(pr.Activity, e)
		}
		return act.Err()
	})
	sort.Slice(pr.Roles, func(i, j int) bool { return pr.Roles[i].Name < pr.Roles[j].Name })
	return pr, err
}

// ProfileInput is what a person may change about themselves.
type ProfileInput struct {
	DisplayName string `json:"display_name"`
	JobTitle    string `json:"job_title"`
	Phone       string `json:"phone"`
}

// UpdateMyProfile changes the signed-in person's name, job title and phone.
// Email and access are kept by administrators and the identity provider.
func (s *Service) UpdateMyProfile(ctx context.Context, p auth.Principal, in ProfileInput) (Profile, error) {
	in.DisplayName, in.JobTitle, in.Phone = strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.JobTitle), strings.TrimSpace(in.Phone)
	if in.DisplayName == "" || len(in.DisplayName) > 200 || len(in.JobTitle) > 120 || len(in.Phone) > 40 {
		return Profile{}, fmt.Errorf("%w: give your name (at most 200 characters); job title at most 120, phone at most 40", ErrInvalidInput)
	}
	if !userIDPattern.MatchString(p.UserID) {
		return Profile{}, fmt.Errorf("%w: this sign-in cannot keep a profile", ErrInvalidInput)
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkSessionTx(ctx, tx, p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO directory_users (user_id, display_name, job_title, phone, created_by, first_seen, last_seen)
			VALUES ($1, $2, $3, $4, 'sign-in', now(), now())
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET display_name = EXCLUDED.display_name, job_title = EXCLUDED.job_title,
			  phone = EXCLUDED.phone, updated_by = $1, updated_at = clock_timestamp()`, p.UserID, in.DisplayName, in.JobTitle, in.Phone); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "people.profile_changed", "person", p.UserID, "people.profile_changed.v1",
			map[string]any{"name": in.DisplayName, "job_title": in.JobTitle})
	})
	if err != nil {
		return Profile{}, err
	}
	return s.MyProfile(ctx, p)
}
