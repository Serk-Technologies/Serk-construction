-- 0056 SERK FLEET, part 3: maintenance (requirements FLT-03.x; ADR 0023)
--
-- WHAT THIS MIGRATION DOES
--   * WORK ORDERS: one per job on a machine (PREVENTIVE, CORRECTIVE, BREAKDOWN
--     or INSPECTION). Prepared by plant staff, or by site staff for a
--     breakdown; authorised by a person with fleet.approve; reported complete
--     by plant staff with what was done and the labour hours; CLOSED by a
--     certifier (fleet.release, fresh second factor) who is not the person who
--     reported it complete. A decided work order never changes.
--   * RETURN TO SERVICE: a machine that has been UNDER_MAINTENANCE since it
--     was last in service goes back to service only when a work order for it
--     was closed after it entered maintenance, and never while a work order is
--     still authorised or awaiting its certifier. This holds whichever way it
--     leaves maintenance (directly or through inspection).
--   * PARTS: a part used on a work order is an ordinary STOCK ISSUE (the
--     existing ledger values it and charges the project); maintenance_parts
--     records which work order it was for. One per issue, never changed.
--   * PLANS: preventive maintenance of a machine every so many metered units
--     and/or days. Closing a PREVENTIVE work order raised from a plan records
--     the meter and date it was done; nothing else writes those.
--   * maintenance_due(): plans with what is due, from the machine's meter and
--     today's date.
--
-- NOT DONE HERE: labour cost (no rates exist), workshop cost centres for
--   machines that are not at a project, parts reservations specific to a work
--   order (the existing stock reservation can be used), retiring a plan.
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0054, additions marked 0056):
--   p2p_subject_text, serk_subject_project

CREATE TABLE maintenance_plans (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id   uuid NOT NULL,
  title          text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 200),
  every_units    numeric(14,2) CHECK (every_units IS NULL OR every_units > 0),
  every_days     integer CHECK (every_days IS NULL OR every_days BETWEEN 1 AND 3650),
  last_done_meter numeric(14,2) NOT NULL DEFAULT 0 CHECK (last_done_meter >= 0),
  last_done_on   date NOT NULL DEFAULT current_date,
  created_by     text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, equipment_id, title),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id),
  CONSTRAINT plan_has_an_interval CHECK (every_units IS NOT NULL OR every_days IS NOT NULL)
);

CREATE TABLE maintenance_work_orders (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id    uuid NOT NULL,
  plan_id         uuid,
  number          text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('PREVENTIVE', 'CORRECTIVE', 'BREAKDOWN', 'INSPECTION')),
  description     text NOT NULL CHECK (length(btrim(description)) BETWEEN 10 AND 2000),
  opened_meter    numeric(14,2) NOT NULL DEFAULT 0,
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'COMPLETED', 'CLOSED', 'REJECTED', 'CANCELLED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  work_done       text CHECK (work_done IS NULL OR length(btrim(work_done)) BETWEEN 10 AND 4000),
  labour_hours    numeric(8,2) CHECK (labour_hours IS NULL OR labour_hours >= 0),
  completed_by    text,
  completed_at    timestamptz,
  closed_by       text,
  closed_at       timestamptz,
  closed_meter    numeric(14,2),
  closed_txid     bigint,
  returned_by     text,
  returned_at     timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES maintenance_plans(tenant_id, id),
  CONSTRAINT wo_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by),
  CONSTRAINT wo_certifier_is_independent CHECK (closed_by IS NULL OR closed_by <> completed_by),
  CONSTRAINT wo_plan_only_for_preventive CHECK (plan_id IS NULL OR kind = 'PREVENTIVE')
);
CREATE INDEX maintenance_work_orders_by_equipment ON maintenance_work_orders (tenant_id, equipment_id, created_at);
CREATE INDEX maintenance_work_orders_open ON maintenance_work_orders (tenant_id, status) WHERE status IN ('SUBMITTED', 'APPROVED', 'COMPLETED');
-- One open work order per plan: a due service is not raised twice.
CREATE UNIQUE INDEX maintenance_work_orders_one_per_plan ON maintenance_work_orders (tenant_id, plan_id) WHERE plan_id IS NOT NULL AND status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'COMPLETED');

