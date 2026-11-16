-- 0065_quality_site.sql — WP-3.6 Quality management, WP-3.8 Punch lists, site instructions, meetings
-- (SRS QA-01..05, SIT-06, SIT-07, COL-04).
--
-- QUALITY: an inspection and test plan (ITP) per project and activity, prepared by a quality engineer and
-- approved by a project manager, lists items of kind HOLD, WITNESS or REVIEW. A HOLD item stops progress on its
-- activity above a percentage until an inspection request for it has PASSED (enforced on schedule progress lines
-- by the database). Inspection requests are raised by the site and passed or failed by a different person.
-- Non-conformance reports (NCR): raised, corrective action recorded, closed (verified) by a different person.
-- Material tests compute pass or fail against their specification.
--
-- SITE: punch items (raised, fixed, closed by someone other than the fixer), site instructions (issued,
-- acknowledged, optionally linked to a change event) and meeting minutes with action items (issued minutes
-- never change).
-- p2p_subject_text / serk_subject_project: verbatim from 0064 plus ITPs.

CREATE TABLE itps (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id    uuid NOT NULL,
  number        text NOT NULL,
  title         text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 300),
  activity_code text,
  status        text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED')),
  created_by    text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  approved_by   text,
  approved_at   timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE TABLE itp_items (
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  itp_id          uuid NOT NULL,
  seq             integer NOT NULL CHECK (seq >= 1),
  description     text NOT NULL CHECK (length(btrim(description)) BETWEEN 3 AND 500),
  kind            text NOT NULL CHECK (kind IN ('HOLD', 'WITNESS', 'REVIEW')),
  hold_at_percent numeric(5,2) NOT NULL DEFAULT 0 CHECK (hold_at_percent >= 0 AND hold_at_percent < 100),
  PRIMARY KEY (tenant_id, itp_id, seq),
  FOREIGN KEY (tenant_id, itp_id) REFERENCES itps(tenant_id, id)
);
CREATE FUNCTION itp_items_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM itps WHERE id = NEW.itp_id AND tenant_id = current_tenant() AND status = 'DRAFT') THEN
    RAISE EXCEPTION 'items are added while the plan is a draft' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER itp_items_guard BEFORE INSERT ON itp_items FOR EACH ROW EXECUTE FUNCTION itp_items_guard();

CREATE FUNCTION itps_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN RAISE EXCEPTION 'plans are prepared as DRAFT' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED', 'CANCELLED') THEN RAISE EXCEPTION 'a % plan never changes', lower(OLD.status) USING ERRCODE = 'check_violation'; END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'plan');
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'plan');
    IF NOT EXISTS (SELECT 1 FROM itp_items WHERE itp_id = NEW.id) THEN RAISE EXCEPTION 'a plan has at least one item' USING ERRCODE = 'check_violation'; END IF;
    PERFORM approval_use('itp.submit', 'itp', NEW.id::text, NEW.created_by, ARRAY['quality.manage'], false, NULL, NULL);
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'plan');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN RAISE EXCEPTION 'rejecting a plan requires a reason' USING ERRCODE = 'check_violation'; END IF;
    PERFORM approval_use('itp.decide', 'itp', NEW.id::text, NEW.approved_by, ARRAY['quality.approve'], false, NEW.status, NULL);
    NEW.approved_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal plan transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER itps_guard BEFORE INSERT OR UPDATE ON itps FOR EACH ROW EXECUTE FUNCTION itps_guard();

