import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import relayFlowPi from "./pi";

const originalEnv = { ...process.env };
const temporaryDirectories: string[] = [];
const reportContractFixtures = JSON.parse(
  readFileSync(new URL("../testdata/report-contract.json", import.meta.url), "utf8"),
);
const validReport = reportContractFixtures.end.assistantText as string;

afterEach(() => {
  process.env = { ...originalEnv };
  for (const directory of temporaryDirectories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

type PiEvent = "session_start" | "agent_settled";
type PiHandler = (event: unknown, context: PiContext) => Promise<void> | void;

type PiContext = {
  ui: {
    select(title: string, options: string[], opts?: unknown): Promise<string | undefined>;
  };
  mode: "tui";
  hasUI: boolean;
  cwd: string;
  sessionManager: {
    getSessionId(): string;
    getBranch(): PiSessionEntry[];
  };
};

type PiSessionEntry = {
  type: "message" | "tool_result";
  id: string;
  parentId: string | null;
  timestamp: string;
  message?: {
    role: "user" | "assistant";
    content: unknown;
    stopReason?: string;
  };
};

type PiFake = {
  handlers: Map<PiEvent, PiHandler>;
  names: string[];
  messages: string[];
  getSessionName(): string | undefined;
  setSessionName(name: string): void;
  sendUserMessage(content: string): void;
};

function makePi(initialName?: string): PiFake {
  let sessionName = initialName;
  const handlers = new Map<PiEvent, PiHandler>();
  const names: string[] = [];
  const messages: string[] = [];
  return {
    handlers,
    names,
    messages,
    getSessionName: () => sessionName,
    setSessionName: (name: string) => {
      sessionName = name;
      names.push(name);
    },
    sendUserMessage: (content: string) => {
      messages.push(content);
    },
    on: (event: string, handler: PiHandler) => {
      if (event !== "session_start" && event !== "agent_settled") {
        throw new Error(`unsupported Pi event ${event}`);
      }
      handlers.set(event, handler);
    },
  } as PiFake;
}

function makeContext(sessionId: string, branch: PiSessionEntry[]): PiContext {
  return {
    ui: {
      select: async () => undefined,
    },
    mode: "tui",
    hasUI: true,
    cwd: "/srv/payments/.worktrees/PAY-101",
    sessionManager: {
      getSessionId: () => sessionId,
      getBranch: () => branch,
    },
  };
}

function sessionStart(): { type: "session_start"; reason: "startup" } {
  return { type: "session_start", reason: "startup" };
}

function settled(): { type: "agent_settled" } {
  return { type: "agent_settled" };
}

function assistantEntry(
  id: string,
  content: unknown,
  stopReason = "stop",
): PiSessionEntry {
  return {
    type: "message",
    id,
    parentId: "parent-entry",
    timestamp: "2026-08-31T00:00:00.000Z",
    message: {
      role: "assistant",
      content,
      stopReason,
    },
  };
}

function configureMetadata(home: string, runId: string, nodeType: "agent" | "hitl" = "agent") {
  Object.assign(process.env, {
    RELAY_FLOW_HOME: home,
    RELAY_FLOW_RUN_ID: runId,
    RELAY_FLOW_WORKFLOW: "basicFlow",
    RELAY_FLOW_REPO: "payments",
    RELAY_FLOW_TICKET: "PAY-101",
    RELAY_FLOW_NODE: "implement",
    RELAY_FLOW_NODE_TYPE: nodeType,
    RELAY_FLOW_NUDGE_PROMPT: "emit the complete report",
    RELAY_FLOW_NEXT_STEPS_JSON: JSON.stringify([
      { target: "review", when: "implementation complete" },
      { target: "end", when: "approved" },
    ]),
  });
}

type RelayFixture = {
  directory: string;
  calls: string;
  failMarker: string;
};

function relayFixture(failFirst?: "runtime-register" | "report"): RelayFixture {
  const directory = mkdtempSync(join(tmpdir(), "relay-flow-pi-extension-"));
  temporaryDirectories.push(directory);
  const calls = join(directory, "calls.jsonl");
  const failMarker = join(directory, "first-failure");
  const executable = join(directory, "relay-flow");
  const script = `#!/usr/bin/env bun
import { appendFileSync, existsSync, writeFileSync } from "node:fs";

const command = process.argv[2];
if (process.argv.length !== 3 || (command !== "runtime-register" && command !== "report")) {
  process.exit(2);
}
const input = await Bun.stdin.text();
let payload;
try {
  payload = JSON.parse(input);
} catch {
  process.exit(3);
}
if (!payload || typeof payload !== "object" || Array.isArray(payload)) process.exit(4);
appendFileSync(process.env.RELAY_FLOW_TEST_CALLS, JSON.stringify({ command, input }) + "\\n");
if (${JSON.stringify(failFirst)} === command && !existsSync(process.env.RELAY_FLOW_FAIL_MARKER)) {
  writeFileSync(process.env.RELAY_FLOW_FAIL_MARKER, "failed");
  process.exit(7);
}
`;
  writeFileSync(executable, script);
  chmodSync(executable, 0o700);
  process.env.PATH = `${directory}:${originalEnv.PATH ?? ""}`;
  process.env.RELAY_FLOW_TEST_CALLS = calls;
  process.env.RELAY_FLOW_FAIL_MARKER = failMarker;
  configureMetadata(directory, `run-${directory.split("/").pop()}`);
  return { directory, calls, failMarker };
}

function calls(path: string): Array<{ command: string; input: string }> {
  if (!existsSync(path)) return [];
  const text = readFileSync(path, "utf8").trim();
  if (!text) return [];
  return text.split("\n").map((line) => JSON.parse(line));
}

function handler(pi: PiFake, event: PiEvent): PiHandler {
  const selected = pi.handlers.get(event);
  if (!selected) throw new Error(`Pi did not register ${event}`);
  return selected;
}

describe("Pi extension contract", () => {
  test("registers session_start and pins the stable session name", async () => {
    const fixture = relayFixture();
    const pi = makePi("old-name");
    const context = makeContext("pi-session-1", []);

    relayFlowPi(pi as never);
    expect([...pi.handlers.keys()].sort()).toEqual(["agent_settled", "session_start"]);
    await handler(pi, "session_start")(sessionStart(), context);
    await handler(pi, "session_start")(sessionStart(), context);

    const actual = calls(fixture.calls);
    expect(actual).toHaveLength(1);
    expect(actual[0].command).toBe("runtime-register");
    expect(JSON.parse(actual[0].input)).toEqual({
      runId: process.env.RELAY_FLOW_RUN_ID,
      node: "implement",
      sessionId: "pi-session-1",
    });
    expect(pi.names).toEqual(["PAY-101:implement"]);
    expect(pi.getSessionName()).toBe("PAY-101:implement");
  });

  test("retries registration on a later agent_settled event after failure", async () => {
    const fixture = relayFixture("runtime-register");
    const pi = makePi();
    const context = makeContext("pi-session-retry", []);

    relayFlowPi(pi as never);
    await expect(handler(pi, "session_start")(sessionStart(), context)).resolves.toBeUndefined();
    expect(calls(fixture.calls)).toHaveLength(1);

    await handler(pi, "agent_settled")(settled(), context);
    const actual = calls(fixture.calls);
    expect(actual.map((call) => call.command)).toEqual(["runtime-register", "runtime-register"]);
    expect(actual[0].input).toBe(actual[1].input);
    expect(readFileSync(join(fixture.directory, "plugin.log"), "utf8")).toContain(
      'msg="runtime registration failed"',
    );
  });

  test("selects the latest completed assistant on the active branch and uses its entry ID", async () => {
    const fixture = relayFixture();
    const pi = makePi();
    const older = assistantEntry("older-assistant", "ordinary prose");
    const latestText = validReport;
    const latest = assistantEntry("latest-assistant-entry", [
      { type: "thinking", thinking: "not part of the report" },
      { type: "text", text: latestText.slice(0, 40) },
      { type: "toolCall", name: "ignored-tool" },
      { type: "text", text: latestText.slice(40) },
    ]);
    const branch: PiSessionEntry[] = [
      {
        type: "message",
        id: "user-entry",
        parentId: null,
        timestamp: "2026-08-31T00:00:00.000Z",
        message: { role: "user", content: [{ type: "text", text: "work" }] },
      },
      older,
      { type: "tool_result", id: "tool-entry", parentId: older.id, timestamp: older.timestamp },
      latest,
    ];
    const context = makeContext("pi-session-report", branch);

    relayFlowPi(pi as never);
    await handler(pi, "session_start")(sessionStart(), context);
    await handler(pi, "agent_settled")(settled(), context);

    const actual = calls(fixture.calls);
    expect(actual.map((call) => call.command)).toEqual(["runtime-register", "report"]);
    const submitted = JSON.parse(actual[1].input);
    expect(submitted).toMatchObject({
      runId: process.env.RELAY_FLOW_RUN_ID,
      node: "implement",
      reportId: "pi-session-report:latest-assistant-entry",
      report: { status: "success", nextStep: "end" },
    });
    expect(submitted.report.summary.completed).toBe("Implemented the report endpoint");
    expect(submitted.report.feedback.expectedResult).toBe("None");
  });

  test("ignores aborted and error assistant entries", async () => {
    for (const stopReason of ["aborted", "error"]) {
      const fixture = relayFixture();
      const pi = makePi();
      const context = makeContext(
        `pi-session-${stopReason}`,
        [assistantEntry(`${stopReason}-assistant-entry`, [{ type: "text", text: validReport }], stopReason),
        ],
      );

      relayFlowPi(pi as never);
      await handler(pi, "session_start")(sessionStart(), context);
      await handler(pi, "agent_settled")(settled(), context);

      expect(calls(fixture.calls).map((call) => call.command)).toEqual(["runtime-register"]);
      expect(pi.messages).toEqual([]);
    }
  });
});
