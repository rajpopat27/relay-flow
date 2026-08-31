import { appendFileSync } from "node:fs";
import type {
  ExtensionAPI,
  ExtensionContext,
  SessionStartEvent,
} from "@earendil-works/pi-coding-agent";
import { runRelayFlow } from "./relay-flow";

const REQUIRED_METADATA = [
  "RELAY_FLOW_HOME",
  "RELAY_FLOW_RUN_ID",
  "RELAY_FLOW_WORKFLOW",
  "RELAY_FLOW_REPO",
  "RELAY_FLOW_TICKET",
  "RELAY_FLOW_NODE",
  "RELAY_FLOW_NODE_TYPE",
  "RELAY_FLOW_NUDGE_PROMPT",
  "RELAY_FLOW_NEXT_STEPS_JSON",
] as const;

type NodeType = "agent" | "hitl";

type RelayFlowMetadata = {
  runId: string;
  ticket: string;
  node: string;
  nodeType: NodeType;
  title: string;
};

function log(message: string, attrs: Record<string, string> = {}): void {
  const home = process.env.RELAY_FLOW_HOME;
  if (!home) return;
  const fields = Object.entries(attrs)
    .map(([key, value]) => `${key}=${JSON.stringify(value)}`)
    .join(" ");
  try {
    appendFileSync(
      `${home}/plugin.log`,
      `level=DEBUG msg=${JSON.stringify(message)}${fields ? ` ${fields}` : ""}\n`,
      { mode: 0o600 },
    );
  } catch {
    // Plugin logging must never prevent Pi from starting or continuing.
  }
}

function relayFlowMetadata(): RelayFlowMetadata | null {
  const runId = process.env.RELAY_FLOW_RUN_ID;
  const node = process.env.RELAY_FLOW_NODE;
  if (!runId && !node) return null;

  const missing = REQUIRED_METADATA.filter(
    (key) => process.env[key] === undefined ||
      (key !== "RELAY_FLOW_NUDGE_PROMPT" && process.env[key] === ""),
  );
  if (missing.length > 0) {
    log("invalid relay-flow metadata", { missing: missing.join(",") });
    return null;
  }

  const nodeType = process.env.RELAY_FLOW_NODE_TYPE;
  if (nodeType !== "agent" && nodeType !== "hitl") {
    log("invalid relay-flow metadata", { reason: "RELAY_FLOW_NODE_TYPE" });
    return null;
  }

  try {
    JSON.parse(process.env.RELAY_FLOW_NEXT_STEPS_JSON!);
  } catch {
    log("invalid relay-flow metadata", { reason: "RELAY_FLOW_NEXT_STEPS_JSON" });
    return null;
  }

  return {
    runId: runId!,
    ticket: process.env.RELAY_FLOW_TICKET!,
    node: node!,
    nodeType,
    title: `${process.env.RELAY_FLOW_TICKET}:${node}`,
  };
}

async function registerSession(metadata: RelayFlowMetadata, sessionId: string): Promise<void> {
  await runRelayFlow(
    "runtime-register",
    JSON.stringify({ runId: metadata.runId, node: metadata.node, sessionId }),
  );
  log("runtime registration succeeded", {
    operation: "runtime-register",
    runId: metadata.runId,
    node: metadata.node,
    sessionId,
  });
}

/**
 * Pi entry point for relay-flow-plugin.
 *
 * Pi loads extensions for ordinary sessions too. Relay-flow behavior is only
 * enabled for sessions launched by the harness, identified by the pair of
 * relay-flow identity variables.
 */
export default function relayFlowPi(pi: ExtensionAPI): void {
  const metadata = relayFlowMetadata();
  if (!metadata) return;

  const registeredSessionIDs = new Set<string>();
  const registrationAttempts = new Map<string, Promise<void>>();

  const register = async (sessionId: string): Promise<void> => {
    if (registeredSessionIDs.has(sessionId)) return;
    const existing = registrationAttempts.get(sessionId);
    if (existing) return existing;

    const attempt = registerSession(metadata, sessionId)
      .then(() => {
        registeredSessionIDs.add(sessionId);
      })
      .catch((error: unknown) => {
        log("runtime registration failed", {
          operation: "runtime-register",
          runId: metadata.runId,
          node: metadata.node,
          sessionId,
          error: error instanceof Error ? error.message : String(error),
        });
      })
      .finally(() => {
        registrationAttempts.delete(sessionId);
      });
    registrationAttempts.set(sessionId, attempt);
    return attempt;
  };

  const start = async (_event: SessionStartEvent, ctx: ExtensionContext): Promise<void> => {
    const sessionId = ctx.sessionManager.getSessionId();
    await register(sessionId);
    if (pi.getSessionName() !== metadata.title) {
      pi.setSessionName(metadata.title);
    }
  };

  pi.on("session_start", start);
}