CREATE TABLE maintenance_parts (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  work_order_id  uuid NOT NULL,
  stock_issue_id uuid NOT NULL,
  created_by     text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, stock_issue_id),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES maintenance_work_orders(tenant_id, id),
  FOREIGN KEY (tenant_id, stock_issue_id) REFERENCES stock_issues(tenant_id, id)
);
CREATE INDEX maintenance_parts_by_work_order ON maintenance_parts (tenant_id, work_order_id);

CREATE FUNCTION maintenance_plans_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e equipment;
BEGIN
  IF TG_OP = 'INSERT' THEN
    SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
    IF NOT FOUND THEN
      RAISE EXCEPTION 'unknown equipment' USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF NEW.every_units IS NOT NULL AND e.meter_kind = 'NONE' THEN
      RAISE EXCEPTION 'this equipment has no meter: plan its maintenance by days' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.maintenance.plan.create', 'equipment', NEW.equipment_id::text, NEW.created_by, ARRAY['fleet.manage'], false, NEW.title, NULL);
    -- The interval counts from now: the machine's present meter and today.
    NEW.last_done_meter := e.last_meter;
    NEW.last_done_on := current_date;
    NEW.created_at := now();
    RETURN NEW;
  END IF;
  -- Only the closing of a work order raised from the plan moves it on: the
  -- certifier's attested closing, in this same transaction, with this meter.
  IF NOT EXISTS (SELECT 1 FROM maintenance_work_orders w WHERE w.tenant_id = current_tenant() AND w.plan_id = NEW.id AND w.status = 'CLOSED'
                    AND w.closed_txid = txid_current() AND w.closed_meter = NEW.last_done_meter) THEN
    RAISE EXCEPTION 'a maintenance plan is marked done only by closing its work order' USING ERRCODE = 'insufficient_privilege';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['last_done_meter', 'last_done_on'], 'maintenance plan');
  RETURN NEW;
END$$;
CREATE TRIGGER maintenance_plans_guard BEFORE INSERT OR UPDATE ON maintenance_plans FOR EACH ROW EXECUTE FUNCTION maintenance_plans_guard();

