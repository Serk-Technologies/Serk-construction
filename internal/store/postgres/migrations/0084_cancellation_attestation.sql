-- 0084_cancellation_attestation.sql — who cancelled is attested; an asset is disposed of by its approved disposal (DEF-017).
--
-- The carried consolidation runs (0068), tax returns (0069) and subcontract payments (0062) could be cancelled by the
-- tenant login with any reason and no record of who cancelled them. A cancellation is now recorded in
-- document_cancellations, attested for the person cancelling (consolidation.manage, tax.prepare, or payment.prepare /
-- payment.approve), by the transaction that cancels the document: the document refuses CANCELLED without that row,
-- and the row refuses to outlive a transaction that did not cancel its document. The existing guards (status and
-- reason only) are unchanged.
--
-- The carried fixed-asset guard (0063) accepted ACTIVE -> DISPOSED from any nested trigger (pg_trigger_depth() >= 2).
-- The asset is now marked DISPOSED by an AFTER trigger of its disposal's approval, and the asset guard requires a
-- disposal of that asset approved by the current transaction (xmin), never a trigger depth. Both functions are copied
-- verbatim from 0063 apart from the marked changes.

CREATE TABLE document_cancellations (
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  subject_type text NOT NULL CHECK (subject_type IN ('consolidation_run', 'tax_return', 'subcontract_payment')),
  subject_id   uuid NOT NULL,
  cancelled_by text NOT NULL,
  reason       text NOT NULL CHECK (length(btrim(reason)) >= 5),
  cancelled_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, subject_type, subject_id)
);

CREATE FUNCTION document_cancellations_attest() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN
    RAISE EXCEPTION 'a cancellation record never changes' USING ERRCODE = 'check_violation';
  END IF;
  NEW.cancelled_at := now();
  CASE NEW.subject_type
    WHEN 'consolidation_run' THEN
      PERFORM approval_use('conrun.cancel', 'consolidation_run', NEW.subject_id::text, NEW.cancelled_by, ARRAY['consolidation.manage'], false, 'CANCELLED', NULL);
    WHEN 'tax_return' THEN
      PERFORM approval_use('taxreturn.cancel', 'tax_return', NEW.subject_id::text, NEW.cancelled_by, ARRAY['tax.prepare'], false, 'CANCELLED', NULL);
    ELSE
      PERFORM approval_use('subpay.cancel', 'subcontract_payment', NEW.subject_id::text, NEW.cancelled_by, ARRAY['payment.prepare', 'payment.approve'], false, 'CANCELLED', NULL);
  END CASE;
  RETURN NEW;
END$$;
CREATE TRIGGER document_cancellations_attest BEFORE INSERT OR UPDATE OR DELETE ON document_cancellations FOR EACH ROW EXECUTE FUNCTION document_cancellations_attest();

-- At commit the cancelled document must be CANCELLED (no orphan record that would block a later real cancellation).
CREATE FUNCTION document_cancellations_used() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE st text;
BEGIN
  CASE NEW.subject_type
    WHEN 'consolidation_run' THEN SELECT status INTO st FROM consolidation_runs WHERE id = NEW.subject_id;
    WHEN 'tax_return' THEN SELECT status INTO st FROM tax_returns WHERE id = NEW.subject_id;
    ELSE SELECT status INTO st FROM subcontract_payments WHERE id = NEW.subject_id;
  END CASE;
  IF st IS DISTINCT FROM 'CANCELLED' THEN
    RAISE EXCEPTION 'a cancellation of % % was recorded but the document was not cancelled', NEW.subject_type, NEW.subject_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER document_cancellations_used AFTER INSERT ON document_cancellations DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION document_cancellations_used();

-- A document becomes CANCELLED only with its attested cancellation, recorded by this transaction, with the same reason.
CREATE FUNCTION serk_cancellation_recorded() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM document_cancellations c WHERE c.tenant_id = current_tenant() AND c.subject_type = TG_ARGV[0] AND c.subject_id = NEW.id
                   AND c.xmin = pg_current_xact_id()::xid AND c.reason = btrim(to_jsonb(NEW) ->> TG_ARGV[1])) THEN
    RAISE EXCEPTION 'cancelling a % requires an attested cancellation by the person cancelling, recorded in the same transaction', replace(TG_ARGV[0], '_', ' ')
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER consolidation_runs_zcancel BEFORE UPDATE ON consolidation_runs FOR EACH ROW
  WHEN (NEW.status = 'CANCELLED' AND OLD.status IS DISTINCT FROM 'CANCELLED') EXECUTE FUNCTION serk_cancellation_recorded('consolidation_run', 'decision_reason');
CREATE TRIGGER tax_returns_zcancel BEFORE UPDATE ON tax_returns FOR EACH ROW
  WHEN (NEW.status = 'CANCELLED' AND OLD.status IS DISTINCT FROM 'CANCELLED') EXECUTE FUNCTION serk_cancellation_recorded('tax_return', 'decision_reason');
CREATE TRIGGER subcontract_payments_zcancel BEFORE UPDATE ON subcontract_payments FOR EACH ROW
  WHEN (NEW.status = 'CANCELLED' AND OLD.status IS DISTINCT FROM 'CANCELLED') EXECUTE FUNCTION serk_cancellation_recorded('subcontract_payment', 'cancel_reason');

ALTER TABLE document_cancellations ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_cancellations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON document_cancellations USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
DO $$
BEGIN
  EXECUTE format('CREATE POLICY owner_maintenance ON document_cancellations TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', current_user);
END$$;
REVOKE ALL ON document_cancellations FROM PUBLIC;
GRANT SELECT, INSERT ON document_cancellations TO serk_app;

