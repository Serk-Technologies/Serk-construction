#!/usr/bin/env bash
# Reproducible measurement of the fleet register and summary queries at volume.
#
# Builds a DISPOSABLE database, applies every migration, bulk-loads synthetic
# equipment and approved logs for one tenant with the guard triggers switched
# off (session_replication_role = replica: this is a load fixture, not a path
# the product offers), then times the queries the API runs, through the
# tenant's own login so row-level security is in the plan.
#
# It measures query time on one machine. It is not a load test: no
# concurrency, no API layer, no network. Record the hardware with the result.
#
#   ADMIN='postgres://postgres@/postgres?host=/tmp' EQUIPMENT=100000 LOGS=1000000 tools/capacity/fleet_measure.sh
set -euo pipefail
ADMIN=${ADMIN:?set ADMIN to a superuser DSN of a disposable cluster}
EQUIPMENT=${EQUIPMENT:-100000}
LOGS=${LOGS:-1000000}
DB=serk_capacity_$$
DSN=$(printf '%s' "$ADMIN" | sed -E "s#^(postgres(ql)?://[^/]*/)[^?]*#\\1$DB#")
psql "$ADMIN" -q -c "CREATE DATABASE $DB"
trap 'psql "$ADMIN" -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)"' EXIT
psql "$DSN" -q -v ON_ERROR_STOP=1 -f deploy/00-roles.sql
SERK_ADMIN_DSN="$DSN" go run ./cmd/serk-migrate > /dev/null
TENANT=$(psql "$DSN" -At -c "SELECT gen_random_uuid()")
SERK_ADMIN_DSN="$DSN" SERK_TENANT_DB_SECRET="$(openssl rand -hex 24)" SERK_APPROVAL_SIGNING_KEY="$(openssl rand -hex 24)" \
  go run ./cmd/serk-migrate provision-tenant --tenant "$TENANT" --name "Capacity Fixture" > /dev/null
