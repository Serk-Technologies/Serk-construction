-- 0048 People and access (SRS IAM-05, IAM-06, IAM-09, IAM-11, ADM-01).
--
-- The company directory: who works here, what they may do and on which
-- projects. A person signs in through the identity provider; the directory
-- records them on first sign-in (roles as the token states them). When an
-- administrator takes a person "under management", the roles and projects
-- given here replace the token's on the very next request, and disabling a
-- person refuses every request at once.
--
--   directory_users  one row per person; profile, status, projects
--   custom_roles     the company's own roles, composed from the fixed
--                    permission list (the kernel refuses unknown permissions
--                    and any pair of duties that must be kept apart)
--   role_grants      who holds which role; never deleted, a removal is a
--                    dated revocation with a reason, so history stays whole
--
-- Nobody changes their own access: the database refuses a grant, a
-- revocation, a status, project or management change made by the person it
-- is about.

CREATE TABLE directory_users (
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  user_id         text NOT NULL CHECK (user_id ~ '^[A-Za-z0-9._@:+|-]{1,200}$'),
  display_name    text NOT NULL DEFAULT '' CHECK (length(display_name) <= 200),
  email           text NOT NULL DEFAULT '' CHECK (length(email) <= 254 AND (email = '' OR email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$')),
  job_title       text NOT NULL DEFAULT '' CHECK (length(job_title) <= 120),
  phone           text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  managed         boolean NOT NULL DEFAULT false,
  status          text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
  status_reason   text,
  project_ids     uuid[] NOT NULL DEFAULT '{}',
  idp_roles       text[] NOT NULL DEFAULT '{}',
  idp_project_ids text[] NOT NULL DEFAULT '{}',
  first_seen      timestamptz,
  last_seen       timestamptz,
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      text,
  updated_at      timestamptz,
  version         integer NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, user_id),
  CHECK (status = 'ACTIVE' OR length(btrim(COALESCE(status_reason, ''))) >= 5)
);

CREATE TABLE custom_roles (
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  id          uuid NOT NULL DEFAULT gen_random_uuid(),
  code        text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_]{2,40}$'),
  name        text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 80),
  description text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  permissions text[] NOT NULL CHECK (cardinality(permissions) BETWEEN 1 AND 200),
  status      text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'RETIRED')),
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  text,
  updated_at  timestamptz,
  version     integer NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);

CREATE TABLE role_grants (
  tenant_id     uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  id            uuid NOT NULL DEFAULT gen_random_uuid(),
  user_id       text NOT NULL,
  role          text NOT NULL CHECK (role ~ '^(custom:)?[a-z][a-z0-9_]{1,60}$'),
  reason        text NOT NULL CHECK (length(btrim(reason)) >= 5),
  granted_by    text NOT NULL,
  granted_at    timestamptz NOT NULL DEFAULT now(),
  revoked_at    timestamptz,
  revoked_by    text,
  revoke_reason text,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES directory_users(tenant_id, user_id),
  CONSTRAINT role_grants_not_self CHECK (granted_by <> user_id),
  CONSTRAINT role_grants_revoke_not_self CHECK (revoked_by IS NULL OR revoked_by <> user_id),
  CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
  CHECK (revoked_at IS NULL OR length(btrim(COALESCE(revoke_reason, ''))) >= 5)
);
CREATE UNIQUE INDEX role_grants_one_active ON role_grants (tenant_id, user_id, role) WHERE revoked_at IS NULL;
CREATE INDEX role_grants_role ON role_grants (tenant_id, role) WHERE revoked_at IS NULL;