CREATE TABLE inspection_requests (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id    uuid NOT NULL,
  number        text NOT NULL,
  itp_id        uuid NOT NULL,
  item_seq      integer NOT NULL,
  requested_for date NOT NULL,
  location      text,
  status        text NOT NULL DEFAULT 'REQUESTED' CHECK (status IN ('REQUESTED', 'PASSED', 'FAILED', 'CANCELLED')),
  requested_by  text NOT NULL,
  requested_at  timestamptz NOT NULL DEFAULT now(),
  result_by     text,
  result_at     timestamptz,
  result_note   text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, itp_id, item_seq) REFERENCES itp_items(tenant_id, itp_id, seq),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE FUNCTION inspection_requests_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'REQUESTED' OR NEW.result_by IS NOT NULL THEN RAISE EXCEPTION 'requests are raised as REQUESTED' USING ERRCODE = 'check_violation'; END IF;
    IF NOT EXISTS (SELECT 1 FROM itps WHERE id = NEW.itp_id AND project_id = NEW.project_id AND status = 'APPROVED') THEN
      RAISE EXCEPTION 'inspect against an approved plan of the project' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status <> 'REQUESTED' THEN RAISE EXCEPTION 'a decided inspection request never changes' USING ERRCODE = 'check_violation'; END IF;
  IF NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'inspection request');
    RETURN NEW;
  END IF;
  IF NEW.status NOT IN ('PASSED', 'FAILED') THEN RAISE EXCEPTION 'illegal inspection transition' USING ERRCODE = 'check_violation'; END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'result_by', 'result_at', 'result_note'], 'inspection request');
  IF NEW.result_by IS NULL OR NEW.result_by = OLD.requested_by THEN RAISE EXCEPTION 'the person who requested an inspection cannot decide it' USING ERRCODE = 'check_violation'; END IF;
  IF NEW.status = 'FAILED' AND (NEW.result_note IS NULL OR length(btrim(NEW.result_note)) < 5) THEN RAISE EXCEPTION 'a failed inspection needs a note' USING ERRCODE = 'check_violation'; END IF;
  NEW.result_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER inspection_requests_guard BEFORE INSERT OR UPDATE ON inspection_requests FOR EACH ROW EXECUTE FUNCTION inspection_requests_guard();

-- The hold-point guard: progress above an unreleased hold point is refused.
CREATE FUNCTION schedule_progress_hold_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE h record;
BEGIN
  IF NEW.percent_complete > 0 THEN
    SELECT t.number AS itp, i.seq, i.description INTO h
      FROM schedule_versions v JOIN itps t ON t.project_id = v.project_id AND t.status = 'APPROVED' AND t.activity_code = NEW.activity_code
      JOIN itp_items i ON i.itp_id = t.id AND i.kind = 'HOLD' AND i.hold_at_percent < NEW.percent_complete
     WHERE v.id = NEW.version_id
       AND NOT EXISTS (SELECT 1 FROM inspection_requests r WHERE r.itp_id = i.itp_id AND r.item_seq = i.seq AND r.status = 'PASSED')
     LIMIT 1;
    IF FOUND THEN
      RAISE EXCEPTION 'progress on % above % %% is held: hold point % item % (%) has not been passed', NEW.activity_code, NEW.percent_complete, h.itp, h.seq, h.description USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER schedule_progress_hold_guard BEFORE INSERT ON schedule_progress_lines FOR EACH ROW EXECUTE FUNCTION schedule_progress_hold_guard();

CREATE TABLE ncrs (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id       uuid NOT NULL,
  number           text NOT NULL,
  severity         text NOT NULL CHECK (severity IN ('MINOR', 'MAJOR', 'CRITICAL')),
  description      text NOT NULL CHECK (length(btrim(description)) BETWEEN 5 AND 4000),
  location         text,
  root_cause       text,
  corrective_action text,
  cost_impact      numeric(24,6) NOT NULL DEFAULT 0 CHECK (cost_impact >= 0),
  status           text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'ACTIONED', 'CLOSED')),
  raised_by        text NOT NULL,
  raised_at        timestamptz NOT NULL DEFAULT now(),
  actioned_by      text,
  closed_by        text,
  closed_at        timestamptz,
  close_note       text,
  source_test_id   uuid,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE FUNCTION ncrs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' OR NEW.closed_by IS NOT NULL THEN RAISE EXCEPTION 'reports are raised OPEN' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'CLOSED' THEN RAISE EXCEPTION 'a closed non-conformance never changes' USING ERRCODE = 'check_violation'; END IF;
  IF OLD.status = 'OPEN' AND NEW.status = 'ACTIONED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'root_cause', 'corrective_action', 'cost_impact', 'actioned_by'], 'non-conformance');
    IF COALESCE(length(btrim(NEW.root_cause)), 0) < 5 OR COALESCE(length(btrim(NEW.corrective_action)), 0) < 5 THEN
      RAISE EXCEPTION 'record the root cause and the corrective action' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ACTIONED' AND NEW.status = 'CLOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at', 'close_note'], 'non-conformance');
    IF NEW.closed_by IS NULL OR NEW.closed_by = OLD.raised_by OR NEW.closed_by = OLD.actioned_by THEN
      RAISE EXCEPTION 'a non-conformance is closed by a person other than the one who raised it or recorded the action' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.close_note IS NULL OR length(btrim(NEW.close_note)) < 5 THEN RAISE EXCEPTION 'closing needs a verification note' USING ERRCODE = 'check_violation'; END IF;
    NEW.closed_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal non-conformance transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER ncrs_guard BEFORE INSERT OR UPDATE ON ncrs FOR EACH ROW EXECUTE FUNCTION ncrs_guard();

