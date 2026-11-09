# WP-1.10 addendum: account menu, dependency graph, adjustable product roles (2026-11-08)

Requested in review: a more formal top band (a dropdown instead of many controls), user management like Serk Finance
with a dependency graph, and roles that can be adjusted at any time, including removing permissions.

## Delivered

| Area | What |
|---|---|
| Top band | The profile link, MFA badge and Sign out are now one **account menu** (avatar, name and caret). It shows the name, sign-in id, roles and sign-in strength ("Signed in with a second factor…" or "password only…"), then *My profile*, *People & access* (for those who may see it) and *Sign out*. It opens on click, closes on Escape, a click outside or navigation, and the arrow keys move between items. Bell and day/night stay as icons. |
| People & access | The Duty map tab is now **Dependency graph** (default) with *Columns* as the alternative view. People sit in stage columns (Prepare, Approve, Release, Govern). For every control (two permissions that must be held by different people), a curved arrow runs from each holder of the earlier-stage half to each holder of the later half. Hovering or selecting a person highlights their hand-offs and lists *Hands work to* and *Receives work from* by control; zoom and reset; *Open person*. |
| Adjustable roles | Migration `0060_role_adjustments.sql`; `AdjustRole` in `internal/kernel/people.go`; `PUT /v1/roles/{code}/adjustment`. A security administrator (`role.manage`, a person, fresh MFA, a reason) may **withhold any permission of a product role** for the company and restore it later. It applies from each holder's next request: `ApplyDirectory` fills `Principal.Withheld`, and the authoriser skips withheld permissions in `Has` and `Permissions` (and so in every attestation). The role book shows effective permissions, so people, duty conflicts, stages, the profile and the graph follow automatically. User and role administration can never be withheld (kernel and database CHECK), so access can always be repaired. Role cards show "*n* withheld"; the role sheet shows the product definition with ticks; every change is in the access history. Giving more than a product role is still done with a company role. |

## Tests

`TestPeople_AdjustProductRole`:
- a non-administrator is refused, a change without fresh MFA is refused, a change without a reason is refused;
- `role.manage` cannot be withheld from the security administrator, and a permission outside the role is refused;
- after withholding `po.approve` from the procurement manager, the holder no longer has it (`Has` and `Permissions`) but keeps `supplier.approve`;
- the catalogue shows one withheld permission, adjusted by `u-secadmin`;
- clearing the list restores the permission.

Journey A now signs out through the account menu.

## Browser check (demo on :8080)

**Account menu:** items *My profile | People & access | Sign out*; Escape closes it; checked in day and night themes.

**Dependency graph:** 27 people and 58 hand-offs across 57 controls. Selecting the Accountant listed:
- hands work to the Finance manager (journals), the Treasury officers (payments: release, reconcile) and the CFO (fixed assets);
- receives work from the Bank statement officer.

**Procurement manager role:** *Approve purchase orders* unticked ("1 of 7 withheld…"), saved with a reason, then restored. Both changes are in the access history.

Two defects fixed during the check: a stray "null" in the side panel, and the later columns falling off-screen (the nodes are narrower now).

## Full suite

`go vet ./...` and `go test ./... -count=1` with PostgreSQL 16 and the Chrome browser journeys (accessibility audited on
every page, day and night, 390 px): all 18 packages with tests `ok`, 0 FAIL, exit 0.
