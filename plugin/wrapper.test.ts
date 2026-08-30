import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, cpSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RelayFlowPlugin, RelayFlowProcessError, runRelayFlow } from "./relay-flow";

const directories: string[] = [];
const originalEnv = { ...process.env };

afterEach(() => {
  process.env = { ...originalEnv };
  for (const directory of directories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

function fixture(exitCode = 0, stderr = "") {
  const directory = mkdtempSync(join(tmpdir(), "relay-flow-plugin-"));
  directories.push(directory);
  const calls = join(directory, "calls.jsonl");
  const executable = join(directory, "relay-flow");
  writeFileSync(executable, `#!/usr/bin/env bun
import { appendFileSync } from "node:fs";
const input = await Bun.stdin.text();
appendFileSync(process.env.RELAY_FLOW_TEST_CALLS, JSON.stringify({ command: process.argv[2], input }) + "\\n");
process.stderr.write(${JSON.stringify(stderr)});
process.exit(${exitCode});
`);
  chmodSync(executable, 0o755);
  process.env.PATH = `${directory}:${originalEnv.PATH ?? ""}`;
  process.env.RELAY_FLOW_TEST_CALLS = calls;
  return { directory, calls };
}

function calls(path: string): Array<{ command: string; input: string }> {
  try {
    return readFileSync(path, "utf8").trim().split("\n").filter(Boolean).map(JSON.parse);
  } catch {
    return [];
  }
}

function setEnvelope(home?: string, nodeType: "agent" | "hitl" = "agent") {
  Object.assign(process.env, {
    RELAY_FLOW_HOME: home,
    RELAY_FLOW_RUN_ID: "run-1",
    RELAY_FLOW_TICKET: "TEST-1",
    RELAY_FLOW_NODE: "implement",
    RELAY_FLOW_NODE_TYPE: nodeType,
    RELAY_FLOW_NUDGE_PROMPT: "emit the report",
  });
}

const validReport = `STATUS: success
NEXT STEP: end
SUMMARY:
COMPLETED: implemented
COMMITS: abc123
NOT COMPLETED: None
ISSUES DISCOVERED: None
VERIFICATION: passed
NOTES: None
FEEDBACK:
REASON FOR NEXT STEP: None
REQUIRED ACTIONS: None
RELEVANT CONTEXT: None
EXPECTED RESULT: None`;

function assistant(id: string, text: string, parts?: any[]) {
  return {
    info: {
      id,
      sessionID: "session-hitl",
      role: "assistant",
      parentID: `user-${id}`,
      time: { created: 1, completed: 2 },
    },
    parts: parts ?? [{ id: `part-${id}`, messageID: id, sessionID: "session-hitl", type: "text", text }],
  };
}

function questionAsked(sessionID = "session-hitl", requestID = "question-1", tool?: { messageID: string; callID: string }) {
  return { type: "question.asked", properties: {
    id: requestID,
    sessionID,
    questions: [{
      question: `Approve this report?\n\n${validReport}`,
      header: "Decision",
      options: [
        { label: "Approve", description: "Submit this report" },
        { label: "Reject", description: "Continue the review" },
      ],
      custom: true,
    }],
    tool,
  } };
}

function questionReplied(sessionID = "session-hitl", requestID = "question-1", answer = "Approve") {
  return { type: "question.replied", properties: {
    sessionID,
    requestID,
    answers: [[answer]],
  } };
}

describe("spawn transport", () => {
  test("executes argv-only commands and writes exact JSON to stdin", async () => {
    const f = fixture();
    const registration = JSON.stringify({ sessionId: "session-1" });
    const report = JSON.stringify({ runId: "run-1", report: { status: "success" } });
    await runRelayFlow("runtime-register", registration);
    await runRelayFlow("report", report);
    expect(calls(f.calls)).toEqual([
      { command: "runtime-register", input: registration },
      { command: "report", input: report },
    ]);
  });

  test("captures non-zero exit code and stderr", async () => {
    fixture(7, "server unavailable");
    try {
      await runRelayFlow("runtime-register", "{}");
      throw new Error("expected transport failure");
    } catch (error) {
      expect(error).toBeInstanceOf(RelayFlowProcessError);
      expect((error as RelayFlowProcessError).exitCode).toBe(7);
      expect((error as RelayFlowProcessError).stderr).toBe("server unavailable");
    }
  });
});

describe("OpenCode event wrapper", () => {
  test("session events immediately register and pin the title", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const updates: unknown[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async (input: unknown) => { updates.push(input); },
    } } } as any);
    await hooks.event!({ event: { type: "session.created", properties: { info: { id: "session-created" } } } } as any);

    expect(calls(f.calls).map((call) => ({ command: call.command, input: JSON.parse(call.input) }))).toEqual([{
      command: "runtime-register",
      input: { runId: "run-1", node: "implement", sessionId: "session-created" },
    }]);
    expect(updates).toEqual([{ path: { id: "session-created" }, body: { title: "TEST-1:implement" } }]);
    expect(readFileSync(join(f.directory, "plugin.log"), "utf8")).toContain('msg="runtime registration succeeded"');
  });

  test("valid idle output registers, pins, and delivers the parsed report", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const updates: unknown[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async (input: unknown) => { updates.push(input); },
      messages: async () => ({ data: [{
        info: { id: "message-idle", role: "assistant", time: { completed: Date.now() } },
        parts: [{ type: "text", text: validReport }],
      }] }),
    } } } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-idle" } } } as any);

    const actual = calls(f.calls);
    expect(actual.map((call) => call.command)).toEqual(["runtime-register", "report"]);
    expect(JSON.parse(actual[1].input)).toMatchObject({
      runId: "run-1", node: "implement", reportId: "session-idle:message-idle", report: { nextStep: "end" },
    });
    expect(updates).toHaveLength(1);
  });

  test("valid HITL report without a question requests approval once", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    const prompts: string[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data: [assistant("report-1", validReport)] }),
      promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
    } } } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).map((call) => call.command)).toEqual(["runtime-register"]);
    expect(prompts).toHaveLength(1);
  });

  test("asked-but-unanswered and rejected direct reports request approval", async () => {
    for (const rejected of [false, true]) {
      const f = fixture();
      setEnvelope(f.directory, "hitl");
      let data = [assistant("pre-question", validReport)];
      const prompts: string[] = [];
      const hooks = await RelayFlowPlugin({ client: { session: {
        update: async () => {},
        messages: async () => ({ data }),
        promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
      } } } as any);
      await hooks.event!({ event: questionAsked() } as any);
      data = [assistant("post-question", validReport)];
      if (rejected) {
        await hooks.event!({ event: { type: "question.rejected", properties: {
          sessionID: "session-hitl", requestID: "question-1",
        } } } as any);
      }
      await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
      expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
      expect(prompts).toHaveLength(rejected ? 1 : 0);
    }
  });

  test("matching Approve reply authorizes one new HITL report", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    let data = [assistant("pre-question", validReport)];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
    } } } as any);
    await hooks.event!({ event: questionAsked() } as any);
    await hooks.event!({ event: questionReplied() } as any);
    data = [assistant("post-question", validReport)];
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(1);
  });

  test("matching Reject reply does not authorize HITL", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    let data = [assistant("pre-question", validReport)];
    const prompts: string[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
      promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
    } } } as any);
    await hooks.event!({ event: questionAsked() } as any);
    await hooks.event!({ event: questionReplied("session-hitl", "question-1", "Reject") } as any);
    data = [assistant("post-question", validReport)];
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
    expect(prompts).toHaveLength(1);
  });

  test("reject then direct report requires a new approved Question", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    let data = [assistant("proposal-1", validReport)];
    const prompts: string[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
      promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
    } } } as any);

    await hooks.event!({ event: questionAsked() } as any);
    await hooks.event!({ event: questionReplied("session-hitl", "question-1", "Reject") } as any);
    data = [assistant("direct-after-reject", validReport)];
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(prompts).toHaveLength(1);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);

    await hooks.event!({ event: questionAsked("session-hitl", "question-2") } as any);
    await hooks.event!({ event: questionReplied("session-hitl", "question-2") } as any);
    data = [assistant("approved-report", validReport)];
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(1);
  });

  test("report generated before the matching reply stays stale", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    let data = [assistant("pre-question", "review complete")];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
    } } } as any);
    await hooks.event!({ event: questionAsked() } as any);
    data = [assistant("too-early", validReport)];
    await hooks.event!({ event: questionReplied() } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
  });

  test("approved invalid output requests regeneration", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    let data = [assistant("proposal", validReport)];
    const prompts: string[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
      promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
    } } } as any);
    await hooks.event!({ event: questionAsked() } as any);
    await hooks.event!({ event: questionReplied() } as any);
    data = [assistant("invalid-after-approval", "review approved")];
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(prompts).toHaveLength(1);
    expect(prompts[0]).toContain("approved by the user did not match");
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
  });

  test("wrong-session and wrong-request replies do not authorize HITL", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    let data = [assistant("pre-question", validReport)];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
    } } } as any);
    await hooks.event!({ event: questionAsked() } as any);
    data = [assistant("post-question", validReport)];
    await hooks.event!({ event: questionReplied("another-session") } as any);
    await hooks.event!({ event: questionReplied("session-hitl", "another-question") } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
  });

  test("pre-question report stays stale after reply", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    const stale = assistant("same-message", validReport);
    let data = [stale];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
    } } } as any);
    await hooks.event!({ event: questionAsked() } as any);
    data = [stale];
    await hooks.event!({ event: questionReplied() } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
  });

  test("Question approval authorizes only the next assistant message", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    let data = [assistant("review-message", validReport, [
      { id: "pre", messageID: "review-message", sessionID: "session-hitl", type: "text", text: validReport },
      { id: "tool", messageID: "review-message", sessionID: "session-hitl", type: "tool", callID: "call-1", tool: "question", state: { status: "running", input: {}, time: { start: 1 } } },
    ])];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data }),
    } } } as any);
    await hooks.event!({ event: questionAsked("session-hitl", "question-1", { messageID: "review-message", callID: "call-1" }) } as any);
    await hooks.event!({ event: questionReplied() } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
    data = [assistant("review-message", validReport, [
      { id: "pre", messageID: "review-message", sessionID: "session-hitl", type: "text", text: validReport },
      { id: "tool", messageID: "review-message", sessionID: "session-hitl", type: "tool", callID: "call-1", tool: "question", state: { status: "completed", input: {}, output: "answered", title: "Question", metadata: {}, time: { start: 1, end: 2 } } },
    ]), assistant("post-approval", validReport)];
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "session-hitl" } } } as any);
    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(1);
  });

  test("event failures are logged with actionable identity and never escape", async () => {
    const f = fixture(9, "registration refused");
    setEnvelope(f.directory);
    const hooks = await RelayFlowPlugin({ client: { session: {} } } as any);
    await expect(hooks.event!({ event: {
      type: "session.created", properties: { info: { id: "session-failed" } },
    } } as any)).resolves.toBeUndefined();
    const log = readFileSync(join(f.directory, "plugin.log"), "utf8");
    for (const expected of [
      'operation="runtime-register"', 'runId="run-1"', 'node="implement"',
      'sessionId="session-failed"', 'exitCode="9"',
      'stderr="registration refused"',
    ]) expect(log).toContain(expected);
  });
});

