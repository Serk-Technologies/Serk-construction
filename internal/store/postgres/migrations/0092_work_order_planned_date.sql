-- 0092_work_order_planned_date.sql — when a work order is planned to be done (FLT-03.1.d, maintenance calendar).
--
-- A work order may say the day it is planned for. The day is chosen when the order is prepared, is today or
-- later, and never changes afterwards (a different day is a new order). The maintenance calendar shows open
-- orders on their planned day, date-based preventive plans on their due day, and meter-based plans on a day
-- projected from the machine's recent approved daily logs (labelled as an estimate).

ALTER TABLE maintenance_work_orders ADD COLUMN planned_for date;

CREATE FUNCTION maintenance_work_orders_planned() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.planned_for IS NOT NULL AND NEW.planned_for < current_date THEN
      RAISE EXCEPTION 'a work order is planned for today or later' USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.planned_for IS DISTINCT FROM OLD.planned_for THEN
    RAISE EXCEPTION 'the planned day of a work order never changes' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER maintenance_work_orders_zplanned BEFORE INSERT OR UPDATE ON maintenance_work_orders FOR EACH ROW EXECUTE FUNCTION maintenance_work_orders_planned();

-- Average meter units a machine ran per day over its approved daily logs of the last p_days days (NULL when unknown).
CREATE FUNCTION equipment_daily_usage(p_equipment uuid, p_days integer) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN count(*) = 0 THEN NULL ELSE round(sum(l.meter_close - l.meter_open) / p_days, 4) END
    FROM equipment_logs l
   WHERE l.equipment_id = p_equipment AND l.tenant_id = current_tenant() AND l.status = 'APPROVED'
     AND l.log_date > current_date - p_days AND l.meter_close >= l.meter_open
$$;
REVOKE ALL ON FUNCTION equipment_daily_usage(uuid, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION equipment_daily_usage(uuid, integer) TO serk_app;
