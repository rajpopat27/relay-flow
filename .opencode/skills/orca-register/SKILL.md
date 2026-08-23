---
name: orca-register
description: Create and bind this terminal's own Orca orchestration Run and report the Run ID. Use when an orchestrated relay-flow agent (foreman, implementer, or verifier) starts up and must register itself before messaging.
---

# Orca Self-Register

Run these three commands yourself, in this order, in YOUR OWN terminal. Read the Run ID off the printed JSON (`result.run.id`, the `run_...` string — NOT the top-level `id`). Never use `$(...)` or pipes to capture it.

Replace `<role>` with your role: `foreman relay-flow rewrite`, `implementer`, or `verifier`.

1. `orca-ide orchestration run-create --objective "<role>" --json`
2. `orca-ide orchestration run-use --id run_... --json`   (type the literal id you just read)
3. `orca-ide orchestration run-show --id run_... --json`   → confirm `coordinator_handle` is YOUR terminal handle, not null. If null, STOP and report.

Then:
- If you are the **foreman**: record the run id as MY_RUN and continue your Step 2.
- If you are an **implementer**: send `orca-ide orchestration send --to run:<FOREMAN_RUN> --type status --subject "IMPLEMENTER-RUN" --body "MY_RUN=run_..." --json`, then idle.
- If you are a **verifier**: same with subject `VERIFIER-RUN`, then idle.

Use `orca-ide` for all orca commands. No backticks or command substitution anywhere.
