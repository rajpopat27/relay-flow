import { describe, expect, test } from "bun:test";

// 3.39: plugin nudge policy per specs/structured-node-reporting "Agent
// nodes are nudged for invalid output" and "HITL nodes remain silent
// without valid output". The nudge is delivered through OpenCode's session
// API. Implemented by section 4.16.

import {
  handleIdle,
  HITL_APPROVAL_REQUIRED_PROMPT,
  HITL_APPROVED_INVALID_REPORT_PROMPT,
  INVALID_REPORT_PROMPT,
} from "./index";

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
    // OpenCode session API seam the plugin nudges through.
    sendPrompt: async (text: string) => { calls.push(text); },
  };
}

describe("agent node nudge via session API", () => {
  test("invalid output sends the nudge through the session API", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "ordinary prose", session });
    expect(session.calls.length).toBe(1);
    expect(session.calls[0]).toBe(INVALID_REPORT_PROMPT);
    for (const label of ["STATUS:", "NEXT STEP:", "SUMMARY:", "COMPLETED:", "COMMITS:", "NOT COMPLETED:", "ISSUES DISCOVERED:", "VERIFICATION:", "NOTES:", "FEEDBACK:", "REASON FOR NEXT STEP:", "REQUIRED ACTIONS:", "RELEVANT CONTEXT:", "EXPECTED RESULT:"]) {
      expect(session.calls[0]).toContain(label);
    }
  });

  test("missing output sends the nudge", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "", session });
    expect(session.calls.length).toBe(1);
  });

  test("valid output sends no nudge", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: valid, session });
    expect(session.calls.length).toBe(0);
  });

  test("aborted response (no completed finish reason) is not nudged", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "", lastMessageCompleted: false, session });
    expect(session.calls.length).toBe(0);
  });
});

describe("HITL node silence", () => {
  test("invalid output sends no nudge and no report", async () => {
    const session = makeSession();
    const reports: any[] = [];
    await handleIdle({ nodeType: "hitl", lastMessage: "no contract", session, report: async (r: any) => { reports.push(r); } });
    expect(session.calls.length).toBe(0);
    expect(reports.length).toBe(0);
  });

  test("missing output stays silent", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "hitl", lastMessage: "", session });
    expect(session.calls.length).toBe(0);
  });

  test("valid HITL output without Question authorization requests approval", async () => {
    const session = makeSession();
    const reports: any[] = [];
    await handleIdle({ nodeType: "hitl", lastMessage: valid, session, report: async (r: any) => { reports.push(r); } });
    expect(session.calls).toEqual([HITL_APPROVAL_REQUIRED_PROMPT]);
    expect(reports.length).toBe(0);
  });

  test("invalid HITL output after approval requests regeneration", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "hitl", lastMessage: "incomplete report", hitlAuthorized: true, hitlOutputSubmitted: true, session });
    expect(session.calls).toEqual([HITL_APPROVED_INVALID_REPORT_PROMPT]);
  });

  test("no HITL output after approval stays silent", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "hitl", lastMessage: "", hitlAuthorized: true, hitlOutputSubmitted: false, session });
    expect(session.calls).toEqual([]);
  });

  test("valid HITL output with Question authorization reports", async () => {
    const session = makeSession();
    const reports: any[] = [];
    await handleIdle({ nodeType: "hitl", lastMessage: valid, hitlAuthorized: true, session, report: async (r: any) => { reports.push(r); } });
    expect(session.calls.length).toBe(0);
    expect(reports.length).toBe(1);
  });
});
