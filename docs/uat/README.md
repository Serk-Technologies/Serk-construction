# SERK Construction OS - UAT Manual (source)

The manual is **generated**. Never edit the PDF or HTML; edit `src/*.txt` and rebuild.

    python3 docs/uat/build_uat.py            # writes the .html and .pdf (needs google-chrome, pdfinfo, pdftotext)
    python3 docs/uat/build_uat.py --check    # coverage check only; exit 1 if a Must requirement has no test

## Principles
* **Derived from the SRS** (`docs/srs/build_srs.py`), not from the current build. Expected results state what the SRS says the system *shall* do. Screen names are generic on purpose.
* Every test case lists the SRS requirement IDs it verifies. The build fails `--check` if any **Must** requirement has no test or a test cites an unknown ID. When the SRS changes, rebuild and the gaps appear.
* Each case is tagged with a release (R1-R4, SRS 9.2) and one or more roles. The "Role execution sheets" and the traceability appendix are generated from those tags.

## Source format (`src/NN_name.txt`, sorted by file name)
    # 12. Chapter title            new chapter (page break). Number it by hand.
    ## Heading / ### Sub-heading
    - bullet      > note box      | a | b |   (table; first row = header)
    @release R1                    default release for the cases that follow
    @term Word = definition        goes to the glossary (Appendix E)
    @case T-XXX-01 | Title | R1 | SRS-ID, SRS-ID | ROLE, ROLE
    pre: data or state needed before the steps
    1. step
    2. step
    => expected result (one per line; all must be true to Pass)
    why: the industry reason for the control
    note: evidence or caveat
    @scenario UC-xx | Title | ROLES | SRS-IDs | R1
    @before ... / | # | who | action | expected | / @after ...

Role keys: EXE EST QS PM SITE PROC STORE EQP HR AP FIN CFO TRS QA HSE AUD ADM EXT UAT (see `ROLES` in the script).

## Maintaining
* New SRS requirement -> add a `@case` that cites its ID (the check tells you which are missing).
* Changed fixture number -> search `src/00_front.txt` section 4.3 and the cases that quote it; figures are deliberately worked in the expected results.
* Tax and payroll values in the fixture are **test values**; the adviser-signed fixture (SRS D-04, D-06) overrides them.
