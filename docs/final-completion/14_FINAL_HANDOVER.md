# Final-completion handover

The live resume point is `docs/engineering/execution-state.json` (`exact_next_action`). Read it with
`docs/engineering/HANDOVER.md`, `docs/final-completion/00_PROGRAM_CHARTER.md` and
`docs/completion/22_FINAL_SCOPE_AND_GAP_REGISTER.md`.

Regenerate the control centre after any change to the register:

    python3 tools/requirements_register.py --results docs/engineering/evidence/<run>.tests.txt --commit <sha>
    python3 tools/final_completion.py && python3 tools/final_completion.py --check

Release qualification (expected to fail until the contract is met):

    python3 tools/final_completion.py --release
