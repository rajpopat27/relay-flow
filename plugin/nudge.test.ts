import { describe, expect, test } from "bun:test";

// The pure core owns agent correction behavior. HITL approval is owned by the
// native TUI entrypoint, so invalid/missing HITL output is silent here.

import { handleIdle, INVALID_REPORT_PROMPT } from "./index";

const valid = `
STATUS: success
NEXT STEP: end
SUMMARY:
COMPLETED: x
COMMITS: abc123
NOT COMPLETED: None
ISSUES DISCOVERED: None
VERIFICATION: x
NOTES: None
FEEDBACK:
REASON FOR NEXT STEP: None
REQUIRED ACTIONS: None
RELEVANT CONTEXT: None
EXPECTED RESULT: None
`;

function makeSession() {
  const calls: string[] = [];
  return {
    calls,
    sendPrompt: async (text: string) => { calls.push(text); },
  };
}

describe("agent node nudge via session API", () => {
  test("invalid output sends the nudge through the session API", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "ordinary prose", session });
    expect(session.calls).toEqual([INVALID_REPORT_PROMPT]);
    for (const label of ["STATUS:", "NEXT STEP:", "SUMMARY:", "COMPLETED:", "COMMITS:", "NOT COMPLETED:", "ISSUES DISCOVERED:", "VERIFICATION:", "NOTES:", "FEEDBACK:", "REASON FOR NEXT STEP:", "REQUIRED ACTIONS:", "RELEVANT CONTEXT:", "EXPECTED RESULT:"]) {
      expect(session.calls[0]).toContain(label);
    }
  });

  test("missing output sends the nudge", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "", session });
    expect(session.calls).toHaveLength(1);
  });

  test("valid output sends no nudge", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: valid, session });
    expect(session.calls).toHaveLength(0);
  });

  test("aborted response (no completed finish reason) is not nudged", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "", lastMessageCompleted: false, session });
    expect(session.calls).toHaveLength(0);
  });
});

describe("HITL node silence in the pure core", () => {
  test("invalid output sends no nudge and no report", async () => {
    const session = makeSession();
    const reports: any[] = [];
    await handleIdle({ nodeType: "hitl", lastMessage: "no contract", session, report: async (r: any) => { reports.push(r); } });
    expect(session.calls).toHaveLength(0);
    expect(reports).toHaveLength(0);
  });

  test("missing output stays silent", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "hitl", lastMessage: "", session });
    expect(session.calls).toHaveLength(0);
  });

  test("valid output is delivered only when the caller supplies an approved sink", async () => {
    const session = makeSession();
    const reports: any[] = [];
    await handleIdle({ nodeType: "hitl", lastMessage: valid, session, report: async (r: any) => { reports.push(r); } });
    expect(session.calls).toHaveLength(0);
    expect(reports).toHaveLength(1);
  });
});
