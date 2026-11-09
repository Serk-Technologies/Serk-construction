-- 0049 People and access, follow-up to 0048.
--
--  * The three guard functions get a pinned search_path, like every other
--    function of the schema (privilege catalogue invariant).
--  * "First seen" may be filled once: a person added before their first
--    sign-in gets the date when they first sign in. It never changes after.

CREATE OR REPLACE FUNCTION directory_users_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.user_id <> OLD.user_id OR NEW.tenant_id <> OLD.tenant_id OR NEW.created_by <> OLD.created_by
     OR NEW.created_at <> OLD.created_at OR (OLD.first_seen IS NOT NULL AND NEW.first_seen IS DISTINCT FROM OLD.first_seen) THEN
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
  IF (to_jsonb(NEW) - ARRAY['first_seen', 'last_seen', 'idp_roles', 'idp_project_ids', 'version'])
     IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['first_seen', 'last_seen', 'idp_roles', 'idp_project_ids', 'version']) THEN
    NEW.version := OLD.version + 1;
  ELSE
    NEW.version := OLD.version;
  END IF;
  RETURN NEW;
END$$;

CREATE OR REPLACE FUNCTION custom_roles_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
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

CREATE OR REPLACE FUNCTION role_grants_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
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

GRANT UPDATE (first_seen) ON directory_users TO serk_app;
