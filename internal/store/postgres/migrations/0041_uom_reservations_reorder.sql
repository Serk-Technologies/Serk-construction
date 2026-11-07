-- 0041 Units of measure, stock reservations and reorder points (WP-1.6; SRS INV-04, INV-05, INV-07)
--
-- WHAT THIS MIGRATION DOES
--   Stock is always kept in the item's base unit (stock_items.unit). Real
--   sites buy and issue in other units (a bag, a truck, a bundle), reserve
--   material for work that has not started, and run out of things. This adds:
--
--     uom_conversions     "1 bag of cement = 0.05 tonne", per item or for all
--                         items. A controlled document: prepared by one
--                         person, approved by another (attested), exact
--                         decimal factor, one approved factor per pair.
--     stock_reservations  material set aside for a project. Reserved stock
--                         cannot be reserved twice and cannot be issued to
--                         anyone else: the database refuses an issue that
--                         would eat into another project's reservation.
--     stock_reorder_rules a minimum, maximum and reorder quantity per item
--                         and store, so low stock is reported with a
--                         suggested quantity (reports only; nothing orders
--                         automatically).
--
--   stock_issues gains reservation_id: an issue made against a reservation
--   consumes it when the issue is approved.
--
-- GUARDS THE DATABASE ENFORCES
--   * a reservation never exceeds what is on hand and not already reserved
--     (the stock balance row is locked, so two people cannot both reserve the
--     last bag);
--   * an approved issue never leaves on-hand stock below other people's active
--     reservations; an issue linked to a reservation consumes it, in the same
--     transaction, and cannot exceed it;
--   * a reservation is released (never edited), and only ACTIVE ones;
--   * conversion factors are positive; item-specific factors convert into the
--     item's own base unit; approval is maker/checker with an attestation.
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0040, additions marked 0041):
--   p2p_subject_text, serk_subject_project

