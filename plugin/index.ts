// relay-flow OpenCode runtime plugin: the runtime half of the harness
// contract. Reads the last completed assistant message on idle, parses
// the concise report contract, nudges agent nodes on invalid output,
// corrects partial report-shaped HITL output while staying silent for
// ordinary, missing, or aborted HITL output, and retries the exact parsed
// report via
// `relay-flow report` stdin with the shared backoff constants until
// acknowledged. See specs/structured-node-reporting/spec.md and
// docs/structs-methods-interfaces.md lines 525-533.

// --- Shared backoff constants (mirror internal/retry DefaultBackoffPolicy) ---
// Specs: exponential backoff, 2s initial, factor 2, 20% jitter, 5m cap.
export const BACKOFF = {
  initialMs: 2000,
  factor: 2,
  jitter: 0.2,
  maxMs: 5 * 60 * 1000,
} as const;

// --- Types matching the Go wire contract ---

export interface Summary {
  completed: string;
  commits: string;
  notCompleted: string;
  issuesDiscovered: string;
  verification: string;
  notes: string;
}

export interface Feedback {
  reasonForNextStep: string;
  requiredActions: string;
  relevantContext: string;
  expectedResult: string;
}

export interface Report {
  status: "success" | "failure";
  nextStep: string;
  summary: Summary;
  feedback: Feedback;
}

export interface ReportEnvelope {
  runId: string;
  node: string;
  reportId: string;
  report: Report;
}

export interface ReportAck {
  accepted: boolean;
  duplicate: boolean;
}

// Parse outcome. ok=false carries no detail by design: invalid agent
// output is nudged; HITL policy classifies partial report-shaped output;
// neither path surfaces parser internals.
export type ParseResult = { ok: true; report: Report } | { ok: false };

// --- parseReport ---

// Four agent-facing fields are normalized to the existing internal JSON
// report. Multi-line summary and feedback values continue until the next
// recognized label; None is the intentionally-empty marker.
export const REPORT_FORMAT = process.env.RELAY_FLOW_REPORT_FORMAT || `STATUS: success | failure
NEXT STEP: <one valid route>
SUMMARY: <concise result>
FEEDBACK: <concise handoff, or None when NEXT STEP is end>`;

const LABELS = ["STATUS", "NEXT STEP", "SUMMARY", "FEEDBACK"] as const;

type Label = (typeof LABELS)[number];

const LABEL_SET = new Set<string>(LABELS);
const LABEL_PATTERN = /^[^A-Za-z0-9:]*([A-Z](?:[A-Z0-9 ]*[A-Z0-9])?)[^A-Za-z0-9:]*:(.*)$/;
const REPORT_LABEL_PATTERN = new RegExp(`^(${LABELS.join("|")}):`);

interface RawFields {
  status?: string;
  nextStep?: string;
  summary?: string;
  feedback?: string;
}

function matchLabel(line: string): { label: string; value: string } | null {
  const match = line.match(LABEL_PATTERN);
  if (!match) return null;
  return { label: match[1], value: match[2].replace(/^\s+/, "") };
}

export function parseReport(text: string): ParseResult {
  if (typeof text !== "string" || text.trim() === "") {
    return { ok: false };
  }
  const lines = text.split("\n");
  const fields: RawFields = {};
  const seen = new Set<Label>();
  let currentLabel: Label | null = null;
  let currentValue: string[] = [];

  for (const raw of lines) {
    const line = raw.replace(/\s+$/, "");
    const m = matchLabel(line);
    if (m && LABEL_SET.has(m.label)) {
      if (seen.has(m.label as Label)) {
        return { ok: false };
      }
      // Flush previous label's buffered value.
      if (currentLabel !== null) {
        const value = currentValue.join("\n").trim();
        assign(fields, currentLabel, value);
      }
      currentLabel = m.label as Label;
      seen.add(currentLabel);
      currentValue = m.value === "" ? [] : [m.value];
      continue;
    }
    // Uppercase headings within SUMMARY or FEEDBACK are ordinary content,
    // not report fields (for example, "REPRO: go test ./...").
    if (m && currentLabel !== "SUMMARY" && currentLabel !== "FEEDBACK") return { ok: false };
    if (currentLabel === null) {
      // Non-label content before any recognised label: not a report.
      if (line.trim() !== "") {
        return { ok: false };
      }
      continue;
    }
    currentValue.push(raw);
  }
  if (currentLabel !== null) {
    const value = currentValue.join("\n").trim();
    assign(fields, currentLabel, value);
  }

  // Validate that the complete contract was present before checking values.
  if (LABELS.some((label) => !seen.has(label))) return { ok: false };

  const status = (fields.status ?? "").trim().toLowerCase();
  if (status !== "success" && status !== "failure") return { ok: false };
  const nextStep = (fields.nextStep ?? "").trim();
  if (nextStep === "") return { ok: false };
  const summary = fields.summary?.trim();
  const feedback = fields.feedback?.trim();
  if (!summary || !feedback || (nextStep === "end" && feedback !== "None")) return { ok: false };

  return {
    ok: true,
    report: {
      status: status as "success" | "failure",
      nextStep,
      summary: {
        completed: summary, commits: "None", notCompleted: "None",
        issuesDiscovered: "None", verification: "None", notes: "None",
      },
      feedback: {
        reasonForNextStep: "None", requiredActions: feedback,
        relevantContext: "None", expectedResult: "None",
      },
    },
  };
}

