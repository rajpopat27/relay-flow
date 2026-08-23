import { describe, expect, test } from "bun:test";

// 3.39: plugin nudge policy per specs/structured-node-reporting "Agent
// nodes are nudged for invalid output" and "HITL nodes remain silent
// without valid output". The nudge is delivered through OpenCode's session
// API. Implemented by section 4.16.

import { handleIdle } from "./index";

const nudge = "Please emit the report contract.";
const valid = `
STATUS: success
NEXT STEP: end
SUMMARY:
COMPLETED: x
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
    await handleIdle({ nodeType: "agent", lastMessage: "ordinary prose", nudgePrompt: nudge, session });
    expect(session.calls.length).toBe(1);
    expect(session.calls[0]).toBe(nudge);
  });

  test("missing output sends the nudge", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "", nudgePrompt: nudge, session });
    expect(session.calls.length).toBe(1);
  });

  test("valid output sends no nudge", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: valid, nudgePrompt: nudge, session });
    expect(session.calls.length).toBe(0);
  });

  test("aborted response (no completed finish reason) is not nudged", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "agent", lastMessage: "", lastMessageCompleted: false, nudgePrompt: nudge, session });
    expect(session.calls.length).toBe(0);
  });
});

describe("HITL node silence", () => {
  test("invalid output sends no nudge and no report", async () => {
    const session = makeSession();
    const reports: any[] = [];
    await handleIdle({ nodeType: "hitl", lastMessage: "no contract", nudgePrompt: nudge, session, report: async (r: any) => { reports.push(r); } });
    expect(session.calls.length).toBe(0);
    expect(reports.length).toBe(0);
  });

  test("missing output stays silent", async () => {
    const session = makeSession();
    await handleIdle({ nodeType: "hitl", lastMessage: "", nudgePrompt: nudge, session });
    expect(session.calls.length).toBe(0);
  });

  test("valid HITL output reports normally", async () => {
    const session = makeSession();
    const reports: any[] = [];
    await handleIdle({ nodeType: "hitl", lastMessage: valid, nudgePrompt: nudge, session, report: async (r: any) => { reports.push(r); } });
    expect(session.calls.length).toBe(0);
    expect(reports.length).toBe(1);
  });
});

