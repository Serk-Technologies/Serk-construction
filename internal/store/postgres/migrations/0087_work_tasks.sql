-- 0087_work_tasks.sql — assignable tasks with due dates, outside document approvals (CNX-07.2).
--
-- A person asks another (or themselves) to do something by a date, optionally on a project: chase a supplier,
-- prepare a method statement, book a crane. Only an ACTIVE person of the directory is assigned. The task is OPEN
-- until the person it is assigned to marks it DONE (with an optional note), or the person who created it cancels it
-- with a reason; while it is open its creator may reassign it or move its date. A task is never deleted. Tasks are
-- coordination, not approvals: no document, payment or posting depends on them, so they carry no attestation; the
-- database still requires that a task is completed in the name of its assignee and cancelled in the name of its
-- creator, and the kernel takes those names from the signed-in person only.

CREATE TABLE work_tasks (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id       uuid,
  number           text NOT NULL,
  title            text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 200),
  detail           text NOT NULL DEFAULT '' CHECK (length(detail) <= 4000),
  priority         text NOT NULL DEFAULT 'NORMAL' CHECK (priority IN ('LOW', 'NORMAL', 'HIGH')),
  assigned_to      text NOT NULL,
  due_on           date NOT NULL,
  status           text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'DONE', 'CANCELLED')),
  completed_by     text,
  completed_at     timestamptz,
  completion_note  text CHECK (completion_note IS NULL OR length(completion_note) <= 2000),
  cancelled_by     text,
  cancelled_reason text,
  created_by       text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  FOREIGN KEY (tenant_id, assigned_to) REFERENCES directory_users(tenant_id, user_id)
);
CREATE INDEX work_tasks_assignee ON work_tasks (tenant_id, assigned_to, status, due_on);
CREATE INDEX work_tasks_creator ON work_tasks (tenant_id, created_by, status, due_on);

CREATE FUNCTION work_tasks_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'tasks are never deleted' USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'INSERT' OR NEW.assigned_to IS DISTINCT FROM OLD.assigned_to THEN
    IF NOT EXISTS (SELECT 1 FROM directory_users WHERE user_id = NEW.assigned_to AND status = 'ACTIVE') THEN
      RAISE EXCEPTION 'a task is assigned to an active person of the directory' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' OR NEW.completed_by IS NOT NULL OR NEW.completed_at IS NOT NULL OR NEW.cancelled_by IS NOT NULL OR NEW.cancelled_reason IS NOT NULL THEN
      RAISE EXCEPTION 'a task is created OPEN' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.due_on < current_date THEN
      RAISE EXCEPTION 'a task is due today or later' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status <> 'OPEN' THEN
    RAISE EXCEPTION 'a % task never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'OPEN' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['assigned_to', 'due_on'], 'task');
    IF NEW.due_on < current_date THEN
      RAISE EXCEPTION 'a task is due today or later' USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.status = 'DONE' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'completed_by', 'completed_at', 'completion_note'], 'task');
    IF NEW.completed_by IS DISTINCT FROM OLD.assigned_to THEN
      RAISE EXCEPTION 'a task is completed by the person it is assigned to' USING ERRCODE = 'check_violation';
    END IF;
    NEW.completed_at := now();
  ELSE
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'cancelled_by', 'cancelled_reason'], 'task');
    IF NEW.cancelled_by IS DISTINCT FROM OLD.created_by OR NEW.cancelled_reason IS NULL OR length(btrim(NEW.cancelled_reason)) < 5 THEN
      RAISE EXCEPTION 'a task is cancelled by the person who created it, with a reason' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER work_tasks_guard BEFORE INSERT OR UPDATE OR DELETE ON work_tasks FOR EACH ROW EXECUTE FUNCTION work_tasks_guard();

ALTER TABLE work_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_tasks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON work_tasks USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
DO $$
BEGIN
  EXECUTE format('CREATE POLICY owner_maintenance ON work_tasks TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', current_user);
END$$;
REVOKE ALL ON work_tasks FROM PUBLIC;
GRANT SELECT, INSERT ON work_tasks TO serk_app;
GRANT UPDATE (assigned_to, due_on, status, completed_by, completed_at, completion_note, cancelled_by, cancelled_reason) ON work_tasks TO serk_app;
