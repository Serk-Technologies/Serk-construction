# Release gates

The repository's gate record is `docs/release/GO_NO_GO.md` with `docs/release/GATE_STATUS.json` (generated) and the checklist CSV. Its decision is **NO-GO**; nothing in this session changes that. This page maps the milestones of `MASTER_PROMPT.md` section 12 to their gates and current state.

| Milestone | Gate | State on 2026-11-15 |
|---|---|---|
| M0 Discover | Known status, no destructive change | **Met**: `BASELINE.md`, `REQUIREMENTS.md`, this folder |
| M1 Foundation | Cross-tenant and role tests pass | Tests pass. Open: real identity provider, signer key custody |
| M2 Project/estimate | Import, version and budget invariants | Pass for BOQ and budget. Open: estimating, rates, tender |
| M3 Supply/stock/GL | Atomic posting and concurrency | Pass |
| M4 Field/IPC | Complete golden path | Pass from BOQ to receipt and reports. Open: work packages, measurement book detail |
| M5 Offline | Restart, revocation, duplication | Server side passes. **Open: no field client** |
| M6 Enterprise ops | Integrated job cost, UAT fixtures | Scheduling and EVM only. Open: fleet, payroll, quality, HSE, resources |
| M7 Contracts/CDE | Permission, signature/approval, retention | Variations, claims, **document control and RFIs** pass. Open: transmittals, portals, real estate, handover, retention |
| M8 BIM/GIS | Standards and measured models | Not started |
| M9 BRAIN | AI evaluation and safety authorisation | Boundary enforced; gateway and agents not started |
| M10 Scale/history | Reconciled historical retrieval, benchmark | Not started |
| M11 Localisation/deploy | Local professional approval, DR | Blocked on people; drill scripts exist |
| M12 Release candidate | Human release decision | Not reached |

## Gates only people can pass

- Accountant sign-off on posting policies, FX policy, revenue recognition.
- Jurisdiction review of tax, payroll and statutory rules before any real rate is loaded.
- Independent penetration test; resolution of critical and high findings.
- Construction, quality and HSE professional acceptance of field workflows.
- Operated staging with a real identity provider, restore drill and monitoring.
- Pilot organisations' written approval before their names or data are used.

No one may mark these from generated tests. The delivering agent cannot sign a release.