CREATE FUNCTION maintenance_work_orders_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e equipment;
BEGIN
  SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF NOT FOUND THEN
      RAISE EXCEPTION 'unknown equipment' USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.completed_by IS NOT NULL OR NEW.closed_by IS NOT NULL THEN
      RAISE EXCEPTION 'a work order is prepared as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF e.status IN ('RETIRED', 'DISPOSED') THEN
      RAISE EXCEPTION 'no work is ordered on % equipment', lower(e.status) USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.plan_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM maintenance_plans p WHERE p.id = NEW.plan_id AND p.tenant_id = current_tenant() AND p.equipment_id = NEW.equipment_id) THEN
      RAISE EXCEPTION 'the plan is not a plan of this equipment' USING ERRCODE = 'check_violation';
    END IF;
    NEW.opened_meter := e.last_meter;
    NEW.created_at := now();
    RETURN NEW;
  END IF;
  IF OLD.status IN ('CLOSED', 'REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a decided work order never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'work order');
    PERFORM approval_use('fleet.maintenance.submit', 'maintenance_work_order', NEW.id::text, NEW.created_by, ARRAY['fleet.manage', 'fleet.operate'], false, NULL, NULL);
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'decision_reason'], 'work order');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'cancelling a work order requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.maintenance.cancel', 'maintenance_work_order', NEW.id::text, NEW.created_by, ARRAY['fleet.manage', 'fleet.operate'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'work order');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a work order requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.maintenance.decide', 'maintenance_work_order', NEW.id::text, NEW.approved_by, ARRAY['fleet.approve'], false, NEW.status, NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'APPROVED' AND NEW.status = 'COMPLETED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'work_done', 'labour_hours', 'completed_by', 'completed_at'], 'work order');
    IF NEW.work_done IS NULL OR NEW.labour_hours IS NULL OR NEW.completed_by IS NULL THEN
      RAISE EXCEPTION 'a completed work order states what was done, the labour hours and who reports it' USING ERRCODE = 'check_violation';
    END IF;
    -- Parts that were requested and never approved are not left hanging on a finished job.
    IF EXISTS (SELECT 1 FROM maintenance_parts mp JOIN stock_issues i ON i.id = mp.stock_issue_id WHERE mp.work_order_id = NEW.id AND mp.tenant_id = current_tenant() AND i.status = 'DRAFT') THEN
      RAISE EXCEPTION 'a part requested for this work order is still awaiting approval; approve, reject or cancel its stock issue first' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.maintenance.complete', 'maintenance_work_order', NEW.id::text, NEW.completed_by, ARRAY['fleet.manage'], false, NULL, NULL);
    NEW.completed_at := now();
  ELSIF OLD.status = 'COMPLETED' AND NEW.status IN ('CLOSED', 'APPROVED') THEN
    -- The certifier accepts the work (CLOSED) or sends it back (APPROVED again, with the reason).
    IF NEW.status = 'CLOSED' THEN
      PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at', 'closed_meter', 'closed_txid'], 'work order');
      PERFORM approval_use('fleet.maintenance.close', 'maintenance_work_order', NEW.id::text, NEW.closed_by, ARRAY['fleet.release'], true, 'CLOSED', NULL);
      NEW.closed_at := now();
      NEW.closed_meter := e.last_meter;
      NEW.closed_txid := txid_current();
    ELSE
      PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'decision_reason', 'work_done', 'labour_hours', 'completed_by', 'completed_at', 'returned_by', 'returned_at'], 'work order');
      IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 OR NEW.returned_by IS NULL OR NEW.returned_by = OLD.completed_by OR NEW.completed_by IS NOT NULL OR NEW.work_done IS NOT NULL OR NEW.labour_hours IS NOT NULL OR NEW.completed_at IS NOT NULL THEN
        RAISE EXCEPTION 'work is sent back by a certifier other than the person who reported it, with the reason; the completion report is cleared' USING ERRCODE = 'check_violation';
      END IF;
      PERFORM approval_use('fleet.maintenance.close', 'maintenance_work_order', NEW.id::text, NEW.returned_by, ARRAY['fleet.release'], true, 'SENT_BACK', NULL);
      NEW.returned_at := now();
    END IF;
  ELSE
    RAISE EXCEPTION 'illegal work order transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER maintenance_work_orders_guard BEFORE INSERT OR UPDATE ON maintenance_work_orders FOR EACH ROW EXECUTE FUNCTION maintenance_work_orders_guard();

-- Closing a preventive work order raised from a plan records when the plan was last done.
CREATE FUNCTION maintenance_work_orders_plan_done() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  UPDATE maintenance_plans SET last_done_meter = NEW.closed_meter, last_done_on = current_date WHERE id = NEW.plan_id AND tenant_id = current_tenant();
  RETURN NULL;
END$$;
CREATE TRIGGER maintenance_work_orders_plan_done AFTER UPDATE ON maintenance_work_orders FOR EACH ROW
  WHEN (NEW.status = 'CLOSED' AND OLD.status = 'COMPLETED' AND NEW.plan_id IS NOT NULL) EXECUTE FUNCTION maintenance_work_orders_plan_done();