CREATE TABLE material_tests (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id  uuid NOT NULL,
  batch_ref   text NOT NULL CHECK (length(btrim(batch_ref)) BETWEEN 2 AND 100),
  test_type   text NOT NULL CHECK (length(btrim(test_type)) BETWEEN 2 AND 100),
  value       numeric(18,4) NOT NULL,
  unit        text NOT NULL CHECK (length(btrim(unit)) BETWEEN 1 AND 20),
  spec_min    numeric(18,4),
  spec_max    numeric(18,4),
  passed      boolean NOT NULL,
  tested_on   date NOT NULL,
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT material_test_spec CHECK (spec_min IS NOT NULL OR spec_max IS NOT NULL),
  CONSTRAINT material_test_spec_order CHECK (spec_min IS NULL OR spec_max IS NULL OR spec_min <= spec_max)
);
CREATE FUNCTION material_tests_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  NEW.passed := (NEW.spec_min IS NULL OR NEW.value >= NEW.spec_min) AND (NEW.spec_max IS NULL OR NEW.value <= NEW.spec_max);
  RETURN NEW;
END$$;
CREATE TRIGGER material_tests_guard BEFORE INSERT ON material_tests FOR EACH ROW EXECUTE FUNCTION material_tests_guard();

-- ----------------------------------------------------------------- site ---
CREATE TABLE punch_items (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id  uuid NOT NULL,
  number      text NOT NULL,
  location    text NOT NULL CHECK (length(btrim(location)) BETWEEN 2 AND 200),
  description text NOT NULL CHECK (length(btrim(description)) BETWEEN 3 AND 2000),
  assignee    text,
  priority    text NOT NULL DEFAULT 'MEDIUM' CHECK (priority IN ('LOW', 'MEDIUM', 'HIGH')),
  due_on      date,
  status      text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'FIXED', 'CLOSED')),
  raised_by   text NOT NULL,
  raised_at   timestamptz NOT NULL DEFAULT now(),
  fixed_by    text,
  fixed_at    timestamptz,
  closed_by   text,
  closed_at   timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE FUNCTION punch_items_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' THEN RAISE EXCEPTION 'punch items are raised OPEN' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'OPEN' AND NEW.status = 'FIXED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'fixed_by', 'fixed_at'], 'punch item');
    NEW.fixed_at := now();
    RETURN NEW;
  END IF;
  IF OLD.status = 'FIXED' AND NEW.status = 'CLOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at'], 'punch item');
    IF NEW.closed_by IS NULL OR NEW.closed_by = OLD.fixed_by THEN RAISE EXCEPTION 'the person who fixed an item cannot close it' USING ERRCODE = 'check_violation'; END IF;
    NEW.closed_at := now();
    RETURN NEW;
  END IF;
  IF OLD.status = 'FIXED' AND NEW.status = 'OPEN' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'fixed_by', 'fixed_at'], 'punch item');
    IF NEW.fixed_by IS NOT NULL THEN RAISE EXCEPTION 'a reopened item has no fixer' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal punch item transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER punch_items_guard BEFORE INSERT OR UPDATE ON punch_items FOR EACH ROW EXECUTE FUNCTION punch_items_guard();

CREATE TABLE site_instructions (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id      uuid NOT NULL,
  number          text NOT NULL,
  title           text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 300),
  body            text NOT NULL CHECK (length(btrim(body)) BETWEEN 5 AND 8000),
  change_event_id uuid,
  status          text NOT NULL DEFAULT 'ISSUED' CHECK (status IN ('ISSUED', 'ACKNOWLEDGED', 'CLOSED')),
  issued_by       text NOT NULL,
  issued_at       timestamptz NOT NULL DEFAULT now(),
  acknowledged_by text,
  acknowledged_at timestamptz,
  closed_by       text,
  closed_at       timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE FUNCTION site_instructions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'ISSUED' THEN RAISE EXCEPTION 'instructions are issued ISSUED' USING ERRCODE = 'check_violation'; END IF;
    IF NEW.change_event_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM change_events WHERE id = NEW.change_event_id AND project_id = NEW.project_id) THEN
      RAISE EXCEPTION 'the change event belongs to another project' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ISSUED' AND NEW.status = 'ACKNOWLEDGED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'acknowledged_by', 'acknowledged_at'], 'site instruction');
    IF NEW.acknowledged_by IS NULL OR NEW.acknowledged_by = OLD.issued_by THEN RAISE EXCEPTION 'an instruction is acknowledged by someone other than its issuer' USING ERRCODE = 'check_violation'; END IF;
    NEW.acknowledged_at := now();
    RETURN NEW;
  END IF;
  IF OLD.status = 'ACKNOWLEDGED' AND NEW.status = 'CLOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at'], 'site instruction');
    NEW.closed_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal site instruction transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER site_instructions_guard BEFORE INSERT OR UPDATE ON site_instructions FOR EACH ROW EXECUTE FUNCTION site_instructions_guard();

