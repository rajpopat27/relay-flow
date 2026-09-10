import { spawn } from "node:child_process";

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
        reject(new RelayFlowProcessError(`relay-flow ${command} exited with code ${code}`, code, stderr));
      } else {
        resolve();
      }
    });
    child.stdin.end(json);
  });
}
