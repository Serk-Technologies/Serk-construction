-- 0052 Cost forecasting: CPI method and change against the last forecast
-- (WP-2.1 completion; SRS PCC-04, PCC-05, PCC-10)
--
-- WHAT THIS MIGRATION DOES
--   * Method CPI: ETC = (BAC - EV) / CPI per cost code, where the cost code's
--     earned value is its budget x the project's earned percentage (EV / BAC of
--     the baseline schedule at the forecast date) and CPI = EV / AC of the
--     project at that date (schedule_evm). Refused when the project has no
--     baseline with approved progress and posted cost (no CPI).
--   * prev_eac: each line freezes the EAC of the same cost code in the
--     project's last APPROVED forecast before this one, so the change is part
--     of what the approver attests.
--   * A line whose EAC moved by more than 10% of its budget since that
--     forecast must be explained (10+ characters) before submission.
--
-- forecast_line_compute is redefined verbatim from 0044 plus CPI and prev_eac
-- (marked 0052); forecast_cycles_guard verbatim from 0047 plus the change rule.

ALTER TABLE forecast_lines DROP CONSTRAINT forecast_lines_method_check;
ALTER TABLE forecast_lines ADD CONSTRAINT forecast_lines_method_check CHECK (method IN ('REMAINING_BUDGET', 'OPEN_COMMITMENTS', 'MANUAL', 'CPI'));
ALTER TABLE forecast_lines ADD COLUMN prev_eac numeric(24,6);

CREATE OR REPLACE FUNCTION forecast_line_compute() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE c forecast_cycles; r record; e record;
BEGIN
  SELECT * INTO c FROM forecast_cycles WHERE id = NEW.cycle_id AND tenant_id = current_tenant();
  IF c.status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'forecast lines change only while the cycle is DRAFT' USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND (NEW.cycle_id <> OLD.cycle_id OR NEW.cost_code_id <> OLD.cost_code_id) THEN
    RAISE EXCEPTION 'a forecast line cannot move' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO r FROM p2p_project_cost_report(c.project_id) WHERE cost_code_id = NEW.cost_code_id;
  IF NOT FOUND THEN
    -- A code with no money on it yet: everything is zero.
    NEW.budget := 0; NEW.committed := 0; NEW.reserved := 0; NEW.received := 0; NEW.cost_to_date := 0;
  ELSE
    NEW.budget := r.current; NEW.committed := r.committed; NEW.reserved := r.reserved; NEW.received := r.received;
    NEW.cost_to_date := r.actual_posted;
  END IF;
  IF NEW.method = 'CPI' THEN
    -- 0052: performance to date continues on the remaining work.
    SELECT * INTO e FROM schedule_evm(c.project_id, c.period_end);
    IF e.version_id IS NULL OR e.cpi IS NULL OR e.cpi <= 0 OR e.bac = 0 THEN
      RAISE EXCEPTION 'the CPI method needs a baseline schedule with approved progress and posted cost at %', c.period_end USING ERRCODE = 'check_violation';
    END IF;
    NEW.etc := round_half_even(greatest(NEW.budget - NEW.budget * e.ev / e.bac, 0) / e.cpi, 2);
  ELSE
    NEW.etc := CASE NEW.method
      WHEN 'REMAINING_BUDGET' THEN greatest(NEW.budget - NEW.cost_to_date, 0)
      WHEN 'OPEN_COMMITMENTS' THEN greatest(NEW.committed - NEW.received, 0) + NEW.reserved
      ELSE NEW.manual_etc END;
  END IF;
  NEW.eac := NEW.cost_to_date + NEW.etc;
  NEW.vac := NEW.budget - NEW.eac;
  -- 0052: the same code in the last approved forecast before this one.
  NEW.prev_eac := (SELECT l.eac FROM forecast_lines l JOIN forecast_cycles f ON f.id = l.cycle_id
                    WHERE f.project_id = c.project_id AND f.tenant_id = current_tenant() AND f.status = 'APPROVED' AND f.id <> c.id
                      AND f.period_end <= c.period_end AND l.cost_code_id = NEW.cost_code_id
                    ORDER BY f.period_end DESC, f.approved_at DESC LIMIT 1);
  RETURN NEW;
END$$;

CREATE OR REPLACE FUNCTION forecast_cycles_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE bad text;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'forecast cycles are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.period_end > current_date + 31 THEN
      RAISE EXCEPTION 'a forecast is for the current period or an earlier one' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a decided forecast is immutable; prepare a new cycle' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['note'], 'forecast cycle');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'CANCELLED' THEN
    -- 0047: a draft is abandoned by its author (it never had an approval to undo).
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'decision_reason'], 'forecast cycle');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'cancelling a draft forecast requires a reason' USING ERRCODE = 'check_violation';
    END IF;
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'forecast cycle');
    IF NOT EXISTS (SELECT 1 FROM forecast_lines WHERE cycle_id = NEW.id) THEN
      RAISE EXCEPTION 'a forecast needs at least one line' USING ERRCODE = 'check_violation';
    END IF;
    SELECT c.code INTO bad FROM forecast_lines l JOIN cost_codes c ON c.id = l.cost_code_id
     WHERE l.cycle_id = NEW.id AND l.eac > l.budget AND length(btrim(COALESCE(l.note, ''))) < 10 LIMIT 1;
    IF bad IS NOT NULL THEN
      RAISE EXCEPTION 'cost code % is forecast above its budget: explain why in its note (10+ characters)', bad USING ERRCODE = 'check_violation';
    END IF;
    -- 0052: a line whose EAC moved by more than 10% of its budget since the
    -- last approved forecast must explain the change.
    SELECT c.code INTO bad FROM forecast_lines l JOIN cost_codes c ON c.id = l.cost_code_id
     WHERE l.cycle_id = NEW.id AND l.prev_eac IS NOT NULL AND abs(l.eac - l.prev_eac) > greatest(l.budget, 0) * 0.10
       AND length(btrim(COALESCE(l.note, ''))) < 10 LIMIT 1;
    IF bad IS NOT NULL THEN
      RAISE EXCEPTION 'cost code % moved by more than 10%% of its budget since the last approved forecast: explain the change in its note (10+ characters)', bad USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('forecast.submit', 'forecast_cycle', NEW.id::text, NEW.created_by, ARRAY['forecast.prepare'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'forecast cycle');
    PERFORM approval_use('forecast.decide', 'forecast_cycle', NEW.id::text, NEW.approved_by, ARRAY['forecast.approve'], false, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'forecast cycle');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a forecast requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('forecast.decide', 'forecast_cycle', NEW.id::text, NEW.approved_by, ARRAY['forecast.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal forecast transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