CREATE FUNCTION maintenance_parts_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE i stock_issues; w maintenance_work_orders; e equipment;
BEGIN
  SELECT * INTO w FROM maintenance_work_orders WHERE id = NEW.work_order_id AND tenant_id = current_tenant() FOR UPDATE;
  IF NOT FOUND OR w.status <> 'APPROVED' THEN
    RAISE EXCEPTION 'parts are issued to an authorised work order that is not yet reported complete' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO i FROM stock_issues WHERE id = NEW.stock_issue_id AND tenant_id = current_tenant() FOR UPDATE;
  IF NOT FOUND OR i.status <> 'DRAFT' OR i.created_by <> NEW.created_by THEN
    RAISE EXCEPTION 'a part is requested with the preparer''s own draft stock issue' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO e FROM equipment WHERE id = w.equipment_id AND tenant_id = current_tenant();
  IF e.current_project_id IS NOT NULL AND i.project_id <> e.current_project_id THEN
    RAISE EXCEPTION 'parts for a machine at a project are charged to that project' USING ERRCODE = 'check_violation';
  END IF;
  IF EXISTS (SELECT 1 FROM fuel_dispenses f WHERE f.tenant_id = current_tenant() AND f.stock_issue_id = NEW.stock_issue_id) THEN
    RAISE EXCEPTION 'that stock issue is a fuel dispense' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('fleet.maintenance.part.create', 'stock_issue', NEW.stock_issue_id::text, NEW.created_by, ARRAY['stock.issue'], false, NEW.work_order_id::text, NULL);
  NEW.created_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER maintenance_parts_guard BEFORE INSERT ON maintenance_parts FOR EACH ROW EXECUTE FUNCTION maintenance_parts_guard();

-- --------------------------------------------------- return to service ---
-- A second trigger on equipment (the 0054 guard is unchanged). It fires for
-- the moves the 0054 state machine reserves to a certifier.
CREATE FUNCTION equipment_maintenance_gate() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE last_service bigint; entered timestamptz;
BEGIN
  IF EXISTS (SELECT 1 FROM maintenance_work_orders w WHERE w.tenant_id = current_tenant() AND w.equipment_id = NEW.id AND w.status IN ('APPROVED', 'COMPLETED')) THEN
    RAISE EXCEPTION 'this equipment has a work order that is not closed; it returns to service when the certifier has closed it' USING ERRCODE = 'check_violation';
  END IF;
  SELECT COALESCE(max(id), 0) INTO last_service FROM equipment_events
   WHERE tenant_id = current_tenant() AND equipment_id = NEW.id AND to_status IN ('AVAILABLE', 'RESERVED', 'MOBILIZING', 'ASSIGNED', 'OPERATING', 'IDLE', 'DEMOBILIZING');
  SELECT min(occurred_at) INTO entered FROM equipment_events
   WHERE tenant_id = current_tenant() AND equipment_id = NEW.id AND id > last_service AND to_status = 'UNDER_MAINTENANCE';
  IF entered IS NOT NULL AND NOT EXISTS (SELECT 1 FROM maintenance_work_orders w WHERE w.tenant_id = current_tenant() AND w.equipment_id = NEW.id
                                            AND w.status = 'CLOSED' AND w.closed_at >= entered) THEN
    RAISE EXCEPTION 'this equipment has been under maintenance: it returns to service only after a work order for it has been closed by a certifier' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_maintenance_gate BEFORE UPDATE OF status ON equipment FOR EACH ROW
  WHEN (OLD.status IN ('UNDER_INSPECTION', 'UNDER_MAINTENANCE', 'SAFETY_HOLD') AND NEW.status IN ('AVAILABLE', 'RESERVED', 'ASSIGNED'))
  EXECUTE FUNCTION equipment_maintenance_gate();

-- What is due, from each machine's meter and today's date. Invoker rights: row security applies.
CREATE FUNCTION maintenance_due(p_projects uuid[], p_tenant_wide boolean)
RETURNS TABLE (plan_id uuid, equipment_id uuid, title text, every_units numeric, every_days integer, last_done_meter numeric, last_done_on date,
               meter numeric, units_left numeric, days_left integer, open_work_order uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT p.id, p.equipment_id, p.title, p.every_units, p.every_days, p.last_done_meter, p.last_done_on, e.last_meter,
         CASE WHEN p.every_units IS NOT NULL THEN p.last_done_meter + p.every_units - e.last_meter END,
         CASE WHEN p.every_days IS NOT NULL THEN (p.last_done_on + p.every_days) - current_date END,
         (SELECT w.id FROM maintenance_work_orders w WHERE w.tenant_id = p.tenant_id AND w.plan_id = p.id AND w.status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'COMPLETED') LIMIT 1)
    FROM maintenance_plans p JOIN equipment e ON e.id = p.equipment_id AND e.tenant_id = p.tenant_id
   WHERE p.tenant_id = current_tenant() AND e.status NOT IN ('RETIRED', 'DISPOSED')
     AND (p_tenant_wide OR e.current_project_id = ANY(p_projects))
