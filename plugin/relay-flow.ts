// relay-flow OpenCode plugin entry: wires the pure core in ./index.ts into
// the OpenCode plugin runtime. Active only when the harness launched this
// session (RELAY_FLOW_* env present). Behavior per design.md decision 17 and
// specs/integration-contracts/spec.md 'Runtime harness plugin owns message
// behavior':
//   - pins the session title to <ticket>:<node> (stable terminal identity)
//   - on session.idle: reads the last assistant message, parses the complete
//     report contract, nudges agent nodes on invalid output, and gates HITL
//     delivery/corrections on a matching Question-tool approval
//   - an aborted turn (esc = human intervention) is never parsed or nudged:
//     the assistant message carries a MessageAbortedError / no completed time
import type { Plugin } from "@opencode-ai/plugin";
import type {
  AssistantMessage,
  Event as RuntimeEvent,
  Message,
  Part,
  QuestionRequest,
} from "@opencode-ai/sdk/v2";
import { spawn } from "node:child_process";
import { appendFileSync } from "node:fs";
import { deliverReport, handleIdle, parseReport } from "./index";
import type { Report, ReportEnvelope, ReportAck } from "./index";

const ENVELOPE_KEYS = [
  "RELAY_FLOW_RUN_ID",
  "RELAY_FLOW_TICKET",
  "RELAY_FLOW_NODE",
  "RELAY_FLOW_NODE_TYPE",
] as const;

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
  type HitlQuestionGate = {
    requestID: string;
    sessionID: string;
    approved: boolean;
    previousAssistantID: string;
  };
  let hitlGate: HitlQuestionGate | null = null;
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

  async function latestAssistantID(sessionID: string): Promise<string> {
    const latest = [...await messages(sessionID)].reverse().find((message) => message.info.role === "assistant");
    return latest?.info.id ?? "";
  }

  return {
    event: async ({ event: rawEvent }) => {
      // OpenCode 1.18 emits the v2 Question events through the generic plugin
      // event hook even though older root Plugin typings omit them.
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
        if (ctx.nodeType === "hitl" && event.type === "question.asked") {
          const request: QuestionRequest = event.properties;
          sessionID = request.sessionID;
          if (activeSessionID && request.sessionID !== activeSessionID) return;
          activeSessionID = request.sessionID;
          operation = "question-baseline";
          const gate: HitlQuestionGate = {
            requestID: request.id,
            sessionID: request.sessionID,
            approved: false,
            previousAssistantID: "",
          };
          hitlGate = gate;
          return;
        }
        if (ctx.nodeType === "hitl" && event.type === "question.replied") {
          const reply = event.properties;
          if (hitlGate && reply.sessionID === hitlGate.sessionID && reply.requestID === hitlGate.requestID) {
            const approved = reply.answers.some((answer) => answer.includes("Approve"));
            if (!approved) {
              hitlGate = null;
              return;
            }
            const gate = hitlGate;
            gate.approved = false;
            operation = "question-reply-baseline";
            const previousAssistantID = await latestAssistantID(reply.sessionID);
            if (hitlGate === gate) {
              gate.previousAssistantID = previousAssistantID;
              gate.approved = true;
            }
          }
          return;
        }
        if (ctx.nodeType === "hitl" && event.type === "question.rejected") {
          const rejection = event.properties;
          if (hitlGate && rejection.sessionID === hitlGate.sessionID && rejection.requestID === hitlGate.requestID) {
            hitlGate = null;
          }
          return;
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

          // Missing HITL output stays silent. Invalid output is logged and is
          // corrected only when a matching approval currently authorizes it.
          const logHitlSilent = (reason: "missing" | "invalid") => {
            if (ctx.nodeType !== "hitl") return;
            debug("hitl silent", {
              reason,
              ticket: process.env.RELAY_FLOW_TICKET ?? "",
              node: ctx.env.node,
              runId: process.env.RELAY_FLOW_RUN_ID ?? "",
            });
          };

          if (!lastAssistant) {
            logHitlSilent("missing");
            return;
          }
          const info = lastAssistant.info as AssistantMessage;
          const aborted = !!info.error || info.time.completed == null;
          if (aborted || handledAssistantIDs.has(info.id)) return;
          const sessionGate = ctx.nodeType === "hitl" && hitlGate?.sessionID === sessionID ? hitlGate : null;
          if (sessionGate && !sessionGate.approved) return;
          if (sessionGate?.previousAssistantID === info.id) return;
          const authorized = sessionGate?.approved === true;
          if (authorized) hitlGate = null;
          const text = messageText(lastAssistant);

          if (!aborted && !parseReport(text).ok) {
            logHitlSilent("invalid");
          }

          operation = "idle";
          await handleIdle({
            nodeType: ctx.nodeType,
            lastMessage: text,
            lastMessageCompleted: true,
            hitlAuthorized: authorized,
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
          if (ctx.nodeType === "hitl" && !parseReport(text).ok && !authorized) {
            handledAssistantIDs.add(info.id);
          }
        }
      } catch (err) {
        logFailure(operation, sessionID, err);
      }
    },
  };
};

export default RelayFlowPlugin;
