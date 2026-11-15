# Capacity plan

**No capacity has been measured.** Nothing below is a benchmark result. The product has no coded ceiling on tenants, projects, transactions or years; the limits that exist are stated here so they are not mistaken for none.

## Limits that exist today

| Limit | Value | Where | Kind |
|---|---|---|---|
| Request body | 1 MiB (14.3 MiB on document upload) | `internal/httpapi` | protection |
| Document file | 10 MiB | kernel and `document_revisions` CHECK | protection, per file |
| List results | 100–500 rows per call, no cursor | `ListDocs` and list reads | **a hidden cap on what a user can see in one list**; cursor pagination is an open requirement (XCT-API.2) |
| Field devices | 5 active per person | 0037 | policy |
| Export budget | 200 per person per day (tenant setting) | platform | policy |
| Document numbers | per tenant, per prefix, per year; padded to 6 digits and widening beyond; the counter is a 32-bit integer (2,147,483,647 per document type, tenant and year) | `p2p_next_number`, `p2p_counters.last` | a coded ceiling, far above any plausible volume; widen to `bigint` when partitioning is designed |
| Tables | unpartitioned | — | will bound performance, not correctness |
| Topology | one database per deployment, or a dedicated database per tenant | `internal/store/postgres/router.go` | no cell moves |

## Sizing model

`rows/year = transactions/day × rows/transaction × active days`; storage = rows × measured bytes per row, plus indexes, WAL, replicas and backups, all to be measured. The Bible's illustration (1M transactions/day × 6 lines × 365 = 2.19B lines/year; at a hypothetical 300 bytes, about 657 GB/year raw) is arithmetic, not a measurement.

Document files are stored in PostgreSQL (ADR 0018): `sum(size_bytes)` of `document_revisions` is the measure to watch; move to object storage before it dominates backup time.

## First measurement: fleet queries (2026-11-15)

`tools/capacity/fleet_measure.sh` builds a disposable database, loads one tenant with synthetic rows (guard triggers off for the load only) and times the API's queries through the tenant's own login, so row-level security is in the plan. Result file: `evidence/2026-11-15-fleet-query-measurement.txt`.

Hardware: 4 vCPU, 16 GiB RAM, PostgreSQL 16.15 with default `shared_buffers` (128 MB), one session, no concurrency. Data: 100,000 machines, 1,000,000 approved logs (all dated in the last 10 days, across 100 projects). Sizes: equipment 61 MB, logs 652 MB with indexes.

| Query | Median of 5 |
|---|---|
| Register, first page of 50 | 0.09 ms |
| Register, page of 50 near the end, by keyset | 0.09 ms |
| The same depth by OFFSET (not used by the product) | 17.5 ms |
| Register, filtered by state, first page | 0.12 ms |
| Counts by state (100,000 rows) | 10.5 ms |
| Usage summary, 30 days, one project (about 10,000 logs) | 36.9 ms |
| Usage summary, 366 days, one project | 48.3 ms |
| Usage summary, 30 days, whole company (aggregates all 1,000,000 logs) | 695 ms |
| One machine's logs for a day | 0.07 ms |

What it showed:
- The first run, before three indexes were added, took 73 ms for the first register page and 53 ms for the per-machine lookup: the register was sorted on every page. The indexes are now part of migration 0054. Keyset paging costs the same at any depth.
- A company-wide summary that aggregates a million logs takes about 0.7 s on this machine. That is acceptable for one request but will not hold at tens of millions of logs or many concurrent users: it needs an incremental daily roll-up per machine before then. Recorded as an open item.
- This is query time on one idle machine. It says nothing about throughput under concurrent load, the API layer, or larger data.

## What to measure next (not done)

1. Bytes per `journal_lines` row and per index, from a seeded tenant.
2. Posting throughput and p95 latency at 50, 200 and 500 concurrent users on stated hardware.
3. The slowest ten report queries at 10M and 100M journal lines.
4. Lock waits between posting, period close and stock movements under load.
5. Backup and restore time against database size.

A repeatable load generator does not exist yet. Tier T1 (100 projects, 10M journal lines, 200 concurrent users) is the first target; T2 and T3 require partitioning and cells, which are undecided (`ADR/README.md`).

## Triggers (proposed, to tune with operators)

Disk above 70%, sustained p95 above the SLO in `docs/operations/OBSERVABILITY_SLOS.md`, outbox oldest event above its SLO, growing vacuum debt. Alert rules for several of these exist and fire in `TestAlertDrill_InducedFailuresFireTheRightAlerts`.