$$;

-- ---------------------------------------- shared functions (verbatim + 0056)
-- p2p_subject_text: verbatim from 0054 plus maintenance (marked 0056).
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
  -- 0052: a document revision (with its register entry; the file is covered by its sha256) and an RFI.
  WHEN 'document_revision' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'doc', (SELECT to_jsonb(d) FROM documents d WHERE d.id = x.document_id))
      INTO j FROM document_revisions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'rfi' THEN SELECT to_jsonb(x) INTO j FROM rfis x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0053: the tenant's document policy. Its id is the tenant id; a tenant with no row has the default policy.
  WHEN 'document_policy' THEN
    IF p_id::uuid <> t THEN RETURN NULL; END IF;
    SELECT COALESCE((SELECT to_jsonb(x) FROM document_policies x WHERE x.tenant_id = t), jsonb_build_object('scan_required', true, 'quota_bytes', NULL, 'default', true)) INTO j;
  -- 0054: fleet. What the person who moves, reserves, logs or resets attests.
  WHEN 'equipment' THEN SELECT to_jsonb(x) INTO j FROM equipment x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_reservation' THEN SELECT to_jsonb(x) INTO j FROM equipment_reservations x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_operator_auth' THEN SELECT to_jsonb(x) INTO j FROM equipment_operator_auths x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_log' THEN SELECT to_jsonb(x) INTO j FROM equipment_logs x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_meter_reset' THEN SELECT to_jsonb(x) INTO j FROM equipment_meter_resets x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0056: maintenance work orders.
  WHEN 'maintenance_work_order' THEN SELECT to_jsonb(x) INTO j FROM maintenance_work_orders x WHERE tenant_id = t AND id = p_id::uuid;
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0054 plus maintenance (marked 0056).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'document_policy', 'equipment_operator_auth') THEN
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
  -- 0052: a document revision belongs to its document's project; an RFI to its project.
  WHEN 'document_revision' THEN SELECT d.project_id::text INTO r FROM document_revisions x JOIN documents d ON d.id = x.document_id WHERE x.id = v_id;
  WHEN 'rfi' THEN SELECT project_id::text INTO r FROM rfis x WHERE x.id = v_id;
  -- 0054: equipment belongs to the project it is at (none in the yard: tenant-wide scope); reservations and logs to their project.
  WHEN 'equipment' THEN SELECT COALESCE(current_project_id::text, '') INTO r FROM equipment x WHERE x.id = v_id;
  WHEN 'equipment_meter_reset' THEN SELECT COALESCE(e.current_project_id::text, '') INTO r FROM equipment_meter_resets x JOIN equipment e ON e.id = x.equipment_id WHERE x.id = v_id;
  -- 0056: a work order belongs to the project its machine is at (none in the yard: tenant-wide scope).
  WHEN 'maintenance_work_order' THEN SELECT COALESCE(e.current_project_id::text, '') INTO r FROM maintenance_work_orders x JOIN equipment e ON e.id = x.equipment_id WHERE x.id = v_id;
  WHEN 'equipment_reservation' THEN SELECT project_id::text INTO r FROM equipment_reservations x WHERE x.id = v_id;
  WHEN 'equipment_log' THEN SELECT project_id::text INTO r FROM equipment_logs x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;


-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner_role text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['maintenance_plans', 'maintenance_work_orders', 'maintenance_parts'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner_role);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason, work_done, labour_hours, completed_by, completed_at, closed_by, closed_at, closed_meter, closed_txid, returned_by, returned_at) ON maintenance_work_orders TO serk_app;
-- The plan's "last done" is written by the closing trigger running as the caller; the plan guard allows nothing else.
GRANT UPDATE (last_done_meter, last_done_on) ON maintenance_plans TO serk_app;
GRANT EXECUTE ON FUNCTION maintenance_due(uuid[], boolean) TO serk_app;
