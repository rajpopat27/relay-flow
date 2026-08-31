import { appendFileSync } from "node:fs";

const capturePath = process.env.PI_CAPTURE_PATH;

function record(value: Record<string, unknown>): void {
  if (!capturePath) return;
  appendFileSync(capturePath, `${JSON.stringify(value)}\n`);
}

function branchShape(ctx: any): unknown[] {
  return ctx.sessionManager.getBranch().map((entry: any) => ({
    type: entry.type,
    id: entry.id,
    parentId: entry.parentId,
    role: entry.message?.role,
    stopReason: entry.message?.stopReason,
    contentTypes: Array.isArray(entry.message?.content)
      ? entry.message.content.map((part: any) => part.type)
      : undefined,
  }));
}

export default function capture(pi: any): void {
  record({ event: "factory", sendUserMessage: typeof pi.sendUserMessage, setSessionName: typeof pi.setSessionName });

  pi.on("session_start", (_event: unknown, ctx: any) => {
    record({
      event: "session_start",
      mode: ctx.mode,
      hasUI: ctx.hasUI,
      cwd: ctx.cwd,
      sessionId: ctx.sessionManager.getSessionId(),
      branch: branchShape(ctx),
    });
    pi.setSessionName("captured-name");
    record({ event: "setSessionName", name: ctx.sessionManager.getSessionName() });
  });

  pi.on("agent_settled", (_event: unknown, ctx: any) => {
    record({
      event: "agent_settled",
      sessionId: ctx.sessionManager.getSessionId(),
      branch: branchShape(ctx),
    });
  });

  pi.registerCommand("capture-ui", {
    description: "Capture direct extension UI behavior",
    handler: async (_args: string, ctx: any) => {
      const choice = await ctx.ui.select("Capture approval", ["Approve", "Reject"]);
      record({ event: "ui.select", title: "Capture approval", options: ["Approve", "Reject"], choice });
    },
  });

  pi.registerCommand("capture-send", {
    description: "Capture sendUserMessage behavior",
    handler: async () => {
      pi.sendUserMessage("capture send");
      record({ event: "sendUserMessage", invoked: true });
    },
  });
}