describe("installed repo-local plugin smoke", () => {
  test("installed plugin shape persists session, pins title, and delivers report", () => {
    const f = fixture();
    const plugins = join(f.directory, "repo", ".opencode", "plugins");
    const lib = join(f.directory, "repo", ".opencode", "lib");
    mkdirSync(plugins, { recursive: true });
    mkdirSync(lib, { recursive: true });
    const installedEntry = join(plugins, "relay-flow.ts");
    const entry = readFileSync(join(import.meta.dir, "relay-flow.ts"), "utf8")
      .replaceAll('"./index"', '"../lib/relay-flow-core"');
    writeFileSync(installedEntry, entry);
    cpSync(join(import.meta.dir, "index.ts"), join(lib, "relay-flow-core.ts"));
    const state = join(f.directory, "smoke-state.json");
    const launcher = join(f.directory, "smoke.ts");
    writeFileSync(launcher, `
import plugin from ${JSON.stringify(installedEntry)};
const updates = [];
const database = { session_id: null };
const client = { session: {
  update: async (input) => { updates.push(input); },
  messages: async () => ({ data: [{ info: { id: "installed-message", role: "assistant", time: { completed: Date.now() } }, parts: [{ type: "text", text: ${JSON.stringify(validReport)} }] }] }),
} };
const hooks = await plugin({ client });
await hooks.event({ event: { type: "session.created", properties: { info: { id: "installed-session" } } } });
await hooks.event({ event: { type: "session.idle", properties: { sessionID: "installed-session" } } });
const registration = (await Bun.file(process.env.RELAY_FLOW_TEST_CALLS).text()).trim().split("\\n").map(JSON.parse).find((call) => call.command === "runtime-register");
database.session_id = JSON.parse(registration.input).sessionId;
await Bun.write(${JSON.stringify(state)}, JSON.stringify({ updates, database }));
`);
    setEnvelope(f.directory);
    const smoke = Bun.spawnSync(["bun", launcher], { env: process.env });
    expect(smoke.exitCode).toBe(0);
    const actual = calls(f.calls);
    expect(actual.map((call) => call.command)).toEqual(["runtime-register", "report"]);
    expect(JSON.parse(actual[0].input).sessionId).toBe("installed-session");
    const persisted = JSON.parse(readFileSync(state, "utf8"));
    expect(persisted.database.session_id).toBe("installed-session");
    expect(persisted.database.session_id).not.toBe("");
    expect(persisted.updates).toContainEqual({ path: { id: "installed-session" }, body: { title: "TEST-1:implement" } });
    expect(JSON.parse(actual[1].input).report.status).toBe("success");
    expect(readFileSync(join(f.directory, "plugin.log"), "utf8")).toContain('sessionId="installed-session"');
  });
});
