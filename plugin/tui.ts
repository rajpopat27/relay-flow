// relay-flow OpenCode TUI plugin: the human-facing HITL report gate.
//
// This is intentionally a separate TUI entrypoint from relay-flow.ts. The
// server plugin handles agent reports; this plugin handles only HITL sessions
// and owns the native approval dialog. It does not call a task system, write
// SQLite, or use OpenCode's Question tool.
import type { AssistantMessage, Message, Part } from "@opencode-ai/sdk/v2";
import type { TuiPlugin, TuiPluginModule } from "@opencode-ai/plugin/tui";
import { appendFile } from "node:fs/promises";
import { deliverReport, parseReport } from "./index";
import type { Report, ReportAck } from "./index";
import { runRelayFlow } from "./transport";

const EXPECTED_NODE_TYPE = "hitl";

type TuiApi = Parameters<TuiPlugin>[0];

type TuiContext = {
  runId: string;
  node: string;
  ticket: string;
  autoReject: boolean;
};

function contextFromEnv(): TuiContext | null {
  const runId = process.env.RELAY_FLOW_RUN_ID?.trim();
  const node = process.env.RELAY_FLOW_NODE?.trim();
  const nodeType = process.env.RELAY_FLOW_NODE_TYPE?.trim();
  if (!runId || !node || nodeType !== EXPECTED_NODE_TYPE) return null;
  return {
    runId,
    node,
    ticket: process.env.RELAY_FLOW_TICKET?.trim() ?? "",
    // Only the exact true value enables automatic routing. Missing or
    // malformed launch metadata fails closed to the approval UI.
    autoReject: process.env.RELAY_FLOW_AUTO_REJECT?.trim() === "true",
  };
}

function createDebugLogger() {
  let pending = Promise.resolve();

  return (message: string, ctx: TuiContext, attrs: Record<string, string> = {}) => {
    const root = process.env.RELAY_FLOW_HOME?.trim();
    if (!root) return;
    const fields = Object.entries({ runId: ctx.runId, node: ctx.node, ticket: ctx.ticket, ...attrs })
      .map(([key, value]) => `${key}=${JSON.stringify(value)}`)
      .join(" ");
    const line = `level=DEBUG msg=${JSON.stringify(message)} ${fields}\n`;
    pending = pending
      .then(() => appendFile(`${root}/plugin.log`, line, { mode: 0o600 }))
      .catch(() => {
        // Logging must never prevent the approval UI from continuing.
      });
  };
}

type Debug = (message: string, ctx: TuiContext, attrs?: Record<string, string>) => void;

function textFromMessage(api: TuiApi, messageID: string): string {
  return api.state
    .part(messageID)
    .filter((part: Part): part is Extract<Part, { type: "text" }> =>
      part.type === "text" && !part.synthetic && !part.ignored,
    )
    .map((part) => part.text)
    .join("\n");
}

function latestCompletedAssistant(api: TuiApi, sessionID: string): { info: AssistantMessage; text: string } | null {
  const message = [...api.state.session.messages(sessionID)]
    .reverse()
    .find((item: Message): item is AssistantMessage => item.role === "assistant");
  if (!message || message.error || message.time.completed == null) return null;
  return { info: message, text: textFromMessage(api, message.id) };
}

type ApprovalOption = {
  title: string;
  value: "approve" | "reject";
  description: string;
};

function autoRejectFailure(
  ctx: TuiContext,
  sessionID: string,
  assistant: AssistantMessage,
  report: Report,
  debug: Debug,
): void {
  const reportId = `${sessionID}:${assistant.id}`;
  void deliverReport(
    {
      runId: ctx.runId,
      node: ctx.node,
      reportId,
      report,
    },
    {
      send: async (json: string): Promise<ReportAck> => {
        await runRelayFlow("report", json);
        return { accepted: true, duplicate: false };
      },
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    },
  )
    .then(() => {
      debug("report auto-rejected", ctx, { sessionId: sessionID, reportId });
    })
    .catch((error: unknown) => {
      debug("report auto-reject delivery stopped", ctx, {
        sessionId: sessionID,
        reportId,
        error: error instanceof Error ? error.message : String(error),
      });
    });
}

