# WP-3.6 Quality, WP-3.8 Punch lists, instructions, meetings: verification, 2026-11-09

Migration `0065_quality_site.sql`; `internal/kernel/quality.go`; `internal/httpapi/quality_handlers.go`; console **Quality** and **Punch lists,
instructions, meetings**. Permissions `quality.request`, `quality.manage`, `quality.approve`, `site.record`, `site.instruct`; role
`quality_engineer`; duty-conflict pair "Quality plans"; persona `u-quality`.

## Controls (database-enforced)
ITPs: prepared by a quality engineer, approved by a project manager; a HOLD item stops progress on its activity above a percentage until an
inspection of it has PASSED (trigger on schedule progress lines). Inspection requests only against approved plans; the requester cannot decide
their own. NCRs: raised, root cause and corrective action recorded, closed by a person other than the one who raised or actioned it, with a
verification note; closed reports never change. Material tests compute pass or fail against the specification and a failure raises an NCR.
Punch items: the fixer cannot close. Site instructions: the issuer cannot acknowledge; optional link to a change event of the same project.
Meetings: issued minutes never change; actions are completed once.

## Test (TestQuality_HoldPointNCRAndSiteRecords)
Progress of 40% on activity B refused before the rebar hold point passes and after a failed inspection, accepted after a pass; 18 N/mm2 against a
minimum of 25 fails and raises an NCR automatically while 31 passes; unactioned NCR cannot be closed; site engineer cannot close; punch, instruction
and meeting rules proven.

## Not built
Document links (WP-3.5), mobile capture (WP-3.1), contractor-side portal for instructions.
