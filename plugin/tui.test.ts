import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RelayFlowTuiPlugin } from "./tui";

const directories: string[] = [];
const disposers: Array<() => void> = [];
const originalEnv = { ...process.env };
const reportContractFixtures = JSON.parse(
  readFileSync(new URL("../testdata/report-contract.json", import.meta.url), "utf8"),
);
const validReportText = reportContractFixtures.end.assistantText;
const validFailureReportText = validReportText
  .replace("STATUS: success", "STATUS: failure")
  .replace("NEXT STEP: end", "NEXT STEP: review");
const failureToEndReportText = validReportText.replace("STATUS: success", "STATUS: failure");
const serverValidationMessage = "NEXT STEP is end, so FEEDBACK must be exactly None.";

afterEach(() => {
  process.env = { ...originalEnv };
  for (const dispose of disposers.splice(0)) dispose();
  for (const directory of directories.splice(0)) rmSync(directory, { recursive: true, force: true });
});

function fixture(rejectFirstReport = false) {
  const directory = mkdtempSync(join(tmpdir(), "relay-flow-tui-plugin-"));
  directories.push(directory);
  const calls = join(directory, "calls.jsonl");
  const executable = join(directory, "relay-flow");
  const marker = join(directory, "rejected");
  writeFileSync(executable, `#!/usr/bin/env bun
import { appendFileSync, existsSync, writeFileSync } from "node:fs";
const input = await Bun.stdin.text();
appendFileSync(process.env.RELAY_FLOW_TEST_CALLS, JSON.stringify({ command: process.argv[2], input }) + "\\n");
if (${JSON.stringify(rejectFirstReport)} && process.argv[2] === "report" && !existsSync(${JSON.stringify(marker)})) {
  writeFileSync(${JSON.stringify(marker)}, "rejected");
  process.stderr.write(${JSON.stringify(JSON.stringify({ error: { code: "invalidReport", message: serverValidationMessage } }))});
  process.exit(1);
}
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

function setEnvelope(
  home: string,
  nodeType: "agent" | "hitl" = "hitl",
  autoReject = false,
) {
  Object.assign(process.env, {
    RELAY_FLOW_HOME: home,
    RELAY_FLOW_RUN_ID: "run-1",
    RELAY_FLOW_TICKET: "TEST-1",
    RELAY_FLOW_NODE: "review",
    RELAY_FLOW_NODE_TYPE: nodeType,
    RELAY_FLOW_AUTO_REJECT: String(autoReject),
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
  let sessionStatus: any = { type: "idle" };
  let idle: ((event: any) => void) | undefined;
  let rendered: (() => unknown) | undefined;
  const replaces: Array<unknown> = [];
  const toasts: Array<unknown> = [];
  const cleanup: Array<() => void> = [];
  const prompts: string[] = [];
  const api: any = {
    client: { session: {
      promptAsync: async (input: any) => { prompts.push(input.body.parts[0].text); },
    } },
    route: { current: { name: "session", params: { sessionID: "session-hitl" } } },
    state: {
      session: {
        messages: () => data.map((item: any) => item),
        status: () => sessionStatus,
      },
      part: (messageID: string) => data.find((item: any) => item.id === messageID)?.parts ?? [],
    },
    event: {
      on: (type: string, handler: (event: any) => void) => {
        if (type === "session.idle") idle = handler;
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
    setStatus(next: any) { sessionStatus = next; },
    triggerIdle() { idle?.({ properties: { sessionID: "session-hitl" } }); },
    getRendered() { return rendered?.() as any; },
    replaces,
    toasts,
    prompts,
    cleanup,
  };
}

async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 75));
}

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
    expect(dialog.options[0].description).toBe("Deliver report to relay-flow.");
    expect(dialog.options[0].details).toBeUndefined();

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

  test("autoReject routes a valid failure without opening approval", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl", true);
    const harness = makeAPI([assistant("failure-1", validFailureReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    await settle();

    expect(harness.replaces).toHaveLength(0);
    const actual = calls(f.calls);
    expect(actual.map((call) => call.command)).toEqual(["report"]);
    expect(JSON.parse(actual[0].input)).toMatchObject({
      reportId: "session-hitl:failure-1",
      report: { status: "failure", nextStep: "review" },
    });
  });

  test("autoReject does not bypass approval for success", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl", true);
    const harness = makeAPI([assistant("success-1", validReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();

    expect(harness.replaces).toHaveLength(1);
    expect(calls(f.calls)).toHaveLength(0);
  });

  test("failure reports selecting end reach server validation with autoReject", async () => {
    const f = fixture();
    setEnvelope(f.directory, "hitl", true);
    const harness = makeAPI([assistant("failure-end", failureToEndReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    await settle();

    expect(harness.replaces).toHaveLength(0);
    expect(calls(f.calls).map((call) => call.command)).toEqual(["report"]);
  });

  test("a permanently rejected approved report prompts correction and needs fresh approval", async () => {
    const f = fixture(true);
    setEnvelope(f.directory);
    const first = assistant("rejected", validReportText);
    const harness = makeAPI([first]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    harness.getRendered().onSelect({ value: "approve" });
    await settle();
    expect(harness.prompts).toEqual([serverValidationMessage]);
    expect(calls(f.calls).map((call) => JSON.parse(call.input).reportId)).toEqual(["session-hitl:rejected"]);
    harness.triggerIdle();
    expect(harness.replaces).toHaveLength(1);

    harness.setData([first, assistant("corrected", validReportText)]);
    harness.triggerIdle();
    expect(harness.replaces).toHaveLength(2);
    expect(calls(f.calls)).toHaveLength(1);
    harness.getRendered().onSelect({ value: "approve" });
    await settle();
    expect(calls(f.calls).map((call) => JSON.parse(call.input).reportId)).toEqual([
      "session-hitl:rejected", "session-hitl:corrected",
    ]);
    expect(harness.prompts).toEqual([serverValidationMessage]);
  });

  test("a corrected HITL failure needs approval even with autoReject", async () => {
    const f = fixture(true);
    setEnvelope(f.directory, "hitl", true);
    const first = assistant("auto-rejected", validFailureReportText);
    const harness = makeAPI([first]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    await settle();
    expect(harness.replaces).toHaveLength(0);
    expect(harness.prompts).toEqual([serverValidationMessage]);
    harness.setData([first, assistant("corrected-failure", validFailureReportText)]);
    harness.triggerIdle();
    expect(harness.replaces).toHaveLength(1);
    expect(calls(f.calls)).toHaveLength(1);
    harness.getRendered().onSelect({ value: "approve" });
    await settle();
    expect(calls(f.calls).map((call) => JSON.parse(call.input).reportId)).toEqual([
      "session-hitl:auto-rejected", "session-hitl:corrected-failure",
    ]);
  });

  test("busy idle events do not inspect or open a dialog", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("busy-message", validReportText)]);
    harness.setStatus({ type: "busy" });
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    await settle();

    expect(harness.replaces).toHaveLength(0);
    expect(calls(f.calls)).toHaveLength(0);
  });

  test("a resumed session is not inspected until an idle event arrives", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("stale-report", validReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    await settle();

    expect(harness.replaces).toHaveLength(0);
    expect(calls(f.calls)).toHaveLength(0);
  });

  test("an authoritative idle event can process before status hydration", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("idle-report", validReportText)]);
    harness.setStatus(undefined);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();

    expect(harness.replaces).toHaveLength(1);
    expect(harness.getRendered().title).toBe("Relay-flow report approval: TEST-1:review");
  });

  test("invalid or missing report stays silent", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("invalid", "ordinary review notes")]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    await settle();

    expect(harness.replaces).toHaveLength(0);
    expect(calls(f.calls)).toHaveLength(0);
    expect(harness.toasts).toHaveLength(0);
  });

  test("a corrected valid report opens the Approve/Reject dialog once", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("invalid", "ordinary review notes")]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    await settle();
    expect(harness.replaces).toHaveLength(0);

    // The server plugin's correction produces a later, valid assistant message.
    const corrected = assistant("corrected", validReportText);
    harness.setData([assistant("invalid", "ordinary review notes"), corrected]);
    harness.triggerIdle();

    expect(harness.replaces).toHaveLength(1);
    const dialog = harness.getRendered();
    expect(dialog.options.map((option: any) => option.title)).toEqual(["Approve", "Reject"]);

    dialog.onSelect({ value: "approve" });
    await settle();
    const actual = calls(f.calls);
    expect(actual.map((call) => call.command)).toEqual(["report"]);
    expect(JSON.parse(actual[0].input)).toMatchObject({ reportId: "session-hitl:corrected" });
  });

  test("duplicate idle events do not open a second dialog", async () => {
    const f = fixture();
    setEnvelope(f.directory);
    const harness = makeAPI([assistant("report-1", validReportText)]);
    await RelayFlowTuiPlugin.tui(harness.api, undefined, undefined as any);
    harness.triggerIdle();
    harness.triggerIdle();
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
