-- 0059 Cash forecasting: planned cash items (WP-2.12; SRS TRS-02)
--
-- The 13-week cash forecast is computed by the kernel from what the system
-- already knows (posted bank cash; approved receivables and payables by due
-- date; released payments not yet settled; certified client certificates not
-- yet invoiced; certified subcontract certificates not yet paid; open purchase
-- order balances by the date the goods are needed). This table adds what it
-- does not know yet: loan drawdowns and repayments, tax payments, payroll (until
-- WP-2.8), other planned receipts and payments. Treasury enters them; an item is
-- never edited, only cancelled with a reason (so the history of the plan stays).

CREATE TABLE cash_plan_items (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id uuid NOT NULL,
  due_on          date NOT NULL,
  direction       text NOT NULL CHECK (direction IN ('IN', 'OUT')),
  category        text NOT NULL CHECK (category IN ('LOAN', 'TAX', 'PAYROLL', 'CAPEX', 'OTHER')),
  amount          numeric(24,6) NOT NULL CHECK (amount > 0),
  description     text NOT NULL CHECK (length(btrim(description)) BETWEEN 3 AND 300),
  confidence      text NOT NULL DEFAULT 'FORECAST' CHECK (confidence IN ('CONFIRMED', 'FORECAST')),
  status          text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'CANCELLED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  cancel_reason   text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id)
);

CREATE FUNCTION cash_plan_items_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'ACTIVE' OR NEW.cancel_reason IS NOT NULL THEN
      RAISE EXCEPTION 'planned cash items are entered ACTIVE' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'cancel_reason'], 'planned cash item');
  IF OLD.status <> 'ACTIVE' OR NEW.status <> 'CANCELLED' OR NEW.cancel_reason IS NULL OR length(btrim(NEW.cancel_reason)) < 5 THEN
    RAISE EXCEPTION 'a planned cash item changes only by being cancelled with a reason' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER cash_plan_items_guard BEFORE INSERT OR UPDATE ON cash_plan_items FOR EACH ROW EXECUTE FUNCTION cash_plan_items_guard();

ALTER TABLE cash_plan_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE cash_plan_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cash_plan_items USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
DO $$ BEGIN
  EXECUTE format('CREATE POLICY owner_maintenance ON cash_plan_items TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', current_user);
END$$;
REVOKE ALL ON cash_plan_items FROM PUBLIC;
GRANT SELECT, INSERT ON cash_plan_items TO serk_app;
GRANT UPDATE (status, cancel_reason) ON cash_plan_items TO serk_app;
