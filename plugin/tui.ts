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

export function formatReportPreview(report: Report): string {
  return [
    `STATUS: ${report.status}`,
    `NEXT STEP: ${report.nextStep}`,
    "",
    "SUMMARY:",
    `COMPLETED: ${report.summary.completed}`,
    `COMMITS: ${report.summary.commits}`,
    `NOT COMPLETED: ${report.summary.notCompleted}`,
    `ISSUES DISCOVERED: ${report.summary.issuesDiscovered}`,
    `VERIFICATION: ${report.summary.verification}`,
    `NOTES: ${report.summary.notes}`,
    "",
    "FEEDBACK:",
    `REASON FOR NEXT STEP: ${report.feedback.reasonForNextStep}`,
    `REQUIRED ACTIONS: ${report.feedback.requiredActions}`,
    `RELEVANT CONTEXT: ${report.feedback.relevantContext}`,
    `EXPECTED RESULT: ${report.feedback.expectedResult}`,
  ].join("\n");
}

const REPORT_DETAIL_WIDTH = 72;

export function formatReportDetails(report: Report): string[] {
  return formatReportPreview(report).split("\n").flatMap((line) => {
    if (line.length === 0) return [""];
    const chunks: string[] = [];
    for (let offset = 0; offset < line.length; offset += REPORT_DETAIL_WIDTH) {
      chunks.push(line.slice(offset, offset + REPORT_DETAIL_WIDTH));
    }
    return chunks;
  });
}

type ApprovalOption = {
  title: string;
  value: "approve" | "reject";
  description: string;
  // OpenCode 1.18.30's DialogSelect host preserves this internal field and
  // renders each item as its own row. The public TUI type omits it, but using
  // it avoids putting a multiline report into the one-line description slot.
  details?: string[];
};

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
      description: "Deliver this exact report to relay-flow.",
      details: formatReportDetails(report),
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

    // Mark before rendering so duplicate idle/message events cannot open a
    // second dialog for the same assistant message.
    handledAssistantIDs.add(latest.info.id);
    showApproval(api, ctx, sessionID, latest.info, parsed.report, debug);
  };

  const offIdle = api.event.on("session.idle", (event) => {
    processIdle(event.data.sessionID);
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