CREATE TABLE meetings (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id uuid NOT NULL,
  number     text NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('PROGRESS', 'DESIGN', 'SAFETY', 'CLIENT', 'COORDINATION')),
  title      text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 300),
  held_on    date NOT NULL,
  attendees  text,
  minutes    text NOT NULL CHECK (length(btrim(minutes)) BETWEEN 5 AND 20000),
  status     text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'ISSUED')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  issued_at  timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE TABLE meeting_actions (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  meeting_id  uuid NOT NULL,
  description text NOT NULL CHECK (length(btrim(description)) BETWEEN 3 AND 1000),
  owner_name  text NOT NULL CHECK (length(btrim(owner_name)) BETWEEN 2 AND 120),
  due_on      date NOT NULL,
  status      text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'DONE')),
  done_on     date,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, meeting_id) REFERENCES meetings(tenant_id, id)
);
CREATE FUNCTION meetings_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' THEN RAISE EXCEPTION 'minutes are drafted as DRAFT' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ISSUED' THEN RAISE EXCEPTION 'issued minutes never change' USING ERRCODE = 'check_violation'; END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'issued_at'], 'meeting');
  NEW.issued_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER meetings_guard BEFORE INSERT OR UPDATE ON meetings FOR EACH ROW EXECUTE FUNCTION meetings_guard();
CREATE FUNCTION meeting_actions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' THEN RAISE EXCEPTION 'actions are raised OPEN' USING ERRCODE = 'check_violation'; END IF;
    IF NOT EXISTS (SELECT 1 FROM meetings WHERE id = NEW.meeting_id AND tenant_id = current_tenant() AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'actions are recorded with the draft minutes' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status <> 'OPEN' OR NEW.status <> 'DONE' THEN RAISE EXCEPTION 'an action is completed once' USING ERRCODE = 'check_violation'; END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'done_on'], 'meeting action');
  RETURN NEW;
END$$;
CREATE TRIGGER meeting_actions_guard BEFORE INSERT OR UPDATE ON meeting_actions FOR EACH ROW EXECUTE FUNCTION meeting_actions_guard();


