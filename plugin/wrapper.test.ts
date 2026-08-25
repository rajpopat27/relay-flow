import { describe, expect, test } from "bun:test";
import { readFileSync } from "fs";
import { join } from "path";
import { RelayFlowPlugin } from "./relay-flow";

// Wrapper-wiring tests: the opencode entry must (a) no-op without the
// RELAY_FLOW_* envelope, (b) skip aborted turns (esc = human intervention),
// (c) register the event's session ID, (d) pin the session title, and
// (e) deliver via `relay-flow report` stdin.
// These assert structure/wiring only; behavior of parse/nudge/deliver is
// covered by the existing core tests.

const src = readFileSync(join(import.meta.dir, "relay-flow.ts"), "utf8");

describe("opencode entry wiring", () => {
  test("no-ops when RELAY_FLOW_RUN_ID/NODE_VISIT_ID absent", () => {
    expect(src).toContain("if (!v.RELAY_FLOW_RUN_ID || !v.RELAY_FLOW_NODE_VISIT_ID) return null");
  });

  test("aborted turn (error or no completed time) is skipped, never nudged", () => {
    expect(src).toContain("const aborted = !!info.error || info.time?.completed == null");
    expect(src).toContain("lastMessageCompleted: !aborted");
  });

  test("pins session title to <ticket>:<node>", () => {
    expect(src).toContain("title: `${v.RELAY_FLOW_TICKET}:${v.RELAY_FLOW_NODE}`");
    expect(src).toContain("client.session.update");
  });

  test("delivers via relay-flow report stdin", () => {
    expect(src).toContain("$`relay-flow report`.stdin(json)");
  });

  test("registers session identity from session events without discovery", () => {
    expect(src).toContain('event.type === "session.created"');
    expect(src).toContain('event.type === "session.updated"');
    expect(src).toContain("event.properties.info.id");
    expect(src).toContain("$`relay-flow runtime-register`.stdin(payload)");
    expect(src).not.toContain("client.session.list");
    expect(src).not.toContain("client.session.children");
  });

  test("session.created immediately registers the emitted ID", async () => {
    const saved = { ...process.env };
    Object.assign(process.env, {
      RELAY_FLOW_RUN_ID: "run-1",
      RELAY_FLOW_NODE_VISIT_ID: "visit-1",
      RELAY_FLOW_TICKET: "TEST-1",
      RELAY_FLOW_NODE: "implement",
      RELAY_FLOW_NODE_TYPE: "agent",
    });
    const registrations: string[] = [];
    const $ = (parts: TemplateStringsArray) => ({
      stdin: (payload: string) => ({
        quiet: () => ({
          nothrow: async () => {
            if (parts.join("") === "relay-flow runtime-register") registrations.push(payload);
            return { exitCode: 0, stderr: Buffer.from("") };
          },
        }),
      }),
    });
    try {
      const hooks = await RelayFlowPlugin({ client: {}, $ } as any);
      await hooks.event!({
        event: { type: "session.created", properties: { info: { id: "session-created" } } },
      } as any);
      expect(registrations.map(JSON.parse)).toEqual([{
        runId: "run-1",
        node: "implement",
        nodeVisitId: "visit-1",
        sessionId: "session-created",
      }]);
    } finally {
      process.env = saved;
    }
  });

  test("handles session.idle reports", () => {
    expect(src).toContain('event.type === "session.idle"');
    expect(src).not.toContain('"chat.message"');
  });

  // 9.4: HITL silence — invalid OR missing HITL output is silent to the
  // session AND logged at debug to plugin.log. The missing case is the
  // no-lastAssistant early return; the invalid case is a completed turn
  // whose text fails the report contract.
  test("hitl silent debug covers missing and invalid output", () => {
    expect(src).toContain('logHitlSilent("missing")');
    expect(src).toContain('logHitlSilent("invalid")');
    expect(src).toContain('debug("hitl silent"');
    expect(src).toContain("appendFileSync(pluginLog");
    expect(src).toContain('${process.env.RELAY_FLOW_HOME}/plugin.log');
    // Logs carry the identity attrs required by section 9.
    for (const k of ["ticket", "node", "nodeVisitId", "runId"]) {
      expect(src).toContain(`${k}:`);
    }
  });

  test("nudges through the supported promptAsync client method", async () => {
    const saved = { ...process.env };
    Object.assign(process.env, {
      RELAY_FLOW_RUN_ID: "run-1",
      RELAY_FLOW_NODE_VISIT_ID: "visit-1",
      RELAY_FLOW_TICKET: "TEST-1",
      RELAY_FLOW_NODE: "implement",
      RELAY_FLOW_NODE_TYPE: "agent",
      RELAY_FLOW_NUDGE_PROMPT: "emit the report",
    });
    const prompts: unknown[] = [];
    const registrations: string[] = [];
    const $ = (parts: TemplateStringsArray) => {
      const command = parts.join("");
      return {
        stdin: (payload: string) => ({
          quiet: () => ({
            nothrow: async () => {
              if (command === "relay-flow runtime-register") registrations.push(payload);
              return { exitCode: 0, stderr: Buffer.from("") };
            },
          }),
        }),
      };
    };
    const client = {
      session: {
        update: async () => ({}),
        messages: async () => ({
          data: [{
            info: { role: "assistant", time: { completed: Date.now() } },
            parts: [{ type: "text", text: "invalid output" }],
          }],
        }),
        promptAsync: async (input: unknown) => { prompts.push(input); },
      },
    };
    try {
      const hooks = await RelayFlowPlugin({ client, $ } as any);
      await hooks.event!({
        event: { type: "session.idle", properties: { sessionID: "session-1" } },
      } as any);
      expect(registrations.map(JSON.parse)).toEqual([{
        runId: "run-1",
        node: "implement",
        nodeVisitId: "visit-1",
        sessionId: "session-1",
      }]);
      expect(prompts).toEqual([{
        path: { id: "session-1" },
        body: { parts: [{ type: "text", text: "emit the report" }] },
      }]);
    } finally {
      process.env = saved;
    }
  });
});
