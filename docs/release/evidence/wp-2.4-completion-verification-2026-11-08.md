# WP-2.4 completion (change events linked to variations): verification, 2026-11-08

SRS: PCC-07, CON-03. Migration `0054_change_variation_links.sql`; `LinkVariation` and the linked variations on
`GetChangeEvent` in `internal/kernel/change.go`; route `POST /v1/change-events/{id}/variations`.

| Control | Test |
|---|---|
| Only an approved change event is linked | TestChange_LinkedVariations |
| Linking needs `change.manage` on the event's project (an AR clerk is refused) | same |
| A variation of another project, or a rejected or cancelled one, is refused by the database | database guard |
| A variation carries one event; a link is never removed (no DELETE grant) | same (raw DELETE refused) |
| Recovered value = approved linked variations (1,850,000 after the variation is approved) | same |

Browser check (demo): the approved change event of P-001 shows *Recovered from the client*: revenue impact 80,000,
recovered 0, "80,000 not yet recovered", and the link control lists only variations of P-001 that are not rejected or
cancelled. No console errors. Links to subcontract variations come with subcontracts (WP-2.5).

Full suite (run together with WP-2.5 part 1): see that evidence: one environmental fixture failure in another test, the kernel package passed on rerun.
