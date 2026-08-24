import { describe, expect, test } from "bun:test";
import { readFileSync } from "fs";
import { join } from "path";

// Wrapper-wiring tests: the opencode entry must (a) no-op without the
// RELAY_FLOW_* envelope, (b) skip aborted turns (esc = human intervention),
// (c) pin the session title, (d) deliver via `relay-flow report` stdin.
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

  test("subscribes to session.idle only", () => {
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
});