function assign(fields: RawFields, label: Label, value: string) {
  switch (label) {
    case "STATUS":
      fields.status = value;
      return;
    case "NEXT STEP":
      fields.nextStep = value;
      return;
    case "SUMMARY":
      fields.summary = value;
      return;
    case "FEEDBACK":
      fields.feedback = value;
      return;
  }
}

// --- handleIdle ---

// Session seam: the OpenCode session API the plugin nudges through.
export interface IdleSession {
  sendPrompt(text: string): Promise<void>;
}

export interface IdleInput {
  nodeType: "agent" | "hitl";
  lastMessage: string;
  // lastMessageCompleted=false means the turn was aborted; do not parse
  // or nudge. Defaults to true when omitted (tests rely on this).
  lastMessageCompleted?: boolean;
  session: IdleSession;
  // report seam: when provided and the parsed report is valid, invoked
  // with the parsed report; the caller delivers via deliverReport. The TUI
  // wrapper supplies this only after the human selects Approve.
  report?: (report: Report) => Promise<void>;
}

// handleIdle implements the server-side nudge policy:
//   agent + invalid -> send the exact report contract through the session API
//   agent + valid -> report (if a report sink is wired) and do not nudge
//   hitl + partial report-shaped invalid -> send the same correction once;
//     approval stays with the harness entrypoint that owns the human UI
//   hitl + ordinary/missing/empty invalid -> silence; the human may still be away
//   hitl + valid -> report only when the caller supplies an approved sink
//   aborted turn -> no action
export async function handleIdle(input: IdleInput): Promise<void> {
  if (input.lastMessageCompleted === false) {
    return;
  }
  const parsed = parseReport(input.lastMessage);
  if (parsed.ok) {
    if (input.report) {
      await input.report(parsed.report);
    }
    return;
  }
  if (input.nodeType === "agent" || hitlOutcome(input.lastMessage).kind === "nudge") {
    await input.session.sendPrompt(INVALID_REPORT_PROMPT);
  }
}

// --- hitlOutcome ---

export type HitlOutcome =
  | { kind: "silent" }
  | { kind: "nudge" }
  | { kind: "approve"; report: Report };

// hitlOutcome classifies one completed HITL assistant output:
//   "silent"  missing, empty, or ordinary conversation: send nothing
//   "nudge"   invalid output with at least two distinct report labels:
//             send INVALID_REPORT_PROMPT once
//   "approve" valid report: the harness entrypoint asks the human to approve
// Aborted turns never reach here; the caller drops them before classifying.
export function hitlOutcome(text: string): HitlOutcome {
  const parsed = parseReport(text);
  if (parsed.ok) return { kind: "approve", report: parsed.report };
  if (typeof text !== "string" || text.trim() === "") return { kind: "silent" };

  const labels = new Set<string>();
  for (const line of text.split("\n")) {
    const match = line.match(REPORT_LABEL_PATTERN);
    if (match) labels.add(match[1]);
  }
  return labels.size >= 2 ? { kind: "nudge" } : { kind: "silent" };
}


export const INVALID_REPORT_PROMPT = `Your last message did not contain a complete, valid report.
Reply using this exact contract:

${REPORT_FORMAT}`;

// --- deliverReport ---

export interface DeliverOptions {
  // send writes one JSON object to `relay-flow report` stdin and resolves
  // with the parsed ack. Rejects on transport/server failure.
  send: (json: string) => Promise<ReportAck>;
  sleep: (ms: number) => Promise<void>;
  // Optional deterministic RNG for jitter (tests); defaults to Math.random.
  rand?: () => number;
}

// One unacknowledged report per run/node. Later report attempts are ignored
// until the current report is acknowledged.
const inFlight = new Map<string, Promise<void>>();

function backoffDelay(attempt: number, rand: () => number): number {
  // attempt is 0-based for the first retry sleep.
  const base = Math.min(BACKOFF.maxMs, BACKOFF.initialMs * Math.pow(BACKOFF.factor, attempt));
  const spread = base * BACKOFF.jitter;
  // Jitter in [-spread, +spread].
  const delta = (rand() * 2 - 1) * spread;
  const d = base + delta;
  // Clamp into (0, maxMs]; a 0ms delay would defeat the backoff.
  return Math.max(1, Math.min(BACKOFF.maxMs, d));
}

// deliverReport retries the exact parsed JSON (no regeneration) until
// acknowledged. An ack with duplicate:true is success. A response of
// accepted:false is a validation failure, not a delivery failure — the
// plugin throws rather than looping on it (the agent must fix output).
export async function deliverReport(env: ReportEnvelope, opts: DeliverOptions): Promise<void> {
  const key = `${env.runId}:${env.node}`;
  const existing = inFlight.get(key);
  if (existing) {
    return existing;
  }
  // Serialize the envelope ONCE; every retry sends the identical bytes.
  const payload = JSON.stringify(env);
  const p = (async () => {
    let attempt = 0;
    const rand = opts.rand ?? Math.random;
    for (;;) {
      try {
        const ack = await opts.send(payload);
        if (ack.accepted) {
          return;
        }
        // accepted:false -> validation rejection; do not retry.
        throw new Error("report rejected by server");
      } catch (err) {
        // Distinguish "rejected by server" (terminal) from transport
        // failure (retry). The terminal case is the Error we just threw.
        if (err instanceof Error && err.message === "report rejected by server") {
          throw err;
        }
        await opts.sleep(backoffDelay(attempt, rand));
        attempt++;
      }
    }
  })().finally(() => {
    inFlight.delete(key);
  });
  inFlight.set(key, p);
  return p;
}
