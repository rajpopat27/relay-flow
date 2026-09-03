// relay-flow OpenCode TUI plugin: the human-facing HITL report gate.
//
// This is intentionally a separate TUI entrypoint from relay-flow.ts. The
// server plugin handles agent reports; this plugin handles only HITL sessions
// and owns the native approval dialog. It does not call a task system, write
// SQLite, or use OpenCode's Question tool.
import type { AssistantMessage, Message, Part } from "@opencode-ai/sdk/v2";
import type { TuiPlugin, TuiPluginModule } from "@opencode-ai/plugin/tui";
import { appendFileSync } from "node:fs";
import { deliverReport, parseReport } from "./index";
import type { Report, ReportAck } from "./index";
import { runRelayFlow } from "./relay-flow";

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

function debug(message: string, ctx: TuiContext, attrs: Record<string, string> = {}) {
  const root = process.env.RELAY_FLOW_HOME?.trim();
  if (!root) return;
  const fields = Object.entries({ runId: ctx.runId, node: ctx.node, ticket: ctx.ticket, ...attrs })
    .map(([key, value]) => `${key}=${JSON.stringify(value)}`)
    .join(" ");
  try {
    appendFileSync(`${root}/plugin.log`, `level=DEBUG msg=${JSON.stringify(message)} ${fields}\n`, { mode: 0o600 });
  } catch {
    // Logging must never break the approval UI.
  }
}

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

function showApproval(
  api: TuiApi,
  ctx: TuiContext,
  sessionID: string,
  assistant: AssistantMessage,
  report: Report,
) {
  const reportId = `${sessionID}:${assistant.id}`;
  const preview = formatReportPreview(report);
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

  api.ui.dialog.setSize("large");
  api.ui.dialog.replace(
    () =>
      api.ui.DialogSelect({
        title: `Relay-flow report approval${ctx.ticket ? `: ${ctx.ticket}:${ctx.node}` : ""}`,
        placeholder: "Choose an action",
        options: [
          {
            title: "Approve",
            value: "approve",
            description: `Deliver this exact report to relay-flow.\n\n${preview}`,
          },
          {
            title: "Reject",
            value: "reject",
            description: "Discard this report. The workflow will not advance.",
          },
        ],
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
}

const tui: TuiPlugin = async (api) => {
  const ctx = contextFromEnv();
  if (!ctx) return;

  const handledAssistantIDs = new Set<string>();
  let disposed = false;

  const processIdle = (sessionID: string) => {
    if (disposed) return;
    const route = api.route.current;
    if (route.name !== "session" || route.params.sessionID !== sessionID) return;

    const latest = latestCompletedAssistant(api, sessionID);
    if (!latest || handledAssistantIDs.has(latest.info.id)) return;
    const parsed = parseReport(latest.text);
    if (!parsed.ok) {
      // Invalid/missing HITL output is intentionally silent. Agent-node
      // correction remains in the server plugin; no Question tool is used.
      debug("hitl output ignored", ctx, { sessionId: sessionID, assistantMessageId: latest.info.id });
      return;
    }

    // Mark before rendering so duplicate idle/message events cannot open a
    // second dialog for the same assistant message.
    handledAssistantIDs.add(latest.info.id);
    showApproval(api, ctx, sessionID, latest.info, parsed.report);
  };

  const offIdle = api.event.on("session.idle", (event) => {
    processIdle(event.data.sessionID);
  });
  const offMessage = api.event.on("message.updated", (event) => {
    if (event.data.info.role === "assistant") processIdle(event.data.sessionID);
  });
  api.lifecycle.onDispose(() => {
    disposed = true;
    offIdle();
    offMessage();
  });

  // The initial `--prompt` can finish before the TUI listener is attached.
  // Recheck the active route briefly so a completed report is not lost while
  // still keeping session.idle as the normal trigger.
  const delays = [0, 100, 500, 1000];
  const timers = delays.map((delay) =>
    setTimeout(() => {
      const route = api.route.current;
      if (route.name === "session") processIdle(route.params.sessionID);
    }, delay),
  );
  api.lifecycle.onDispose(() => {
    for (const timer of timers) clearTimeout(timer);
  });
};

export const RelayFlowTuiPlugin: TuiPluginModule & { id: string } = {
  id: "relay-flow-plugin.tui",
  tui,
};

export default RelayFlowTuiPlugin;
