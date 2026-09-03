import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { deliverReport } from "./index";
import { runRelayFlow } from "./transport";

const originalEnv = { ...process.env };
const temporaryDirectories: string[] = [];

afterEach(() => {
  process.env = { ...originalEnv };
  for (const directory of temporaryDirectories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

type StrictCall = {
  command: string;
  args: string[];
  cwd: string;
  env: Record<string, string | undefined>;
  input: string;
};

const CONTRACT_KEYS = [
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

function strictTransportFixture(failCommand?: "runtime-register" | "report", failCount = 0) {
  const directory = mkdtempSync(join(tmpdir(), "relay-flow-pi-transport-"));
  temporaryDirectories.push(directory);
  const callsPath = join(directory, "calls.jsonl");
  const attemptsPath = join(directory, "attempts");
  const executable = join(directory, "relay-flow");
  const expectedCwd = process.cwd();
  const expected = {
    RELAY_FLOW_HOME: directory,
    RELAY_FLOW_RUN_ID: "payments/basicFlow/PAY-101",
    RELAY_FLOW_WORKFLOW: "basicFlow",
    RELAY_FLOW_REPO: "payments",
    RELAY_FLOW_TICKET: "PAY-101",
    RELAY_FLOW_NODE: "implement",
    RELAY_FLOW_NODE_TYPE: "agent",
    RELAY_FLOW_NUDGE_PROMPT: "emit the complete report",
    RELAY_FLOW_NEXT_STEPS_JSON: JSON.stringify([
      { target: "review", when: "implementation complete" },
      { target: "end", when: "approved" },
    ]),
  };
  const script = `#!/usr/bin/env bun
import { appendFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";

const command = process.argv[2];
const args = process.argv.slice(2);
const expectedCommands = ["runtime-register", "report"];
if (process.argv.length !== 3 || !expectedCommands.includes(command)) process.exit(11);
if (process.cwd() !== process.env.RELAY_FLOW_TEST_EXPECTED_CWD) process.exit(12);

const contract = ${JSON.stringify(CONTRACT_KEYS)};
for (const key of contract) {
  if (process.env[key] !== process.env["RELAY_FLOW_TEST_EXPECTED_" + key]) process.exit(13);
}
if (process.env.RELAY_FLOW_NODE_VISIT_ID !== undefined) process.exit(14);

const input = await Bun.stdin.text();
let parsed;
try {
  parsed = JSON.parse(input);
} catch {
  process.exit(15);
}
if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) process.exit(16);

let attempts = 0;
if (existsSync(process.env.RELAY_FLOW_TEST_ATTEMPTS)) {
  attempts = Number(readFileSync(process.env.RELAY_FLOW_TEST_ATTEMPTS, "utf8")) || 0;
}
attempts++;
writeFileSync(process.env.RELAY_FLOW_TEST_ATTEMPTS, String(attempts));
appendFileSync(process.env.RELAY_FLOW_TEST_CALLS, JSON.stringify({
  command,
  args,
  cwd: process.cwd(),
  env: Object.fromEntries(contract.map((key) => [key, process.env[key]])),
  input,
}) + "\\n");

if (process.env.RELAY_FLOW_TEST_FAIL_COMMAND === command && attempts <= Number(process.env.RELAY_FLOW_TEST_FAIL_COUNT)) {
  process.stderr.write("temporary relay-flow failure");
  process.exit(17);
}
`;
  writeFileSync(executable, script, { mode: 0o700 });
  chmodSync(executable, 0o700);
  process.env.PATH = `${directory}:${originalEnv.PATH ?? ""}`;
  process.env.RELAY_FLOW_TEST_CALLS = callsPath;
  process.env.RELAY_FLOW_TEST_ATTEMPTS = attemptsPath;
  process.env.RELAY_FLOW_TEST_EXPECTED_CWD = expectedCwd;
  process.env.RELAY_FLOW_TEST_FAIL_COMMAND = failCommand ?? "";
  process.env.RELAY_FLOW_TEST_FAIL_COUNT = String(failCount);
  process.env.RELAY_FLOW_HOME = expected.RELAY_FLOW_HOME;
  for (const key of CONTRACT_KEYS) {
    process.env[key] = expected[key];
    process.env[`RELAY_FLOW_TEST_EXPECTED_${key}`] = expected[key];
  }
  return { directory, executable, callsPath, expectedCwd, expected };
}

function strictCalls(path: string): StrictCall[] {
  if (!existsSync(path)) return [];
  const text = readFileSync(path, "utf8").trim();
  if (!text) return [];
  return text.split("\n").map((line) => JSON.parse(line) as StrictCall);
}

const report = {
  runId: "payments/basicFlow/PAY-101",
  node: "implement",
  reportId: "pi-session-1:assistant-entry-1",
  report: {
    status: "success" as const,
    nextStep: "end",
    summary: {
      completed: "line one\nline two",
      commits: "abc123",
      notCompleted: "None",
      issuesDiscovered: "None",
      verification: "go test ./...",
      notes: "None",
    },
    feedback: {
      reasonForNextStep: "None",
      requiredActions: "None",
      relevantContext: "None",
      expectedResult: "None",
    },
  },
};

describe("Pi relay-flow transport command shape", () => {
  test("writes one unchanged report JSON object to stdin", async () => {
    const fixture = strictTransportFixture();
    const payload = JSON.stringify(report);

    await runRelayFlow("report", payload);

    const actual = strictCalls(fixture.callsPath);
    expect(actual).toHaveLength(1);
    expect(actual[0]).toMatchObject({
      command: "report",
      args: ["report"],
      cwd: fixture.expectedCwd,
      input: payload,
    });
    expect(actual[0].env).toEqual(Object.fromEntries(
      CONTRACT_KEYS.map((key) => [key, fixture.expected[key]]),
    ));
    expect(Object.keys(actual[0].env)).not.toContain("RELAY_FLOW_NODE_VISIT_ID");
  });

  test("retries through the command transport with identical JSON bytes", async () => {
    const fixture = strictTransportFixture("report", 2);
    const payload = JSON.stringify({ ...report, reportId: "pi-session-1:retry-entry" });
    const sent: string[] = [];

    await deliverReport(reportFor("pi-session-1:retry-entry"), {
      send: async (json) => {
        sent.push(json);
        await runRelayFlow("report", json);
        return { accepted: true, duplicate: false };
      },
      sleep: async () => {},
    });

    const actual = strictCalls(fixture.callsPath);
    expect(actual.map((call) => call.command)).toEqual(["report", "report", "report"]);
    expect(actual.map((call) => call.args)).toEqual([["report"], ["report"], ["report"]]);
    expect(actual.map((call) => call.input)).toEqual([payload, payload, payload]);
    expect(sent).toEqual([payload, payload, payload]);
  });

  test("treats a duplicate acknowledgement as success without another delivery", async () => {
    const fixture = strictTransportFixture();
    const payload = JSON.stringify(reportFor("pi-session-1:duplicate-entry"));
    let sendCount = 0;

    await deliverReport(reportFor("pi-session-1:duplicate-entry"), {
      send: async (json) => {
        sendCount++;
        expect(json).toBe(payload);
        await runRelayFlow("report", json);
        return { accepted: true, duplicate: true };
      },
      sleep: async () => {},
    });

    expect(sendCount).toBe(1);
    expect(strictCalls(fixture.callsPath)).toHaveLength(1);
  });
});

describe("strict relay-flow transport fake", () => {
  test("rejects incorrect cwd, environment, and argv", () => {
    const fixture = strictTransportFixture();
    const baseEnv = { ...process.env } as Record<string, string>;
    const wrongCwd = mkdtempSync(join(tmpdir(), "relay-flow-pi-wrong-cwd-"));
    temporaryDirectories.push(wrongCwd);

    const wrongCwdResult = spawnSync(fixture.executable, ["report"], {
      cwd: wrongCwd,
      env: baseEnv,
      input: "{}",
    });
    expect(wrongCwdResult.status).not.toBe(0);

    const wrongEnv = { ...baseEnv, RELAY_FLOW_NODE: "wrong-node" };
    const wrongEnvResult = spawnSync(fixture.executable, ["report"], {
      cwd: fixture.expectedCwd,
      env: wrongEnv,
      input: "{}",
    });
    expect(wrongEnvResult.status).not.toBe(0);

    const wrongArgvResult = spawnSync(fixture.executable, ["report", "extra-arg"], {
      cwd: fixture.expectedCwd,
      env: baseEnv,
      input: "{}",
    });
    expect(wrongArgvResult.status).not.toBe(0);
  });
});

function reportFor(reportId: string) {
  return { ...report, reportId };
}
