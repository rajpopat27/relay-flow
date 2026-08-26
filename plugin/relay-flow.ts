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
import { spawn } from "node:child_process";
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

const REBIND_PREFIX = "RELAY_FLOW_REBIND:";

export class RelayFlowProcessError extends Error {
  constructor(
    message: string,
    readonly exitCode: number | null,
    readonly stderr: string,
  ) {
    super(message);
    this.name = "RelayFlowProcessError";
  }
}

// The OpenCode runtime currently runs on Bun, but its shell promise does not
// expose a stdin writer. Use Node's process API so the JSON contract is always
// written to stdin and command data can never become shell syntax.
export function runRelayFlow(command: "runtime-register" | "report", json: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn("relay-flow", [command], {
      env: process.env,
      stdio: ["pipe", "ignore", "pipe"],
    });
    let stderr = "";
    let processError: Error | null = null;
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => { stderr += chunk; });
    child.on("error", (err) => { processError = err; });
    child.stdin.on("error", (err) => { processError ??= err; });
    child.on("close", (code) => {
      if (processError) {
        reject(new RelayFlowProcessError(processError.message, code, stderr));
      } else if (code !== 0) {
        reject(new RelayFlowProcessError(`relay-flow ${command} exited with code ${code}`, code, stderr));
      } else {
        resolve();
      }
    });
    child.stdin.end(json);
  });
}

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

export const RelayFlowPlugin: Plugin = async ({ client }) => {
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

  const logFailure = (operation: string, sessionID: string, err: unknown) => {
    const processErr = err instanceof RelayFlowProcessError ? err : null;
    debug("event handler failure", {
      operation,
      runId: ctx.env.runId,
      node: process.env.RELAY_FLOW_NODE ?? "",
      nodeVisitId: ctx.env.nodeVisitId,
      sessionId: sessionID,
      error: err instanceof Error ? err.message : String(err),
      exitCode: processErr?.exitCode == null ? "" : String(processErr.exitCode),
      stderr: processErr?.stderr ?? "",
    });
  };

  const send = async (json: string): Promise<ReportAck> => {
    try {
      await runRelayFlow("report", json);
    } catch (err) {
      logFailure("report", activeSessionID, err);
      throw err;
    }
    return { accepted: true, duplicate: false };
  };

  // Persist the real OpenCode session ID from the event itself. Never list
  // sessions: the server guards this write by run/node/nodeVisitId.
  const registered = new Set<string>();
  async function registerSession(sessionID: string) {
    if (registered.has(sessionID)) return;
    const payload = JSON.stringify({
      runId: ctx!.env.runId,
      node: process.env.RELAY_FLOW_NODE,
      nodeVisitId: ctx!.env.nodeVisitId,
      sessionId: sessionID,
    });
    await runRelayFlow("runtime-register", payload);
    registered.add(sessionID);
    debug("runtime registration succeeded", {
      operation: "runtime-register",
      runId: ctx.env.runId,
      node: process.env.RELAY_FLOW_NODE ?? "",
      nodeVisitId: ctx.env.nodeVisitId,
      sessionId: sessionID,
    });
  }

  // Pin the session title to <ticket>:<node> once we know the session id.
  const pinned = new Set<string>();
  async function pinTitle(sessionID: string) {
    if (pinned.has(sessionID)) return;
    pinned.add(sessionID);
    try {
      await client.session.update({ path: { id: sessionID }, body: { title: ctx!.title } });
    } catch (err) {
      pinned.delete(sessionID); // transient; retry on next event
      throw err;
    }
  }

  let activeSessionID = "";

  return {
    event: async ({ event }) => {
      let operation = "event";
      let sessionID = "";
      try {
        if (event.type === "message.part.updated" && event.properties.part.type === "text" &&
            event.properties.part.text.startsWith(REBIND_PREFIX)) {
          sessionID = event.properties.part.sessionID;
          const visit = event.properties.part.text.slice(REBIND_PREFIX.length).split("\n", 1)[0];
          ctx.env.nodeVisitId = visit;
          registered.clear();
          operation = "runtime-register";
          await registerSession(sessionID);
        }
        if (event.type === "session.created" || event.type === "session.updated") {
          sessionID = event.properties.info.id;
          operation = "runtime-register";
          await registerSession(sessionID);
          operation = "title-pin";
          await pinTitle(sessionID);
        }
        if (event.type === "session.idle") {
          sessionID = event.properties.sessionID;
          activeSessionID = sessionID;
          operation = "runtime-register";
          await registerSession(sessionID);
          operation = "title-pin";
          await pinTitle(sessionID);

          // Last assistant message: completed turns only. An aborted turn
        // (esc = human intervention) is skipped entirely: no parse, no
        // nudge, no report.
          operation = "session-messages";
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

          operation = "idle";
          await handleIdle({
          nodeType: ctx.nodeType,
          lastMessage: text,
          lastMessageCompleted: !aborted,
          nudgePrompt: ctx.nudgePrompt,
          session: {
            sendPrompt: async (prompt: string) => {
              await client.session.promptAsync({
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
      } catch (err) {
        logFailure(operation, sessionID, err);
      }
    },
  };
};

export default RelayFlowPlugin;
