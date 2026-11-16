# SERK Construction OS 3.0 — Claude Code implementation pack

1. Unzip the package. Place the files in the root of the intended construction OS repository (merge existing `CLAUDE.md` rather than overwrite it), or add them to a new empty repository if none exists.
2. Open that repository in Claude Code and select the strongest coding model available to your account. The requested Fable 5.2 model name is a user-specified target, not a verified availability claim.
3. Send the initial instruction below, or paste `MASTER_PROMPT.md` directly. The agent needs filesystem and terminal access to implement and run tests. Give Git access only if you authorize it. Never upload real secrets or customer data.

```text
Read CLAUDE.md, MASTER_PROMPT.md, spec/BIBLE.md, spec/module-register.csv, the original Master Engineering Bible and its diagrams. Execute the entire project as an evidence-driven engineering program. Start with repository inspection and M0, then implement M1 and successive integrated milestones. Do not stop at a plan or mockup. Persist checkpoint and requirement evidence after each slice. Do not deploy, merge, push or use real data without my authorization. Report actual verified results and remaining blockers.
```

On later sessions:

```text
Read CLAUDE.md and docs/engineering/execution-state.json, inspect Git and the last evidence, rerun any necessary baseline checks and continue the next incomplete milestone in MASTER_PROMPT.md. Do not repeat completed work or claim unverified functionality. Implement and verify the next coherent vertical slice.
```

## Contents
- `MASTER_PROMPT.md`: detailed engineering directive and implementation protocols.
- `CLAUDE.md`: concise persistent repo instructions.
- `spec/BIBLE.md`: text/table extraction with embedded diagram references, derived from the Word Bible.
- `spec/SERK_Construction_OS_3_Master_Engineering_Bible.docx`: original source document.
- `spec/figures/`: all embedded graphics from that document, not the dynamically displayed reference photographs from the earlier chat.
- `spec/module-register.csv`: grouped functional domain register from the Bible, to expand into individually testable requirements.

The pack is a specification and prompt, not working software, and cannot guarantee that a single model session completes a multi-release ERP program. No codebase has been inspected or implementation verified in this turn. Physical capacity, lawful retention and access controls apply even when the product imposes no arbitrary lifetime ceiling.