-- ------------------------------------------------------------- guards ---
CREATE FUNCTION directory_users_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.user_id <> OLD.user_id OR NEW.tenant_id <> OLD.tenant_id OR NEW.created_by <> OLD.created_by
     OR NEW.created_at <> OLD.created_at OR NEW.first_seen IS DISTINCT FROM OLD.first_seen THEN
    RAISE EXCEPTION 'a person''s identity and creation record cannot be changed' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status <> OLD.status OR NEW.managed <> OLD.managed OR NEW.project_ids <> OLD.project_ids THEN
    IF NEW.updated_by IS NULL OR NEW.updated_by = NEW.user_id OR NEW.updated_by IS NOT DISTINCT FROM OLD.updated_by AND NEW.updated_at IS NOT DISTINCT FROM OLD.updated_at THEN
      RAISE EXCEPTION 'nobody changes their own access: another administrator must do it' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  IF OLD.managed AND NOT NEW.managed THEN
    RAISE EXCEPTION 'a managed person stays managed; disable them or change their roles instead' USING ERRCODE = 'check_violation';
  END IF;
  -- Sign-in bookkeeping (last_seen, idp_*) does not make a new version.
  IF (to_jsonb(NEW) - ARRAY['last_seen', 'idp_roles', 'idp_project_ids', 'version'])
     IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['last_seen', 'idp_roles', 'idp_project_ids', 'version']) THEN
    NEW.version := OLD.version + 1;
  ELSE
    NEW.version := OLD.version;
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER directory_users_guard BEFORE UPDATE ON directory_users FOR EACH ROW EXECUTE FUNCTION directory_users_guard();

CREATE FUNCTION custom_roles_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.code <> OLD.code OR NEW.id <> OLD.id OR NEW.created_by <> OLD.created_by OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'a role''s code and creation record cannot be changed' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'RETIRED' THEN
    RAISE EXCEPTION 'a retired role cannot be changed' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.updated_by IS NULL THEN
    RAISE EXCEPTION 'a role change names who made it' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'RETIRED' AND EXISTS (SELECT 1 FROM role_grants g WHERE g.tenant_id = NEW.tenant_id AND g.role = 'custom:' || NEW.code AND g.revoked_at IS NULL) THEN
    RAISE EXCEPTION 'people still hold this role; remove it from them first' USING ERRCODE = 'check_violation';
  END IF;
  NEW.version := OLD.version + 1;
  RETURN NEW;
END$$;
CREATE TRIGGER custom_roles_guard BEFORE UPDATE ON custom_roles FOR EACH ROW EXECUTE FUNCTION custom_roles_guard();

CREATE FUNCTION role_grants_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE u directory_users;
BEGIN
  IF TG_OP = 'INSERT' THEN
    SELECT * INTO u FROM directory_users WHERE tenant_id = NEW.tenant_id AND user_id = NEW.user_id;
    IF NOT FOUND OR NOT u.managed OR u.status <> 'ACTIVE' THEN
      RAISE EXCEPTION 'roles are given only to active people whose access is managed here' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.role LIKE 'custom:%' AND NOT EXISTS (SELECT 1 FROM custom_roles r WHERE r.tenant_id = NEW.tenant_id
         AND 'custom:' || r.code = NEW.role AND r.status = 'ACTIVE') THEN
      RAISE EXCEPTION 'that company role does not exist or is retired' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.revoked_at IS NOT NULL THEN
      RAISE EXCEPTION 'a grant starts active' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.revoked_at IS NOT NULL THEN
    RAISE EXCEPTION 'this role was already removed' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['revoked_at', 'revoked_by', 'revoke_reason'], 'role grant');
  IF NEW.revoked_at IS NULL THEN
    RAISE EXCEPTION 'a role grant can only be ended' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER role_grants_guard BEFORE INSERT OR UPDATE ON role_grants FOR EACH ROW EXECUTE FUNCTION role_grants_guard();

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['directory_users', 'custom_roles', 'role_grants'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (display_name, email, job_title, phone, managed, status, status_reason, project_ids,
              idp_roles, idp_project_ids, last_seen, updated_by, updated_at) ON directory_users TO serk_app;
GRANT UPDATE (name, description, permissions, status, updated_by, updated_at) ON custom_roles TO serk_app;
GRANT UPDATE (revoked_at, revoked_by, revoke_reason) ON role_grants TO serk_app;
