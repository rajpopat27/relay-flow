#!/usr/bin/env bash
# Run AFTER the human confirms the component is set on the ticket.
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "acli jira workitem view $T (expect status To Do, component $JIRA_COMPONENT, assignee set)"
VIEW=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json)
echo "$VIEW" | jq '{key, status: .fields.status.name, components: ([.fields.components[]?.name] // []), assignee: .fields.assignee.emailAddress, labels: .fields.labels, subtasks: .fields.subtasks}'
echo "$VIEW" | jq -e --arg key "$T" --arg component "$JIRA_COMPONENT" --arg assignee "raj.popat@wolterskluwer.com" '
  (.key == $key) and
  (.fields.status.name == "To Do") and
  ([.fields.components[]?.name] | index($component) != null) and
  (.fields.assignee.emailAddress == $assignee) and
  ((.fields.labels // []) | length == 0) and
  ((.fields.subtasks // []) | length == 0)' >/dev/null || fail "Jira ticket fields do not match the clean-run contract"
beat 2
