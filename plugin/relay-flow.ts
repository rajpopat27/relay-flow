// relay-flow OpenCode plugin entry: wires the pure core in ./index.ts into
// the OpenCode plugin runtime. Active only when the harness launched this
// session (RELAY_FLOW_* env present). Behavior per design.md decision 17 and
// specs/integration-contracts/spec.md 'Runtime harness plugin owns message
// behavior':
//   - pins the session title to <ticket>:<node> (stable terminal identity)
//   - on session.idle: reads the last assistant message, parses the complete
//     report contract, nudges agent nodes on invalid output, stays silent
//     for HITL, and delivers a valid report via `relay-flow report` stdin
//   - an aborted turn (esc = human intervention) is never parsed or nudged:
//     the assistant message carries a MessageAbortedError / no completed time
import type { Plugin } from "@opencode-ai/plugin";
import { appendFileSync } from "node:fs";
import { deliverReport, handleIdle, parseReport } from "./index";
import type { Report, ReportEnvelope, ReportAck } from "./index";

const ENVELOPE_KEYS = [
  "RELAY_FLOW_RUN_ID",
  "RELAY_FLOW_NODE_VISIT_ID",
  "RELAY_FLOW_TICKET",
  "RELAY_FLOW_NODE",
  "RELAY_FLOW_NODE_TYPE",
  "RELAY_FLOW_NUDGE_PROMPT",
] as const;

function envelopeFromEnv(): { env: ReportEnvelope; nodeType: "agent" | "hitl"; nudgePrompt: string; title: string } | null {
  const v = Object.fromEntries(ENVELOPE_KEYS.map((k) => [k, process.env[k]]));
  if (!v.RELAY_FLOW_RUN_ID || !v.RELAY_FLOW_NODE_VISIT_ID) return null; // not a relay-flow session
  return {
    env: {
      runId: v.RELAY_FLOW_RUN_ID!,
      nodeVisitId: v.RELAY_FLOW_NODE_VISIT_ID!,
      report: null as unknown as Report, // filled at parse time
    },
    nodeType: v.RELAY_FLOW_NODE_TYPE === "hitl" ? "hitl" : "agent",
    nudgePrompt: v.RELAY_FLOW_NUDGE_PROMPT ?? "",
    title: `${v.RELAY_FLOW_TICKET}:${v.RELAY_FLOW_NODE}`,
  };
}

export const RelayFlowPlugin: Plugin = async ({ client, $ }) => {
  const ctx = envelopeFromEnv();
  if (!ctx) return {}; // relay-flow did not launch this session; no-op.

  // 9.4: invalid/missing HITL output stays silent to the session but is
  // logged at debug. The plugin runs in the OpenCode process (separate from
  // serve), so its debug stream is $RELAY_FLOW_HOME/plugin.log — never the
  // session.
  const pluginLog = process.env.RELAY_FLOW_HOME
    ? `${process.env.RELAY_FLOW_HOME}/plugin.log`
    : null;
  const debug = (msg: string, attrs: Record<string, string>) => {
    if (!pluginLog) return;
    const kv = Object.entries(attrs).map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(" ");
    try {
      appendFileSync(pluginLog, `level=DEBUG msg=${JSON.stringify(msg)} ${kv}\n`, { mode: 0o600 });
    } catch {
      // Logging must never break the plugin.
    }
  };

  const send = async (json: string): Promise<ReportAck> => {
    const out = await $`relay-flow report`.stdin(json).quiet().nothrow();
    if (out.exitCode !== 0) throw new Error(`relay-flow report exit ${out.exitCode}: ${out.stderr.toString()}`);
    return { accepted: true, duplicate: false };
  };

  // Pin the session title to <ticket>:<node> once we know the session id.
  const pinned = new Set<string>();
  async function pinTitle(sessionID: string) {
    if (pinned.has(sessionID)) return;
    pinned.add(sessionID);
    try {
      await client.session.update({ path: { id: sessionID }, body: { title: ctx!.title } });
    } catch {
      pinned.delete(sessionID); // transient; retry on next event
    }
  }

  return {
    event: async ({ event }) => {
      if (event.type === "session.idle") {
        const sessionID = event.properties.sessionID;
        await pinTitle(sessionID);

        // Last assistant message: completed turns only. An aborted turn
        // (esc = human intervention) is skipped entirely: no parse, no
        // nudge, no report.
        const res = await client.session.messages({ path: { id: sessionID } });
        const msgs = (res.data ?? []) as Array<{ info: any; parts: any[] }>;
        const lastAssistant = [...msgs].reverse().find((m) => m.info?.role === "assistant");

        // 9.4: invalid/missing HITL output stays silent to the session but
        // is logged at debug. Missing = no completed assistant turn at all
        // (or only an aborted one); invalid = text does not parse as the
        // report contract. Either way handleIdle will send no nudge and
        // write no report; this debug record is the only observable trace.
        const logHitlSilent = (reason: "missing" | "invalid") => {
          if (ctx.nodeType !== "hitl") return;
          debug("hitl silent", {
            reason,
            ticket: process.env.RELAY_FLOW_TICKET ?? "",
            node: process.env.RELAY_FLOW_NODE ?? "",
            nodeVisitId: process.env.RELAY_FLOW_NODE_VISIT_ID ?? "",
            runId: process.env.RELAY_FLOW_RUN_ID ?? "",
          });
        };

        if (!lastAssistant) {
          logHitlSilent("missing");
          return;
        }
        const info = lastAssistant.info;
        const aborted = !!info.error || info.time?.completed == null;

        const text = lastAssistant.parts
          .filter((p) => p?.type === "text" && !p.synthetic && !p.ignored)
          .map((p) => p.text ?? "")
          .join("\n");

        if (!aborted && !parseReport(text).ok) {
          logHitlSilent("invalid");
        }

        await handleIdle({
          nodeType: ctx.nodeType,
          lastMessage: text,
          lastMessageCompleted: !aborted,
          nudgePrompt: ctx.nudgePrompt,
          session: {
            sendPrompt: async (prompt: string) => {
              await client.session.prompt_async({
                path: { id: sessionID },
                body: { parts: [{ type: "text", text: prompt }] },
              });
            },
          },
          report: async (report: Report) => {
            await deliverReport({ ...ctx.env, report }, {
              send,
              sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
            });
          },
        });
      }
    },
  };
};

export default RelayFlowPlugin;
