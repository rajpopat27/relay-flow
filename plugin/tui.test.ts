import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RelayFlowTuiPlugin, formatReportPreview } from "./tui";

const directories: string[] = [];
const disposers: Array<() => void> = [];
const originalEnv = { ...process.env };
const reportContractFixtures = JSON.parse(
  readFileSync(new URL("../testdata/report-contract.json", import.meta.url), "utf8"),
);
const validReportText = reportContractFixtures.end.assistantText;

afterEach(() => {
  process.env = { ...originalEnv };
  for (const dispose of disposers.splice(0)) dispose();
  for (const directory of directories.splice(0)) rmSync(directory, { recursive: true, force: true });
});

function fixture() {
  const directory = mkdtempSync(join(tmpdir(), "relay-flow-tui-plugin-"));
  directories.push(directory);
  const calls = join(directory, "calls.jsonl");
  const executable = join(directory, "relay-flow");
  writeFileSync(executable, `#!/usr/bin/env bun
import { appendFileSync } from "node:fs";
const input = await Bun.stdin.text();
appendFileSync(process.env.RELAY_FLOW_TEST_CALLS, JSON.stringify({ command: process.argv[2], input }) + "\\n");
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

function setEnvelope(home: string, nodeType: "agent" | "hitl" = "hitl") {
  Object.assign(process.env, {
    RELAY_FLOW_HOME: home,
    RELAY_FLOW_RUN_ID: "run-1",
    RELAY_FLOW_TICKET: "TEST-1",
    RELAY_FLOW_NODE: "review",
    RELAY_FLOW_NODE_TYPE: nodeType,
  });
}

function assistant(id: string, text: string): any {
  return {
    id,
    sessionID: "session-hitl",
    role: "assistant",
    parentID: `user-${id}`,
    time: { created: 1, completed: 2 },
    parts: [{ id: `part-${id}`, messageID: id, sessionID: "session-hitl", type: "text", text }],
  };
}

function makeAPI(initial: any) {
  let data = initial;
  let idle: ((event: any) => void) | undefined;
  let message: ((event: any) => void) | undefined;
  let rendered: (() => unknown) | undefined;
  const replaces: Array<unknown> = [];
  const toasts: Array<unknown> = [];
  const cleanup: Array<() => void> = [];
  const api: any = {
    route: { current: { name: "session", params: { sessionID: "session-hitl" } } },
    state: {
      session: {
        messages: () => data.map((item: any) => item),
      },
      part: (messageID: string) => data.find((item: any) => item.id === messageID)?.parts ?? [],
    },
    event: {
      on: (type: string, handler: (event: any) => void) => {
        if (type === "session.idle") idle = handler;
        if (type === "message.updated") message = handler;
        return () => {};
      },
    },
    ui: {
      dialog: {
        replace: (render: () => unknown) => {
          rendered = render;
          replaces.push(render);
        },
        clear: () => {},
        setSize: () => {},
      },
      DialogSelect: (props: unknown) => props,
      toast: (toast: unknown) => { toasts.push(toast); },
    },
    lifecycle: {
      onDispose: (fn: () => void) => {
        cleanup.push(fn);
        disposers.push(fn);
        return () => {};
      },
      signal: new AbortController().signal,
    },
  };
  return {
    api,
    setData(next: any[]) { data = next; },
    triggerIdle() { idle?.({ data: { sessionID: "session-hitl" } }); },
    triggerMessage(next: any) { message?.({ data: { sessionID: "session-hitl", info: next } }); },
    getRendered() { return rendered?.() as any; },
    replaces,
    toasts,
    cleanup,
  };
}

async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 75));
}

describe("report preview", () => {
  test("contains the complete parsed report", () => {
    const parsed = reportContractFixtures.end.envelope.report;
    const preview = formatReportPreview(parsed);
    expect(preview).toContain("STATUS: success");
    expect(preview).toContain("NEXT STEP: end");
    expect(preview).toContain("COMMITS: abc123");
    expect(preview).toContain("EXPECTED RESULT: None");
  });
});

describe("OpenCode native HITL TUI plugin", () => {
  test("valid report opens Approve/Reject dialog and approval delivers one report", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("report-1", validReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();

    const dialog = harness.getRendered();
    expect(dialog.title).toBe("Relay-flow report approval: TEST-1:review");
    expect(dialog.options.map((option: any) => option.title)).toEqual(["Approve", "Reject"]);
    expect(dialog.options[0].description).toContain("STATUS: success");
    expect(dialog.options[0].description).toContain("Deliver this exact report");

    dialog.onSelect({ value: "approve" });
    await settle();
    const actual = calls(f.calls);
    expect(actual.map((call) => call.command)).toEqual(["report"]);
    expect(JSON.parse(actual[0].input)).toMatchObject({
      runId: "run-1",
      node: "review",
      reportId: "session-hitl:report-1",
      report: { nextStep: "end", status: "success" },
    });
    expect(harness.toasts).toContainEqual({ variant: "success", message: "Relay-flow report processed" });
  });

  test("invalid or missing report stays silent", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("invalid", "ordinary review notes")]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    harness.triggerMessage(assistant("invalid", "ordinary review notes"));
    await settle();

    expect(harness.replaces).toHaveLength(0);
    expect(calls(f.calls)).toHaveLength(0);
    expect(harness.toasts).toHaveLength(0);
  });

  test("duplicate idle/message events do not open a second dialog", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("report-1", validReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    harness.triggerIdle();
    harness.triggerMessage(assistant("report-1", validReportText));
    expect(harness.replaces).toHaveLength(1);

    harness.getRendered().onSelect({ value: "reject" });
    await settle();
    expect(calls(f.calls)).toHaveLength(0);
    expect(harness.toasts).toContainEqual({ variant: "warning", message: "Relay-flow report rejected" });
  });

  test("agent sessions are ignored by the TUI entrypoint", async () => {
    const f = fixture();
    setEnvelope(f.directory, "agent");
    const harness = makeAPI([assistant("agent-report", validReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    await settle();

    expect(harness.replaces).toHaveLength(0);
    expect(calls(f.calls)).toHaveLength(0);
  });
});
