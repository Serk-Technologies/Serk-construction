-- 0047 Cancel a draft cost forecast (WP-2.1 follow-up)
--
-- WHY: a project may hold one open forecast (DRAFT or SUBMITTED) at a time
-- (0044). A draft that its author abandoned would block the project forever,
-- because nothing can delete it. This adds the status CANCELLED: the author of
-- a DRAFT may cancel it with a reason; a cancelled forecast never changes and
-- no longer counts as open. Submitted and decided forecasts cannot be
-- cancelled (they are rejected by the approver instead).
--
-- forecast_cycles_guard is redefined verbatim from 0044 plus the CANCELLED branch (marked 0047).

ALTER TABLE forecast_cycles DROP CONSTRAINT forecast_cycles_status_check;
ALTER TABLE forecast_cycles ADD CONSTRAINT forecast_cycles_status_check CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED'));

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
