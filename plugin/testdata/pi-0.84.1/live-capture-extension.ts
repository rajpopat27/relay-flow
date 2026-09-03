// Live contract observer for relay-flow task 5.1/5.2.
// Records the REAL Pi 0.84.1 runtime contract; performs no relay-flow work.
import { appendFileSync, writeFileSync } from "node:fs";

const OUT = process.env.PI_CAPTURE_FILE ?? "/tmp/pi-live-contract/capture/observer.json";
const EVENTS = process.env.PI_CAPTURE_EVENTS ?? "/tmp/pi-live-contract/capture/observer-events.jsonl";

function relayEnv(): Record<string, string | undefined> {
  const out: Record<string, string | undefined> = {};
  for (const key of Object.keys(process.env)) {
    if (key.startsWith("RELAY_FLOW_")) out[key] = process.env[key];
  }
  return out;
}

export default function capture(pi: any): void {
  pi.on("session_start", async (event: any, ctx: any) => {
    const snapshot = {
      event: "session_start",
      eventShape: Object.keys(event ?? {}),
      argv: process.argv,
      cwd: process.cwd(),
      ctxCwd: ctx.cwd,
      mode: ctx.mode,
      hasUI: ctx.hasUI,
      stdinIsTTY: process.stdin.isTTY === true,
      stdoutIsTTY: process.stdout.isTTY === true,
      sessionId: ctx.sessionManager.getSessionId(),
      sessionFile: ctx.sessionManager.getSessionFile?.(),
      sessionName: pi.getSessionName?.(),
      branchLength: ctx.sessionManager.getBranch().length,
      uiSelectType: typeof ctx.ui?.select,
      relayEnv: relayEnv(),
      piVersion: process.env.PI_VERSION ?? null,
    };
    writeFileSync(OUT, JSON.stringify(snapshot, null, 2));
    appendFileSync(EVENTS, JSON.stringify({ at: new Date().toISOString(), ...snapshot }) + "\n");
    if (process.env.PI_CAPTURE_SET_NAME) pi.setSessionName(process.env.PI_CAPTURE_SET_NAME);
  });

  pi.on("agent_settled", async (event: any, ctx: any) => {
    const branch = ctx.sessionManager.getBranch();
    const entries = branch.map((entry: any) => ({
      type: entry.type,
      id: entry.id,
      parentId: entry.parentId,
      role: entry.message?.role,
      stopReason: entry.message?.stopReason,
      contentTypes: Array.isArray(entry.message?.content)
        ? entry.message.content.map((part: any) => part?.type)
        : typeof entry.message?.content,
      text: Array.isArray(entry.message?.content)
        ? entry.message.content.filter((p: any) => p?.type === "text").map((p: any) => p.text).join("\n").slice(0, 400)
        : undefined,
    }));
    appendFileSync(EVENTS, JSON.stringify({
      at: new Date().toISOString(),
      event: "agent_settled",
      eventShape: Object.keys(event ?? {}),
      sessionId: ctx.sessionManager.getSessionId(),
      mode: ctx.mode,
      hasUI: ctx.hasUI,
      entries,
    }) + "\n");
  });
}
