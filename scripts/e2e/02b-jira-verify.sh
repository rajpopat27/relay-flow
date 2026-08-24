#!/usr/bin/env bash
# Run AFTER the human confirms the component is set on the ticket.
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "acli jira workitem view $T (expect status To Do, component $JIRA_COMPONENT, assignee set)"
acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq '{key, status: .fields.status.name, components: ([.fields.components[]?.name] // []), assignee: .fields.assignee.emailAddress}'
beat 2
