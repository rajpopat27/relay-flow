#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
H=$(cat "$E2E_ROOT/hitl-handle")
say "orca terminal send — human rejects once, routing back to implement (feedback loop proof)"
REPORT='The human reviewer rejects this pass. Reply with exactly the report below and no other text.

STATUS: failure
NEXT STEP: implement

SUMMARY:
COMPLETED: Reviewed the hello world change manually.
COMMITS: None
NOT COMPLETED: Program output lacks an exclamation mark.
ISSUES DISCOVERED: Output text mismatch with request.
VERIFICATION: Human review in Orca terminal.
NOTES: None

FEEDBACK:
REASON FOR NEXT STEP: Output must say exactly hello world with exclamation.
REQUIRED ACTIONS: Change the program to print hello world! and re-commit.
RELEVANT CONTEXT: See parent ticket description.
EXPECTED RESULT: Running the program prints hello world!'
orca-ide terminal send --terminal "$H" --text "$REPORT" --enter --json >/dev/null
beat 2
say "terminal read (report echoed)"
orca-ide terminal read --terminal "$H" --limit 100 --json | jq '.result | {nextCursor, lines}'
beat 2
