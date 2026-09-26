import { spawn } from "node:child_process";

export class RelayFlowProcessError extends Error {
  constructor(
    message: string,
    readonly exitCode: number | null,
    readonly stderr: string,
    readonly code: string | null = null,
  ) {
    super(message);
    this.name = "RelayFlowProcessError";
  }
}

// Only a non-zero report command with a structured error envelope can carry
// a server validation code. Other stderr (including malformed JSON) is a
// transport/server failure, not a permanent report rejection.
function reportError(stderr: string): { code: string; message: string } | null {
  try {
    const error = JSON.parse(stderr).error;
    if (typeof error?.code === "string" && typeof error?.message === "string") {
      return { code: error.code, message: error.message };
    }
  } catch {
    // Non-JSON diagnostic from a failed command.
  }
  return null;
}

// Use an argv-only child process so JSON is written directly to stdin. The
// report payload must never become shell syntax or pass through a shell.
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
        const error = command === "report" ? reportError(stderr) : null;
        reject(new RelayFlowProcessError(
          error?.message ?? `relay-flow ${command} exited with code ${code}`,
          code, stderr, error?.code ?? null,
        ));
      } else {
        resolve();
      }
    });
    child.stdin.end(json);
  });
}