ROLE=$(psql "$DSN" -At -c "SELECT role_name FROM tenant_db_roles WHERE tenant_id = '$TENANT'")
echo "### fleet measurement $(date -u +%FT%TZ): $EQUIPMENT machines, $LOGS approved logs, tenant role $ROLE"
echo "### $(psql "$DSN" -At -c 'select version()' | cut -c1-40); $(nproc) CPU; $(awk '/MemTotal/ {printf "%.0f GiB RAM", $2/1048576}' /proc/meminfo); shared_buffers $(psql "$DSN" -At -c 'show shared_buffers')"
psql "$DSN" -q -v ON_ERROR_STOP=1 <<SQL
SET session_replication_role = replica;
INSERT INTO legal_entities (id, tenant_id, name, country, functional_currency) VALUES ('00000000-0000-4000-8000-000000000001', '$TENANT', 'Capacity PLC', 'ET', 'ETB');
INSERT INTO projects (id, tenant_id, legal_entity_id, code, name, contract_currency)
  SELECT ('00000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid, '$TENANT', '00000000-0000-4000-8000-000000000001', 'P-' || g, 'Project ' || g, 'ETB' FROM generate_series(100, 199) g;
INSERT INTO equipment (id, tenant_id, legal_entity_id, number, name, class, ownership, manufacturer, model, serial_number, status, current_project_id, status_changed_by, created_by, last_meter)
  SELECT ('10000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid, '$TENANT', '00000000-0000-4000-8000-000000000001', 'EQ-2026-' || lpad(g::text, 8, '0'), 'Machine ' || g,
         (ARRAY['EXCAVATOR','GRADER','LOADER','BULLDOZER','CRANE','GENERATOR','TRUCK','MIXER'])[1 + g % 8], 'OWNED', 'Maker' || (g % 40), 'M' || (g % 300), 'SN' || g,
         CASE WHEN g % 10 < 6 THEN 'ASSIGNED' WHEN g % 10 < 9 THEN 'AVAILABLE' ELSE 'UNDER_MAINTENANCE' END,
         CASE WHEN g % 10 < 6 OR g % 10 = 9 THEN ('00000000-0000-4000-8000-' || lpad((100 + g % 100)::text, 12, '0'))::uuid END, 'load', 'load', 1000 + g % 5000
    FROM generate_series(1, $EQUIPMENT) g;
INSERT INTO equipment_logs (tenant_id, equipment_id, project_id, number, log_date, shift, operator, meter_serial, meter_open, meter_close, productive_hours, idle_hours, down_hours, fuel_litres, status, created_by, approved_by, approved_at)
  SELECT '$TENANT', ('10000000-0000-4000-8000-' || lpad((1 + g % $EQUIPMENT)::text, 12, '0'))::uuid, ('00000000-0000-4000-8000-' || lpad((100 + (g % $EQUIPMENT) % 100)::text, 12, '0'))::uuid,
         'EQL-L-' || g, current_date - ((g / $EQUIPMENT) % 720)::int, 1 + (g / ($EQUIPMENT * 720)) % 3, 'op-' || (g % 500), 1, 1000, 1008, 6 + (g % 3) * 0.5, 1, (g % 4) * 0.25, 80 + g % 40, 'APPROVED', 'load', 'approver', now()
    FROM generate_series(1, $LOGS) g ON CONFLICT DO NOTHING;
SET session_replication_role = origin;
ANALYZE equipment; ANALYZE equipment_logs;
SQL
psql "$DSN" -At -c "SELECT 'rows: equipment ' || (SELECT count(*) FROM equipment) || ', logs ' || (SELECT count(*) FROM equipment_logs) || '; sizes: equipment ' || pg_size_pretty(pg_total_relation_size('equipment')) || ', logs ' || pg_size_pretty(pg_total_relation_size('equipment_logs'))"
# The tenant's own login (the cluster must let it connect: trust authentication
# on a disposable cluster, or PGPASSWORD set to the tenant's derived password).
TDSN="${DSN/\/\/postgres@/\/\/$ROLE@}"
[ "$(psql "$TDSN" -At -c 'SELECT count(*) FROM equipment')" = "$(psql "$DSN" -At -c "SELECT count(*) FROM equipment WHERE tenant_id = '$TENANT'")" ] || { echo "the tenant login does not see its rows" >&2; exit 1; }
run() { # name, sql : median of 5 timed executions as the tenant login
  local name=$1 sql=$2 t
  t=$(for i in 1 2 3 4 5; do psql "$TDSN" -At -q -c "EXPLAIN (ANALYZE, TIMING OFF, SUMMARY ON) $sql" | awk '/Execution Time/ {print $3}'; done | sort -n | sed -n 3p)
  printf '%-58s %10s ms (median of 5)\n' "$name" "$t"
}
LAST=$(printf 'EQ-2026-%08d' $((EQUIPMENT - 60)))
PAGE="SELECT e.id, e.number, e.name, e.status FROM equipment e WHERE ('' = '' OR e.status = '') AND e.number COLLATE \"C\" > '%s' ORDER BY e.number COLLATE \"C\" LIMIT 51"
run "register, first page of 50"                 "$(printf "$PAGE" '')"
run "register, page of 50 near the end (keyset)" "$(printf "$PAGE" "$LAST")"
run "register, same depth by OFFSET (not used)"  "SELECT e.id FROM equipment e ORDER BY e.number COLLATE \"C\" LIMIT 51 OFFSET $((EQUIPMENT - 60))"
run "register, status filter, first page"        "SELECT e.id FROM equipment e WHERE e.status = 'UNDER_MAINTENANCE' AND e.number COLLATE \"C\" > '' ORDER BY e.number COLLATE \"C\" LIMIT 51"
run "counts by state"                             "SELECT status, count(*) FROM equipment GROUP BY status"
SUM="SELECT e.id, count(l.id), sum(l.productive_hours), sum(l.idle_hours), sum(l.down_hours), sum(l.fuel_litres) FROM equipment e JOIN equipment_logs l ON l.equipment_id = e.id AND l.status = 'APPROVED' AND l.log_date BETWEEN current_date - %s AND current_date %s GROUP BY e.id ORDER BY sum(l.productive_hours) DESC, e.number LIMIT 500"
run "summary, 30 days, one project"   "$(printf "$SUM" 30 "AND l.project_id = '00000000-0000-4000-8000-000000000150'")"
run "summary, 30 days, whole company" "$(printf "$SUM" 30 '')"
run "summary, 366 days, one project"  "$(printf "$SUM" 366 "AND l.project_id = '00000000-0000-4000-8000-000000000150'")"
run "one machine's logs for a day (uniqueness lookup)" "SELECT 1 FROM equipment_logs WHERE equipment_id = '10000000-0000-4000-8000-000000000777' AND log_date = current_date - 3 AND shift = 1"
