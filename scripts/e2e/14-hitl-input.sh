#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
H=$(cat "$E2E_ROOT/hitl-handle")
say "orca terminal send — human rejects once, routing back to implement (feedback loop proof)"
REPORT='STATUS: failure
NEXT STEP: implement
SUMMARY:
  completed: Reviewed the hello world change manually.
  notCompleted: Program output lacks an exclamation mark.
  issuesDiscovered: Output text mismatch with request.
  verification: Human review in Orca terminal.
  notes: none
FEEDBACK FOR implement:
  reasonForNextStep: Output must say exactly hello world with exclamation.
  requiredActions: Change the program to print hello world! and re-commit.
  relevantContext: See parent ticket description.
  expectedResult: Running the program prints hello world!'
# Type the report into the HITL session then submit with Enter.
orca-ide terminal send --terminal "$H" --text "$REPORT"
sleep 1
orca-ide terminal send --terminal "$H" --key enter
beat 2
say "terminal read (report echoed)"
orca-ide terminal read --terminal "$H" 2>&1 | tail -25
beat 2