-- ------------------------------------------------------------------ asset disposal (verbatim from 0063 except as marked)
CREATE OR REPLACE FUNCTION fixed_assets_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE c asset_classes;
BEGIN
  IF TG_OP = 'INSERT' THEN
    SELECT * INTO c FROM asset_classes WHERE id = NEW.class_id AND tenant_id = current_tenant();
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.cap_journal_id IS NOT NULL THEN
      RAISE EXCEPTION 'assets are registered as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    NEW.legal_entity_id := c.legal_entity_id;
    IF NEW.project_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id AND legal_entity_id = c.legal_entity_id) THEN
      RAISE EXCEPTION 'the asset''s project belongs to another entity' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.capitalised_from_account_id AND legal_entity_id = c.legal_entity_id AND active AND type IN ('ASSET', 'EXPENSE', 'LIABILITY'))
       OR NEW.capitalised_from_account_id IN (c.cost_account_id, c.accum_account_id) THEN
      RAISE EXCEPTION 'capitalise from an active asset-under-construction, expense or clearing account of the entity (not the class''s own accounts)' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'DISPOSED') THEN
    RAISE EXCEPTION 'a % asset never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'ACTIVE' AND NEW.status = 'ACTIVE' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['cap_journal_id'], 'asset');
    IF OLD.cap_journal_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.cap_journal_id AND source_type = 'FA_CAP' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'an asset is bound once to its own DRAFT FA_CAP journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ACTIVE' AND NEW.status = 'DISPOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'asset');
    -- 0084 (DEF-017): the disposal approved by this transaction, not the trigger depth, is the proof.
    IF NOT EXISTS (SELECT 1 FROM asset_disposals d WHERE d.asset_id = NEW.id AND d.tenant_id = current_tenant() AND d.status = 'APPROVED'
                     AND d.xmin = pg_current_xact_id()::xid) THEN
      RAISE EXCEPTION 'an asset is disposed only by approving its disposal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'asset');
    PERFORM approval_use('asset.submit', 'fixed_asset', NEW.id::text, NEW.created_by, ARRAY['asset.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('ACTIVE', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'asset');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting an asset requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('asset.decide', 'fixed_asset', NEW.id::text, NEW.approved_by, ARRAY['asset.approve'], NEW.status = 'ACTIVE', CASE WHEN NEW.status = 'ACTIVE' THEN 'APPROVED' ELSE 'REJECTED' END, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal asset transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

CREATE OR REPLACE FUNCTION asset_disposals_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE a fixed_assets; acc numeric;
BEGIN
  SELECT * INTO a FROM fixed_assets WHERE id = NEW.asset_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.journal_id IS NOT NULL OR NEW.nbv IS NOT NULL THEN
      RAISE EXCEPTION 'disposals are proposed as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF a.status <> 'ACTIVE' THEN
      RAISE EXCEPTION 'only an ACTIVE asset is disposed of' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.disposed_on < a.in_service_on OR NEW.disposed_on > current_date THEN
      RAISE EXCEPTION 'the disposal date is between the in-service date and today' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.proceeds_account_id AND legal_entity_id = a.legal_entity_id AND type = 'ASSET' AND active) THEN
      RAISE EXCEPTION 'proceeds go to an active ASSET account of the entity (bank clearing or a receivable)' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') AND NOT (OLD.status = 'APPROVED' AND OLD.journal_id IS NULL) THEN
    RAISE EXCEPTION 'a decided disposal never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'disposal');
    IF NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.journal_id AND source_type = 'FA_DISP' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a disposal is bound once to its own DRAFT FA_DISP journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'disposal');
    PERFORM approval_use('disposal.submit', 'asset_disposal', NEW.id::text, NEW.created_by, ARRAY['asset.manage'], false, NULL, NULL);
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason', 'accumulated', 'nbv', 'gain'], 'disposal');
    IF NEW.status = 'REJECTED' THEN
      IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
        RAISE EXCEPTION 'rejecting a disposal requires a reason' USING ERRCODE = 'check_violation';
      END IF;
      PERFORM approval_use('disposal.decide', 'asset_disposal', NEW.id::text, NEW.approved_by, ARRAY['asset.approve'], false, 'REJECTED', NULL);
      NEW.approved_at := now();
      RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM depreciation_lines l JOIN depreciation_runs r ON r.id = l.run_id JOIN journals j ON j.id = r.journal_id
                WHERE l.asset_id = a.id AND r.status = 'PREPARED' AND j.status IN ('DRAFT', 'APPROVED')) THEN
      RAISE EXCEPTION 'the asset is in a depreciation run whose journal is not posted: post or cancel it first' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('disposal.decide', 'asset_disposal', NEW.id::text, NEW.approved_by, ARRAY['asset.approve'], true, 'APPROVED', NULL);
    acc := fa_accumulated(a.id);
    NEW.accumulated := acc;
    NEW.nbv := a.cost - acc;
    NEW.gain := NEW.proceeds - NEW.nbv;
    NEW.approved_at := now();
    -- 0084: the asset is marked DISPOSED by asset_disposals_zdispose, after this row is stored.
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal disposal transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;

CREATE FUNCTION asset_disposals_dispose() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  UPDATE fixed_assets SET status = 'DISPOSED' WHERE id = NEW.asset_id AND tenant_id = current_tenant();
  RETURN NULL;
END$$;
CREATE TRIGGER asset_disposals_zdispose AFTER UPDATE ON asset_disposals FOR EACH ROW
  WHEN (OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED') EXECUTE FUNCTION asset_disposals_dispose();
