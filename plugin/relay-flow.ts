// relay-flow OpenCode server plugin: the server half of the harness
// contract. It registers sessions, pins stable terminal titles, parses agent
// reports, nudges invalid agent output, and retries the exact parsed report via
// `relay-flow report` stdin. HITL report approval belongs to the separate TUI
// entrypoint in ./tui.ts; this server plugin stays silent for HITL output and
// never uses OpenCode's Question tool for relay-flow approval.
import type { Plugin } from "@opencode-ai/plugin";
import type {
  AssistantMessage,
  Event as RuntimeEvent,
  Message,
  Part,
} from "@opencode-ai/sdk/v2";
import { appendFileSync } from "node:fs";
import { deliverReport, handleIdle, parseReport } from "./index";
import type { Report, ReportEnvelope, ReportAck } from "./index";
import { RelayFlowProcessError, runRelayFlow } from "./transport";

const ENVELOPE_KEYS = [
  "RELAY_FLOW_RUN_ID",
  "RELAY_FLOW_TICKET",
  "RELAY_FLOW_NODE",
  "RELAY_FLOW_NODE_TYPE",
] as const;

function envelopeFromEnv(): { env: Pick<ReportEnvelope, "runId" | "node">; nodeType: "agent" | "hitl"; title: string } | null {
  const v = Object.fromEntries(ENVELOPE_KEYS.map((k) => [k, process.env[k]]));
  if (!v.RELAY_FLOW_RUN_ID || !v.RELAY_FLOW_NODE) return null; // not a relay-flow session
  return {
    env: {
      runId: v.RELAY_FLOW_RUN_ID!,
      node: v.RELAY_FLOW_NODE!,
    },
    nodeType: v.RELAY_FLOW_NODE_TYPE === "hitl" ? "hitl" : "agent",
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
      node: ctx.env.node,
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
  // sessions; retained sessions stay bound to their stable run/node.
  const registered = new Set<string>();
  async function registerSession(sessionID: string) {
    if (registered.has(sessionID)) return;
    const payload = JSON.stringify({
      runId: ctx!.env.runId,
      node: ctx!.env.node,
      sessionId: sessionID,
    });
    await runRelayFlow("runtime-register", payload);
    registered.add(sessionID);
    debug("runtime registration succeeded", {
      operation: "runtime-register",
      runId: ctx.env.runId,
      node: ctx.env.node,
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

  type MessageWithParts = { info: Message; parts: Part[] };
  const handledAssistantIDs = new Set<string>();

  const textParts = (parts: Part[]) => parts.filter((part): part is Extract<Part, { type: "text" }> =>
    part.type === "text" && !part.synthetic && !part.ignored);

  const messageText = (message: MessageWithParts) => textParts(message.parts)
    .map((part) => part.text)
    .join("\n");

  async function messages(sessionID: string): Promise<MessageWithParts[]> {
    const res = await client.session.messages({ path: { id: sessionID } });
    return (res.data ?? []) as MessageWithParts[];
  }

  return {
    event: async ({ event: rawEvent }) => {
      const event = rawEvent as RuntimeEvent;
      let operation = "event";
      let sessionID = "";
      try {
        if (event.type === "session.created" || event.type === "session.updated") {
          sessionID = event.properties.info.id;
          activeSessionID = sessionID;
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
          const msgs = await messages(sessionID);
          const lastAssistant = [...msgs].reverse().find((message) => message.info.role === "assistant");
          if (!lastAssistant) return;

          const info = lastAssistant.info as AssistantMessage;
          const aborted = !!info.error || info.time.completed == null;
          if (aborted || handledAssistantIDs.has(info.id)) return;
          const text = messageText(lastAssistant);

          // The TUI entrypoint owns HITL report approval. Keep this server
          // entrypoint silent for every HITL output; in particular, it must
          // never manufacture a Question-tool approval.
          if (ctx.nodeType === "hitl") {
            if (!parseReport(text).ok) {
              debug("hitl output awaiting tui approval", {
                ticket: process.env.RELAY_FLOW_TICKET ?? "",
                node: ctx.env.node,
                runId: process.env.RELAY_FLOW_RUN_ID ?? "",
                sessionId: sessionID,
              });
            }
            return;
          }

          operation = "idle";
          await handleIdle({
            nodeType: ctx.nodeType,
            lastMessage: text,
            lastMessageCompleted: true,
            session: {
              sendPrompt: async (prompt: string) => {
                await client.session.promptAsync({
                  path: { id: sessionID },
                  body: { parts: [{ type: "text", text: prompt }] },
                });
                handledAssistantIDs.add(info.id);
              },
            },
            report: async (report: Report) => {
              handledAssistantIDs.add(info.id);
              await deliverReport({
                ...ctx.env,
                reportId: `${sessionID}:${info.id}`,
                report,
              }, {
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