-- ---------------------------------------- shared functions (verbatim + 0065)
-- p2p_subject_text: verbatim from the previous migration plus 0065.
CREATE OR REPLACE FUNCTION public.p2p_subject_text(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE t uuid := current_tenant(); j jsonb;
BEGIN
  IF p_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RETURN NULL;
  END IF;
  CASE p_type
  WHEN 'p2p_policy' THEN SELECT to_jsonb(x) INTO j FROM p2p_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'supplier' THEN SELECT to_jsonb(x) INTO j FROM suppliers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'supplier_bank_account' THEN SELECT to_jsonb(x) || jsonb_build_object('account_number', x.account_digest, 'account_number_enc', NULL) INTO j FROM supplier_bank_accounts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'company_bank_account' THEN SELECT to_jsonb(x) || jsonb_build_object('account_number', x.account_digest, 'account_number_enc', NULL) INTO j FROM company_bank_accounts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'boq_version' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'items', (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.item_no COLLATE "C") FROM boq_items i WHERE i.version_id = x.id))
      INTO j FROM boq_versions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'purchase_requisition' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM pr_lines l WHERE l.pr_id = x.id))
      INTO j FROM purchase_requisitions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'rfq' THEN
    SELECT jsonb_build_object('h', to_jsonb(x),
             'suppliers', (SELECT jsonb_agg(s.supplier_id ORDER BY s.supplier_id) FROM rfq_suppliers s WHERE s.rfq_id = x.id),
             'quotes', (SELECT jsonb_agg(jsonb_build_object('q', to_jsonb(q), 'lines',
                          (SELECT jsonb_agg(to_jsonb(ql) ORDER BY ql.pr_line_no) FROM rfq_quote_lines ql WHERE ql.quote_id = q.id)) ORDER BY q.id)
                          FROM rfq_quotes q WHERE q.rfq_id = x.id))
      INTO j FROM rfqs x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'rfq_quote' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.pr_line_no) FROM rfq_quote_lines l WHERE l.quote_id = x.id))
      INTO j FROM rfq_quotes x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'purchase_order' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM po_lines l WHERE l.po_id = x.id))
      INTO j FROM purchase_orders x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'grn' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.po_line_no) FROM grn_lines l WHERE l.grn_id = x.id))
      INTO j FROM grns x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'supplier_invoice' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.po_line_no) FROM invoice_lines l WHERE l.invoice_id = x.id))
      INTO j FROM supplier_invoices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'payment_instruction' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'release_journal_id',
             'alloc', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.invoice_id) FROM payment_allocations a WHERE a.payment_id = x.id),
             'payee', (SELECT to_jsonb(b) || jsonb_build_object('account_number', b.account_digest, 'account_number_enc', NULL) FROM supplier_bank_accounts b WHERE b.id = x.supplier_bank_id))
      INTO j FROM payment_instructions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'bank_statement_line' THEN
    SELECT jsonb_build_object('l', to_jsonb(x) - 'journal_id', 's', (SELECT to_jsonb(s) FROM bank_statements s WHERE s.id = x.statement_id))
      INTO j FROM bank_statement_lines x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer' THEN SELECT to_jsonb(x) INTO j FROM customers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract_policy' THEN SELECT to_jsonb(x) INTO j FROM contract_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM contract_lines l WHERE l.contract_id = x.id))
      INTO j FROM contracts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'measurement' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.contract_line_no) FROM measurement_lines l WHERE l.measurement_id = x.id))
      INTO j FROM measurements x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'ipc' THEN SELECT to_jsonb(x) INTO j FROM ipcs x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer_invoice' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM customer_invoices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer_receipt' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id',
             'alloc', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.line_no) FROM receipt_allocations a WHERE a.receipt_id = x.id),
             'line', (SELECT to_jsonb(l) - 'journal_id' FROM bank_statement_lines l WHERE l.id = x.statement_line_id))
      INTO j FROM customer_receipts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_item' THEN SELECT to_jsonb(x) INTO j FROM stock_items x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'warehouse' THEN SELECT to_jsonb(x) INTO j FROM warehouses x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_receipt' THEN SELECT to_jsonb(x) INTO j FROM stock_receipts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_issue' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM stock_issues x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_count' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM stock_counts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer_credit_note' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM customer_credit_notes x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'retention_release' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM retention_releases x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_transfer' THEN SELECT to_jsonb(x) INTO j FROM stock_transfers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'schedule_version' THEN
    SELECT jsonb_build_object('h', to_jsonb(x),
             'activities', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.code COLLATE "C") FROM schedule_activities a WHERE a.version_id = x.id),
             'links', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.pred_code COLLATE "C", l.succ_code COLLATE "C") FROM schedule_links l WHERE l.version_id = x.id))
      INTO j FROM schedule_versions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'schedule_progress' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.activity_code COLLATE "C") FROM schedule_progress_lines l WHERE l.progress_id = x.id))
      INTO j FROM schedule_progress x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract_variation' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM variation_lines l WHERE l.variation_id = x.id))
      INTO j FROM contract_variations x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'site_diary' THEN
    SELECT jsonb_build_object('h', to_jsonb(x),
             'labour', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM site_diary_labour l WHERE l.diary_id = x.id AND l.version = x.version),
             'equipment', (SELECT jsonb_agg(to_jsonb(e) ORDER BY e.line_no) FROM site_diary_equipment e WHERE e.diary_id = x.id AND e.version = x.version))
      INTO j FROM site_diaries x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'site_device' THEN SELECT to_jsonb(x) - 'last_seq' - 'last_sync_at' INTO j FROM site_devices x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0039: budget documents (header and lines are what the decider attests).
  WHEN 'budget_version' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.cost_code_id) FROM budget_lines l WHERE l.version_id = x.id))
      INTO j FROM budget_versions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'budget_transfer' THEN SELECT to_jsonb(x) INTO j FROM budget_transfers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contingency_drawdown' THEN SELECT to_jsonb(x) INTO j FROM contingency_drawdowns x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0040: governance documents (the decider attests the header and, for a matrix, its rules).
  WHEN 'authority_matrix' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'rules', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.doc_type, r.currency, r.min_amount) FROM authority_rules r WHERE r.matrix_id = x.id))
      INTO j FROM authority_matrices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'delegation' THEN SELECT to_jsonb(x) INTO j FROM delegations x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'sod_exception' THEN SELECT to_jsonb(x) INTO j FROM sod_exceptions x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0041: unit conversions (the decider attests the factor and the units).
  WHEN 'uom_conversion' THEN SELECT to_jsonb(x) INTO j FROM uom_conversions x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0042: projects and their stage gates (what the creator and the gate's decider attest).
  WHEN 'project' THEN SELECT to_jsonb(x) INTO j FROM projects x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'project_stage_gate' THEN SELECT to_jsonb(x) INTO j FROM project_stage_gates x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0044: forecast cycles (header and lines are what the decider attests).
  WHEN 'forecast_cycle' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.cost_code_id) FROM forecast_lines l WHERE l.cycle_id = x.id))
      INTO j FROM forecast_cycles x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0045: change events (header and lines are what the decider attests).
  WHEN 'change_event' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) || jsonb_build_object('cost_code', c.code) ORDER BY c.code)
        FROM change_event_lines l JOIN cost_codes c ON c.id = l.cost_code_id WHERE l.event_id = x.id))
      INTO j FROM change_events x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0046: notices and claims.
  WHEN 'contract_notice' THEN SELECT to_jsonb(x) INTO j FROM contract_notices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract_claim' THEN SELECT to_jsonb(x) INTO j FROM contract_claims x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0050: guarantees, index values, escalation formulas (with their indices) and price adjustments.
  WHEN 'guarantee' THEN SELECT to_jsonb(x) INTO j FROM guarantees x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'price_index_value' THEN SELECT to_jsonb(x) INTO j FROM price_index_values x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'escalation_formula' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.index_code COLLATE "C") FROM escalation_terms l WHERE l.formula_id = x.id))
      INTO j FROM escalation_formulas x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'price_adjustment' THEN SELECT to_jsonb(x) INTO j FROM price_adjustments x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0051: liquidated damages terms.
  WHEN 'ld_terms' THEN SELECT to_jsonb(x) INTO j FROM contract_ld_terms x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0053: revenue policies.
  WHEN 'revenue_policy' THEN SELECT to_jsonb(x) INTO j FROM revenue_policies x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0055: subcontract documents.
  WHEN 'subcontract_policy' THEN SELECT to_jsonb(x) INTO j FROM subcontract_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'subcontract' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM subcontract_lines l WHERE l.subcontract_id = x.id))
      INTO j FROM subcontracts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'subcontract_application' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM subcontract_application_lines l WHERE l.application_id = x.id))
      INTO j FROM subcontract_applications x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'subcontract_backcharge' THEN SELECT to_jsonb(x) INTO j FROM subcontract_backcharges x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0056: subcontract payments with their allocations.
  WHEN 'subcontract_payment' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'release_journal_id', 'alloc', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.application_id) FROM subcontract_payment_allocations a WHERE a.payment_id = x.id),
             'payee', (SELECT to_jsonb(b) || jsonb_build_object('account_number', b.account_digest, 'account_number_enc', NULL) FROM supplier_bank_accounts b WHERE b.id = x.supplier_bank_id))
      INTO j FROM subcontract_payments x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0057: fixed assets and disposals.
  WHEN 'fixed_asset' THEN SELECT to_jsonb(x) - 'cap_journal_id' INTO j FROM fixed_assets x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'asset_disposal' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM asset_disposals x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0061: payroll documents (a pay run with its computed lines).
  WHEN 'payroll_policy' THEN SELECT to_jsonb(x) INTO j FROM payroll_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'payroll_rule_set' THEN SELECT to_jsonb(x) INTO j FROM payroll_rule_sets x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'pay_run' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'pay_journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.employee_id) FROM pay_lines l WHERE l.run_id = x.id))
      INTO j FROM pay_runs x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0062: consolidation groups (with members) and runs (with their computed lines).
  WHEN 'consolidation_group' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'members', (SELECT jsonb_agg(to_jsonb(m) ORDER BY m.legal_entity_id) FROM consolidation_members m WHERE m.group_id = x.id))
      INTO j FROM consolidation_groups x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'consolidation_run' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', consolidation_lines_text(x.id)) INTO j FROM consolidation_runs x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0063: tax returns with their computed lines.
  WHEN 'tax_return' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', tax_return_lines_text(x.id, true)) INTO j FROM tax_returns x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0064: timesheets (with lines) and equipment usage.
  WHEN 'timesheet' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.employee_id) FROM timesheet_lines l WHERE l.timesheet_id = x.id))
      INTO j FROM timesheets x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_usage' THEN SELECT to_jsonb(x) - 'hire_run_id' INTO j FROM equipment_usage x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0065: inspection and test plans with their items.
  WHEN 'itp' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'items', (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.seq) FROM itp_items i WHERE i.itp_id = x.id))
      INTO j FROM itps x WHERE tenant_id = t AND id = p_id::uuid;
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from the previous migration plus 0065.
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal', 'payroll_policy', 'payroll_rule_set', 'pay_run', 'consolidation_group', 'consolidation_run', 'tax_return') THEN
    RETURN NULL;
  END IF;
  IF p_id IS NULL OR p_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RETURN '';
  END IF;
  v_id := p_id::uuid;
  CASE p_type
  WHEN 'boq_version' THEN SELECT project_id::text INTO r FROM boq_versions WHERE boq_versions.id = v_id;
  WHEN 'purchase_requisition' THEN SELECT project_id::text INTO r FROM purchase_requisitions x WHERE x.id = v_id;
  WHEN 'purchase_order' THEN SELECT project_id::text INTO r FROM purchase_orders x WHERE x.id = v_id;
  WHEN 'rfq' THEN SELECT pr.project_id::text INTO r FROM rfqs q JOIN purchase_requisitions pr ON pr.id = q.pr_id WHERE q.id = v_id;
  WHEN 'rfq_quote' THEN SELECT pr.project_id::text INTO r FROM rfq_quotes x JOIN rfqs q ON q.id = x.rfq_id JOIN purchase_requisitions pr ON pr.id = q.pr_id WHERE x.id = v_id;
  WHEN 'grn' THEN SELECT o.project_id::text INTO r FROM grns g JOIN purchase_orders o ON o.id = g.po_id WHERE g.id = v_id;
  WHEN 'supplier_invoice' THEN SELECT o.project_id::text INTO r FROM supplier_invoices x JOIN purchase_orders o ON o.id = x.po_id WHERE x.id = v_id;
  WHEN 'contract' THEN SELECT project_id::text INTO r FROM contracts x WHERE x.id = v_id;
  WHEN 'measurement' THEN SELECT c.project_id::text INTO r FROM measurements m JOIN contracts c ON c.id = m.contract_id WHERE m.id = v_id;
  WHEN 'ipc' THEN SELECT c.project_id::text INTO r FROM ipcs x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'customer_invoice' THEN SELECT project_id::text INTO r FROM customer_invoices x WHERE x.id = v_id;
  WHEN 'customer_credit_note' THEN SELECT project_id::text INTO r FROM customer_credit_notes x WHERE x.id = v_id;
  WHEN 'retention_release' THEN SELECT project_id::text INTO r FROM retention_releases x WHERE x.id = v_id;
  WHEN 'contract_variation' THEN SELECT c.project_id::text INTO r FROM contract_variations v JOIN contracts c ON c.id = v.contract_id WHERE v.id = v_id;
  WHEN 'schedule_version' THEN SELECT project_id::text INTO r FROM schedule_versions x WHERE x.id = v_id;
  WHEN 'schedule_progress' THEN SELECT project_id::text INTO r FROM schedule_progress x WHERE x.id = v_id;
  WHEN 'warehouse' THEN SELECT COALESCE(project_id::text, '') INTO r FROM warehouses x WHERE x.id = v_id;
  WHEN 'stock_receipt' THEN SELECT COALESCE(w.project_id::text, '') INTO r FROM stock_receipts x JOIN warehouses w ON w.id = x.warehouse_id WHERE x.id = v_id;
  WHEN 'stock_issue' THEN SELECT project_id::text INTO r FROM stock_issues x WHERE x.id = v_id;
  WHEN 'stock_count' THEN SELECT COALESCE(project_id::text, '') INTO r FROM stock_counts x WHERE x.id = v_id;
  WHEN 'stock_transfer' THEN SELECT COALESCE(w.project_id::text, '') INTO r FROM stock_transfers x JOIN warehouses w ON w.id = x.from_warehouse_id WHERE x.id = v_id;
  WHEN 'journal' THEN SELECT project_id::text INTO r FROM journals x WHERE x.id = v_id;
  WHEN 'wbs_version' THEN SELECT project_id::text INTO r FROM wbs_versions x WHERE x.id = v_id;
  WHEN 'workflow_instance' THEN SELECT NULLIF(facts->>'project_id', '') INTO r FROM workflow_instances x WHERE x.id = v_id;
  WHEN 'site_diary' THEN SELECT project_id::text INTO r FROM site_diaries x WHERE x.id = v_id;
  -- 0039: budget documents belong to their project.
  WHEN 'budget_version' THEN SELECT project_id::text INTO r FROM budget_versions x WHERE x.id = v_id;
  WHEN 'budget_transfer' THEN SELECT project_id::text INTO r FROM budget_transfers x WHERE x.id = v_id;
  WHEN 'contingency_drawdown' THEN SELECT project_id::text INTO r FROM contingency_drawdowns x WHERE x.id = v_id;
  -- 0042: a stage gate belongs to its project.
  WHEN 'project_stage_gate' THEN SELECT project_id::text INTO r FROM project_stage_gates x WHERE x.id = v_id;
  -- 0044: a forecast cycle belongs to its project.
  WHEN 'forecast_cycle' THEN SELECT project_id::text INTO r FROM forecast_cycles x WHERE x.id = v_id;
  -- 0045: a change event belongs to its project.
  WHEN 'change_event' THEN SELECT project_id::text INTO r FROM change_events x WHERE x.id = v_id;
  -- 0046: a notice or claim belongs to its contract's project.
  WHEN 'contract_notice' THEN SELECT c.project_id::text INTO r FROM contract_notices x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'contract_claim' THEN SELECT c.project_id::text INTO r FROM contract_claims x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  -- 0050: guarantees, formulas and adjustments belong to their contract's project.
  WHEN 'guarantee' THEN SELECT c.project_id::text INTO r FROM guarantees x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'escalation_formula' THEN SELECT c.project_id::text INTO r FROM escalation_formulas x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'price_adjustment' THEN SELECT c.project_id::text INTO r FROM price_adjustments x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  -- 0051: damages terms belong to their contract's project.
  WHEN 'ld_terms' THEN SELECT c.project_id::text INTO r FROM contract_ld_terms x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  -- 0055: subcontract documents belong to the subcontract's project.
  WHEN 'subcontract' THEN SELECT project_id::text INTO r FROM subcontracts x WHERE x.id = v_id;
  WHEN 'subcontract_application' THEN SELECT s.project_id::text INTO r FROM subcontract_applications x JOIN subcontracts s ON s.id = x.subcontract_id WHERE x.id = v_id;
  WHEN 'subcontract_backcharge' THEN SELECT s.project_id::text INTO r FROM subcontract_backcharges x JOIN subcontracts s ON s.id = x.subcontract_id WHERE x.id = v_id;
  WHEN 'subcontract_payment' THEN SELECT s.project_id::text INTO r FROM subcontract_payments x JOIN subcontracts s ON s.id = x.subcontract_id WHERE x.id = v_id;
  -- 0064: timesheets and equipment usage belong to their project.
  WHEN 'timesheet' THEN SELECT project_id::text INTO r FROM timesheets x WHERE x.id = v_id;
  WHEN 'equipment_usage' THEN SELECT project_id::text INTO r FROM equipment_usage x WHERE x.id = v_id;
  -- 0065: a plan belongs to its project.
  WHEN 'itp' THEN SELECT project_id::text INTO r FROM itps x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['itps', 'itp_items', 'inspection_requests', 'ncrs', 'material_tests', 'punch_items', 'site_instructions', 'meetings', 'meeting_actions'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON itps TO serk_app;
GRANT UPDATE (status, result_by, result_at, result_note) ON inspection_requests TO serk_app;
GRANT UPDATE (status, root_cause, corrective_action, cost_impact, actioned_by, closed_by, closed_at, close_note) ON ncrs TO serk_app;
GRANT UPDATE (status, fixed_by, fixed_at, closed_by, closed_at) ON punch_items TO serk_app;
GRANT UPDATE (status, acknowledged_by, acknowledged_at, closed_by, closed_at) ON site_instructions TO serk_app;
GRANT UPDATE (status, issued_at) ON meetings TO serk_app;
GRANT UPDATE (status, done_on) ON meeting_actions TO serk_app;
