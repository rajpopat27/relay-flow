import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, cpSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RelayFlowPlugin } from "./relay-flow";
import { RelayFlowProcessError, runRelayFlow } from "./transport";

const directories: string[] = [];
const originalEnv = { ...process.env };
const reportContractFixtures = JSON.parse(
  readFileSync(new URL("../testdata/report-contract.json", import.meta.url), "utf8"),
);

const validReport = reportContractFixtures.end.assistantText;

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

describe("OpenCode server plugin", () => {
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

  test("valid agent idle output registers, pins, and delivers the parsed report", async () => {
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
    const submitted = JSON.parse(actual[1].input);
    expect(submitted).toMatchObject({
      runId: "run-1", node: "implement", reportId: "session-idle:message-idle", report: { nextStep: "end" },
    });
    expect(submitted.report).toEqual(reportContractFixtures.end.envelope.report);
    expect(Object.keys(submitted.report).sort()).toEqual(["feedback", "nextStep", "status", "summary"]);
    expect(updates).toHaveLength(1);
  });

  test("HITL idle output is left for the TUI plugin and never uses Question", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    const prompts: string[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data: [{
        info: { id: "hitl-message", role: "assistant", time: { completed: Date.now() } },
        parts: [{ type: "text", text: validReport }],
      }] }),
      promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
    } } } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "hitl-session" } } } as any);
    await hooks.event!({ event: { type: "question.asked", properties: { id: "q1", sessionID: "hitl-session", questions: [] } } } as any);

    expect(calls(f.calls).map((call) => call.command)).toEqual(["runtime-register"]);
    expect(prompts).toHaveLength(0);
  });

  test("invalid HITL output remains silent and is not nudged", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl");
    const prompts: string[] = [];
    const hooks = await RelayFlowPlugin({ client: { session: {
      update: async () => {},
      messages: async () => ({ data: [{
        info: { id: "invalid-hitl", role: "assistant", time: { completed: Date.now() } },
        parts: [{ type: "text", text: "ordinary review notes" }],
      }] }),
      promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
    } } } as any);
    await hooks.event!({ event: { type: "session.idle", properties: { sessionID: "hitl-session" } } } as any);

    expect(calls(f.calls).filter((call) => call.command === "report")).toHaveLength(0);
    expect(prompts).toHaveLength(0);
    expect(readFileSync(join(f.directory, "plugin.log"), "utf8")).toContain("hitl output awaiting tui approval");
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
  test("installed server plugin shape persists session, pins title, and delivers report", () => {
    const f = fixture();
    const plugins = join(f.directory, "repo", ".opencode", "plugins");
    const lib = join(f.directory, "repo", ".opencode", "lib");
    mkdirSync(plugins, { recursive: true });
    mkdirSync(lib, { recursive: true });
    const installedEntry = join(plugins, "relay-flow.ts");
    const entry = readFileSync(join(import.meta.dir, "relay-flow.ts"), "utf8")
      .replaceAll('"./index"', '"../lib/relay-flow-core"')
      .replaceAll('"./transport"', '"../lib/transport"');
    writeFileSync(installedEntry, entry);
    cpSync(join(import.meta.dir, "index.ts"), join(lib, "relay-flow-core.ts"));
    cpSync(join(import.meta.dir, "transport.ts"), join(lib, "transport.ts"));
    const state = join(f.directory, "smoke-state.json");
    const launcher = join(f.directory, "smoke.ts");
    writeFileSync(launcher, `
import plugin from ${JSON.stringify(installedEntry)};
const updates = [];
const client = { session: {
  update: async (input) => { updates.push(input); },
  messages: async () => ({ data: [{ info: { id: "installed-message", role: "assistant", time: { completed: Date.now() } }, parts: [{ type: "text", text: ${JSON.stringify(validReport)} }] }] }),
} };
const hooks = await plugin({ client });
await hooks.event({ event: { type: "session.created", properties: { info: { id: "installed-session" } } } });
await hooks.event({ event: { type: "session.idle", properties: { sessionID: "installed-session" } } });
await Bun.write(${JSON.stringify(state)}, JSON.stringify({ updates }));
`);
    setEnvelope(f.directory);
    const smoke = Bun.spawnSync(["bun", launcher], { env: process.env });
    expect(smoke.exitCode).toBe(0);
    const actual = calls(f.calls);
    expect(actual.map((call) => call.command)).toEqual(["runtime-register", "report"]);
    expect(JSON.parse(actual[0].input).sessionId).toBe("installed-session");
    const persisted = JSON.parse(readFileSync(state, "utf8"));
    expect(persisted.updates).toContainEqual({ path: { id: "installed-session" }, body: { title: "TEST-1:implement" } });
    expect(JSON.parse(actual[1].input).report.status).toBe("success");
    expect(readFileSync(join(f.directory, "plugin.log"), "utf8")).toContain('sessionId="installed-session"');
  });
});