function showApproval(
  api: TuiApi,
  ctx: TuiContext,
  sessionID: string,
  assistant: AssistantMessage,
  report: Report,
  debug: Debug,
) {
  const reportId = `${sessionID}:${assistant.id}`;
  let selected = false;

  const decide = async (decision: "approve" | "reject") => {
    if (selected) return;
    selected = true;
    api.ui.dialog.clear();

    if (decision === "reject") {
      debug("report rejected", ctx, { sessionId: sessionID, reportId });
      api.ui.toast({ variant: "warning", message: "Relay-flow report rejected" });
      return;
    }

    try {
      await deliverReport(
        {
          runId: ctx.runId,
          node: ctx.node,
          reportId,
          report,
        },
        {
          send: async (json: string): Promise<ReportAck> => {
            await runRelayFlow("report", json);
            return { accepted: true, duplicate: false };
          },
          sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
        },
      );
      debug("report processed", ctx, { sessionId: sessionID, reportId, decision });
      api.ui.toast({ variant: "success", message: "Relay-flow report processed" });
    } catch (error) {
      debug("report delivery stopped", ctx, {
        sessionId: sessionID,
        reportId,
        error: error instanceof Error ? error.message : String(error),
      });
    }
  };

  const options: ApprovalOption[] = [
    {
      title: "Approve",
      value: "approve",
      description: "Deliver report to relay-flow.",
    },
    {
      title: "Reject",
      value: "reject",
      description: "Discard this report. The workflow will not advance.",
    },
  ];

  // replace() resets the host dialog size to medium, so set the size after it.
  api.ui.dialog.replace(
    () =>
      api.ui.DialogSelect({
        title: `Relay-flow report approval${ctx.ticket ? `: ${ctx.ticket}:${ctx.node}` : ""}`,
        placeholder: "Choose an action",
        options,
        onSelect: (option: { value: "approve" | "reject" }) => {
          void decide(option.value);
        },
      }),
    () => {
      if (!selected) {
        selected = true;
        debug("report approval dismissed", ctx, { sessionId: sessionID, reportId });
      }
    },
  );
  api.ui.dialog.setSize("large");
}

const tui: TuiPlugin = async (api) => {
  const ctx = contextFromEnv();
  if (!ctx) return;

  const debug = createDebugLogger();
  const handledAssistantIDs = new Set<string>();
  let disposed = false;

  const processIdle = (sessionID: string) => {
    if (disposed) return;
    const route = api.route.current;
    if (route.name !== "session" || route.params.sessionID !== sessionID) return;

    // Only session.idle is authoritative. OpenCode dispatches message.updated
    // while the model is still working, so processing that high-volume event
    // stream would inspect intermediate assistant/tool turns on the
    // synchronous TUI event path.
    const status = api.state.session.status(sessionID);
    if (status && status.type !== "idle") return;

    const latest = latestCompletedAssistant(api, sessionID);
    if (!latest || handledAssistantIDs.has(latest.info.id)) return;
    const parsed = parseReport(latest.text);
    if (!parsed.ok) {
      // Invalid/missing HITL output is intentionally silent. Agent-node
      // correction remains in the server plugin; no Question tool is used.
      // Mark it handled so duplicate idle/message events do not synchronously
      // re-log the same planning response.
      handledAssistantIDs.add(latest.info.id);
      debug("hitl output ignored", ctx, { sessionId: sessionID, assistantMessageId: latest.info.id });
      return;
    }
    if (parsed.report.status === "failure" && parsed.report.nextStep === "end") {
      // This is forbidden by the workflow contract. Reject it before either
      // automatic delivery or the native approval UI; the durable boundary
      // enforces the same rule.
      handledAssistantIDs.add(latest.info.id);
      debug("hitl output ignored", ctx, {
        sessionId: sessionID,
        assistantMessageId: latest.info.id,
        reason: "failure report cannot select end",
      });
      return;
    }

    // Mark before rendering or delivering so duplicate idle/message events
    // cannot open a second dialog or submit another report.
    handledAssistantIDs.add(latest.info.id);
    if (ctx.autoReject && parsed.report.status === "failure") {
      autoRejectFailure(ctx, sessionID, latest.info, parsed.report, debug);
      return;
    }
    showApproval(api, ctx, sessionID, latest.info, parsed.report, debug);
  };

  const offIdle = api.event.on("session.idle", (event) => {
    processIdle(event.properties.sessionID);
  });
  api.lifecycle.onDispose(() => {
    disposed = true;
    offIdle();
  });

  // OpenCode v1.18.30 renders the session route only after TUI plugins finish
  // loading, so the initial --prompt cannot complete before this idle listener
  // is installed. Do not poll on startup: polling can re-open an older report
  // from a resumed session before a new prompt has produced a message.
};

export const RelayFlowTuiPlugin: TuiPluginModule & { id: string } = {
  id: "relay-flow-plugin.tui",
  tui,
};

export default RelayFlowTuiPlugin;
