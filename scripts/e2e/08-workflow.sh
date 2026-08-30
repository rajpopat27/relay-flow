#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
WF="$REPO/workflow.yaml"
say "write workflow.yaml (start -> implement -> verify -> pr-review(HITL) -> end)"
cat > "$WF" <<'YAML'
name: helloFlow
repos: [raj-test-repo]
cleanupRunnerOnEnd: true
taskConfig:
  filters:
    assignees: ["raj.popat@wolterskluwer.com"]
nodes:
  start:
    onSuccess:
      - target: implement
  implement:
    type: agent
    agent: build
    description: "Implement the parent ticket ask: create a hello world program in this repo and commit it."
    onSuccess:
      - target: verify
        when: implementation committed
    onFailure:
      - target: implement
        when: fixable error, retry once
      - target: end
        when: unrecoverable
  verify:
    type: agent
    agent: verifier
    description: "Verify the hello world program exists, runs, and is committed. Report success only if verified."
    onSuccess:
      - target: pr-review
        when: verified
    onFailure:
      - target: implement
        when: implementation wrong or missing
  pr-review:
    type: hitl
    agent: build
    description: "Human review of the hello world change. Approve (success) to finish, or reject (failure) with feedback for rework."
    onSuccess:
      - target: end
        when: approved
    onFailure:
      - target: verify
        when: needs re-verification only
      - target: implement
        when: needs rework
  end:
    taskConfig:
      transitionTo:
        parentStatus: Cancelled
YAML
cd "$REPO" && git add workflow.yaml && (git commit -qm "add relay-flow workflow" || echo "workflow.yaml already committed")
say "cat workflow.yaml"; cat "$WF"; beat
say "relay-flow workflow submit --file workflow.yaml"
rf workflow submit --file "$WF"
beat
say "relay-flow workflow list"
[ "$(rf workflow list)" = "$WORKFLOW_NAME" ] || fail "workflow list mismatch"
STORED=$(rf workflow get --name "$WORKFLOW_NAME")
echo "$STORED" | jq -e --arg n "$WORKFLOW_NAME" --arg r "$REPO_NAME" '
  .name == $n and .repos == [$r] and .cleanupRunnerOnEnd == true and
  (.nodes | keys | sort) == (["end","implement","pr-review","start","verify"] | sort)' >/dev/null || fail "stored workflow mismatch"
beat 2
