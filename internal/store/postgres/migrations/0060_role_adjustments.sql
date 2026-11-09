-- 0060 Adjusting the product roles (IAM-05 follow-up)
--
-- A company may narrow any built-in role at any time: the permissions listed
-- here are withheld from everyone holding that role in this company, from
-- their next request. A role is only ever narrowed this way (to give more,
-- use a company role); withheld permissions can be restored by removing them
-- from the list. user.manage and role.manage are never withheld, so access
-- administration can always be repaired. Every change carries a reason and a
-- new version; the history is the audit log.

CREATE TABLE role_adjustments (
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  role_code  text NOT NULL CHECK (role_code ~ '^[a-z][a-z0-9_]{1,60}$'),
  withheld   text[] NOT NULL DEFAULT '{}',
  version    integer NOT NULL DEFAULT 1,
  reason     text NOT NULL CHECK (length(btrim(reason)) >= 5),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, role_code),
  CONSTRAINT role_adjustments_admin_kept CHECK (NOT (withheld && ARRAY['user.manage', 'role.manage']::text[])),
  CONSTRAINT role_adjustments_perm_shape CHECK (array_position(withheld, NULL) IS NULL)
);

CREATE FUNCTION role_adjustments_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE p text;
BEGIN
  FOREACH p IN ARRAY NEW.withheld LOOP
    IF p !~ '^[a-z][a-z0-9_.]{1,80}$' THEN
      RAISE EXCEPTION 'not a permission: %', p USING ERRCODE = 'check_violation';
    END IF;
  END LOOP;
  IF TG_OP = 'UPDATE' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['withheld', 'version', 'reason', 'updated_by', 'updated_at'], 'role adjustment');
    NEW.version := OLD.version + 1;
    NEW.updated_at := now();
  ELSIF NEW.version <> 1 THEN
    RAISE EXCEPTION 'a role adjustment starts at version 1' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER role_adjustments_guard BEFORE INSERT OR UPDATE ON role_adjustments FOR EACH ROW EXECUTE FUNCTION role_adjustments_guard();

ALTER TABLE role_adjustments ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_adjustments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON role_adjustments USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
DO $$ BEGIN
  EXECUTE format('CREATE POLICY owner_maintenance ON role_adjustments TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', current_user);
END$$;
REVOKE ALL ON role_adjustments FROM PUBLIC;
GRANT SELECT, INSERT ON role_adjustments TO serk_app;
GRANT UPDATE (withheld, version, reason, updated_by, updated_at) ON role_adjustments TO serk_app;