-- ------------------------------------------------------------ conversions
CREATE TABLE uom_conversions (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  stock_item_id   uuid,                                    -- NULL = applies to every item whose base unit is to_uom
  from_uom        text NOT NULL CHECK (length(btrim(from_uom)) BETWEEN 1 AND 20),
  to_uom          text NOT NULL CHECK (length(btrim(to_uom)) BETWEEN 1 AND 20),
  factor          numeric(24,9) NOT NULL CHECK (factor > 0),   -- 1 from_uom = factor to_uom
  note            text,
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_items(tenant_id, id),
  CONSTRAINT uom_distinct CHECK (lower(from_uom) <> lower(to_uom)),
  CONSTRAINT uom_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX uom_one_approved ON uom_conversions (tenant_id, COALESCE(stock_item_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(from_uom), lower(to_uom))
  WHERE status = 'APPROVED';

-- 1 from_uom = factor to the item's base unit, or NULL when no approved conversion exists.
CREATE FUNCTION uom_factor(p_item uuid, p_from text) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT c.factor FROM uom_conversions c JOIN stock_items i ON i.id = p_item AND i.tenant_id = current_tenant()
   WHERE c.tenant_id = current_tenant() AND c.status = 'APPROVED' AND lower(c.from_uom) = lower(p_from) AND lower(c.to_uom) = lower(i.unit)
     AND (c.stock_item_id = p_item OR c.stock_item_id IS NULL)
   ORDER BY (c.stock_item_id IS NOT NULL) DESC LIMIT 1
$$;

CREATE FUNCTION uom_conversions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE it stock_items;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'unit conversions are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.stock_item_id IS NOT NULL THEN
      SELECT * INTO it FROM stock_items WHERE id = NEW.stock_item_id AND tenant_id = current_tenant();
      IF lower(it.unit) <> lower(NEW.to_uom) THEN
        RAISE EXCEPTION 'an item-specific conversion converts into the item''s base unit (%)', it.unit USING ERRCODE = 'check_violation';
      END IF;
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided unit conversion is immutable; propose a new one' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', 'unit conversion');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'unit conversion');
    PERFORM approval_use('uom.conversion.submit', 'uom_conversion', NEW.id::text, NEW.created_by, ARRAY['stock.item.manage'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'unit conversion');
    PERFORM approval_use('uom.conversion.decide', 'uom_conversion', NEW.id::text, NEW.approved_by, ARRAY['stock.item.approve'], false, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'unit conversion');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a unit conversion requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('uom.conversion.decide', 'uom_conversion', NEW.id::text, NEW.approved_by, ARRAY['stock.item.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal unit conversion transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER uom_conversions_guard BEFORE INSERT OR UPDATE ON uom_conversions FOR EACH ROW EXECUTE FUNCTION uom_conversions_guard();

-- ------------------------------------------------------------ reservations
CREATE TABLE stock_reservations (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  stock_item_id   uuid NOT NULL,
  warehouse_id    uuid NOT NULL,
  project_id      uuid NOT NULL,
  quantity        numeric(24,6) NOT NULL CHECK (quantity > 0),
  purpose         text NOT NULL CHECK (length(btrim(purpose)) BETWEEN 3 AND 300),
  expires_at      timestamptz NOT NULL,
  status          text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'RELEASED', 'CONSUMED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  released_by     text,
  released_at     timestamptz,
  release_reason  text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_items(tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses(tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE INDEX stock_reservations_stock ON stock_reservations (tenant_id, stock_item_id, warehouse_id) WHERE status = 'ACTIVE';

ALTER TABLE stock_issues ADD COLUMN reservation_id uuid;
ALTER TABLE stock_issues ADD CONSTRAINT stock_issues_reservation_fk FOREIGN KEY (tenant_id, reservation_id) REFERENCES stock_reservations(tenant_id, id);

-- Quantity of an item reserved (active and not expired) in a store, optionally excluding one reservation.
CREATE FUNCTION stock_reserved(p_item uuid, p_warehouse uuid, p_except uuid DEFAULT NULL) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(sum(r.quantity), 0) FROM stock_reservations r
   WHERE r.tenant_id = current_tenant() AND r.stock_item_id = p_item AND r.warehouse_id = p_warehouse
     AND r.status = 'ACTIVE' AND r.expires_at > now() AND r.id IS DISTINCT FROM p_except
$$;

CREATE FUNCTION stock_reservations_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE it stock_items; w warehouses; onhand numeric;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'ACTIVE' OR NEW.released_at IS NOT NULL THEN
      RAISE EXCEPTION 'reservations are created ACTIVE' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.expires_at <= now() OR NEW.expires_at > now() + interval '180 days' THEN
      RAISE EXCEPTION 'a reservation must expire in the future, within 180 days' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO it FROM stock_items WHERE id = NEW.stock_item_id AND tenant_id = current_tenant();
    SELECT * INTO w FROM warehouses WHERE id = NEW.warehouse_id AND tenant_id = current_tenant();
    IF it.status IS DISTINCT FROM 'ACTIVE' OR it.legal_entity_id <> w.legal_entity_id THEN
      RAISE EXCEPTION 'stock is reserved for an ACTIVE item of the store''s entity' USING ERRCODE = 'check_violation';
    END IF;
    IF w.project_id IS NOT NULL AND w.project_id <> NEW.project_id THEN
      RAISE EXCEPTION 'a project store''s stock is reserved only for its own project' USING ERRCODE = 'check_violation';
    END IF;
    -- Serialise with other reservations and with issue approvals of this item
    -- (both take this row lock before reading the balance and the reservations).
    PERFORM 1 FROM stock_items WHERE id = NEW.stock_item_id AND tenant_id = current_tenant() FOR UPDATE;
    SELECT qty INTO onhand FROM stock_balances WHERE stock_item_id = NEW.stock_item_id AND warehouse_id = NEW.warehouse_id AND tenant_id = current_tenant();
    IF COALESCE(onhand, 0) - stock_reserved(NEW.stock_item_id, NEW.warehouse_id) < NEW.quantity THEN
      RAISE EXCEPTION 'cannot reserve %: only % of % is on hand and unreserved in this store', NEW.quantity,
        GREATEST(COALESCE(onhand, 0) - stock_reserved(NEW.stock_item_id, NEW.warehouse_id), 0), it.code USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'a % reservation is final', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'RELEASED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'released_by', 'released_at', 'release_reason'], 'stock reservation');
    IF NEW.released_by IS NULL OR NEW.release_reason IS NULL OR length(btrim(NEW.release_reason)) < 3 THEN
      RAISE EXCEPTION 'releasing a reservation records who and why' USING ERRCODE = 'check_violation';
    END IF;
    NEW.released_at := now();
  ELSIF NEW.status = 'CONSUMED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'released_at'], 'stock reservation');
    -- Only by an approved issue made against it, in this transaction.
    IF NOT EXISTS (SELECT 1 FROM stock_issues i WHERE i.reservation_id = NEW.id AND i.tenant_id = current_tenant() AND i.status = 'APPROVED' AND i.approved_txid = txid_current()) THEN
      RAISE EXCEPTION 'a reservation is consumed only by an approved stock issue made against it' USING ERRCODE = 'insufficient_privilege';
    END IF;
    NEW.released_at := now();
  ELSE
    RAISE EXCEPTION 'illegal reservation transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER stock_reservations_guard BEFORE INSERT OR UPDATE ON stock_reservations FOR EACH ROW EXECUTE FUNCTION stock_reservations_guard();

-- An approved issue never eats into other people's reservations; one made
-- against a reservation is for the same item, store and project and cannot
-- exceed it. Runs after stock_issues_guard (name order), when the balance
-- already reflects this issue.
CREATE FUNCTION stock_issues_zz_reservation() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r stock_reservations; onhand numeric;
BEGIN
  IF NEW.reservation_id IS NOT NULL THEN
    SELECT * INTO r FROM stock_reservations WHERE id = NEW.reservation_id AND tenant_id = current_tenant() FOR UPDATE;
    IF r.status IS DISTINCT FROM 'ACTIVE' OR r.expires_at <= now() THEN
      RAISE EXCEPTION 'the reservation is not active (released, used or expired)' USING ERRCODE = 'check_violation';
    END IF;
    IF r.stock_item_id <> NEW.stock_item_id OR r.warehouse_id <> NEW.warehouse_id OR r.project_id <> NEW.project_id THEN
      RAISE EXCEPTION 'the issue must match its reservation (item, store and project)' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.quantity > r.quantity THEN
      RAISE EXCEPTION 'the issue (%) exceeds its reservation (%)', NEW.quantity, r.quantity USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  PERFORM 1 FROM stock_items WHERE id = NEW.stock_item_id AND tenant_id = current_tenant() FOR UPDATE;  -- see stock_reservations_guard
  SELECT qty INTO onhand FROM stock_balances WHERE stock_item_id = NEW.stock_item_id AND warehouse_id = NEW.warehouse_id AND tenant_id = current_tenant();
  IF COALESCE(onhand, 0) < stock_reserved(NEW.stock_item_id, NEW.warehouse_id, NEW.reservation_id) THEN
    RAISE EXCEPTION 'this issue would use stock that is reserved for others (on hand after issue %, reserved by others %)',
      COALESCE(onhand, 0), stock_reserved(NEW.stock_item_id, NEW.warehouse_id, NEW.reservation_id) USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER stock_issues_zz_reservation BEFORE UPDATE ON stock_issues FOR EACH ROW
  WHEN (OLD.status = 'DRAFT' AND NEW.status = 'APPROVED') EXECUTE FUNCTION stock_issues_zz_reservation();

-- Consume the reservation once the issue row is approved (after the row change,
-- so the reservation guard can see the approved issue).
CREATE FUNCTION stock_issues_consume_reservation() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  UPDATE stock_reservations SET status = 'CONSUMED' WHERE id = NEW.reservation_id AND tenant_id = current_tenant() AND status = 'ACTIVE';
  RETURN NULL;
END$$;
CREATE TRIGGER stock_issues_consume_reservation AFTER UPDATE ON stock_issues FOR EACH ROW
  WHEN (OLD.status = 'DRAFT' AND NEW.status = 'APPROVED' AND NEW.reservation_id IS NOT NULL) EXECUTE FUNCTION stock_issues_consume_reservation();

-- ------------------------------------------------------------ reorder rules
CREATE TABLE stock_reorder_rules (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  stock_item_id  uuid NOT NULL,
  warehouse_id   uuid NOT NULL,
  min_qty        numeric(24,6) NOT NULL CHECK (min_qty >= 0),
  max_qty        numeric(24,6) NOT NULL,
  reorder_qty    numeric(24,6) NOT NULL CHECK (reorder_qty > 0),
  created_by     text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     text,
  updated_at     timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, stock_item_id, warehouse_id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_items(tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses(tenant_id, id),
  CONSTRAINT reorder_band CHECK (max_qty >= min_qty)
);

-- ---------------------------------------- shared functions (verbatim + 0041)
-- p2p_subject_text: verbatim from 0040 plus unit conversions (marked 0041).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0040; unit conversions are company-level (no project).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion') THEN
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
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['uom_conversions', 'stock_reservations', 'stock_reorder_rules'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, submitted_at, approved_by, approved_at, decision_reason) ON uom_conversions TO serk_app;
GRANT UPDATE (status, released_by, released_at, release_reason) ON stock_reservations TO serk_app;
GRANT UPDATE (min_qty, max_qty, reorder_qty, updated_by, updated_at) ON stock_reorder_rules TO serk_app;
GRANT INSERT (reservation_id) ON stock_issues TO serk_app;
REVOKE ALL ON FUNCTION uom_factor(uuid, text), stock_reserved(uuid, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION uom_factor(uuid, text), stock_reserved(uuid, uuid, uuid) TO serk_app;
