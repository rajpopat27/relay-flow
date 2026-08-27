// relay-flow OpenCode runtime plugin: the runtime half of the harness
// contract. Reads the last completed assistant message on idle, parses
// the complete report contract, nudges agent nodes on invalid output,
// stays silent for HITL, and retries the exact parsed report via
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
// output is nudged; invalid HITL output stays silent; neither path
// surfaces parser internals.
export type ParseResult = { ok: true; report: Report } | { ok: false };

// --- parseReport ---

// The complete report contract per specs/structured-node-reporting:
//   STATUS: success|failure
//   NEXT STEP: <target>
//   SUMMARY:
//     COMPLETED / COMMITS / NOT COMPLETED / ISSUES DISCOVERED / VERIFICATION / NOTES
//   FEEDBACK:
//     REASON FOR NEXT STEP / REQUIRED ACTIONS / RELEVANT CONTEXT / EXPECTED RESULT
// Multi-line field values continue until the next recognised label.
// Labels at line start; values follow the colon. "None" is the literal
// intentionally-empty marker and is preserved as-is.

const LABELS = [
  "STATUS",
  "NEXT STEP",
  "SUMMARY",
  "COMPLETED",
  "COMMITS",
  "NOT COMPLETED",
  "ISSUES DISCOVERED",
  "VERIFICATION",
  "NOTES",
  "FEEDBACK",
  "REASON FOR NEXT STEP",
  "REQUIRED ACTIONS",
  "RELEVANT CONTEXT",
  "EXPECTED RESULT",
] as const;

type Label = (typeof LABELS)[number];

interface RawFields {
  status?: string;
  nextStep?: string;
  summary?: Partial<Record<"completed" | "commits" | "notCompleted" | "issuesDiscovered" | "verification" | "notes", string>>;
  feedback?: Partial<Record<"reasonForNextStep" | "requiredActions" | "relevantContext" | "expectedResult", string>>;
}

function matchLabel(line: string): { label: Label; value: string } | null {
  // Longest labels first so "NEXT STEP" wins over any prefix collision.
  const sorted = [...LABELS].sort((a, b) => b.length - a.length);
  for (const label of sorted) {
    if (line === label + ":") {
      return { label, value: "" };
    }
    if (line.startsWith(label + ":")) {
      return { label, value: line.slice(label.length + 1).replace(/^\s+/, "") };
    }
  }
  return null;
}

export function parseReport(text: string): ParseResult {
  if (typeof text !== "string" || text.trim() === "") {
    return { ok: false };
  }
  const lines = text.split("\n");
  const fields: RawFields = {};
  let currentLabel: Label | null = null;
  let currentValue: string[] = [];

  for (const raw of lines) {
    const line = raw.replace(/\s+$/, "");
    const m = matchLabel(line);
    if (m) {
      // Flush previous label's buffered value.
      if (currentLabel !== null) {
        const value = currentValue.join("\n").trim();
        assign(fields, currentLabel, value);
      }
      currentLabel = m.label;
      currentValue = m.value === "" ? [] : [m.value];
      continue;
    }
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

  // Validate required fields.
  const status = (fields.status ?? "").trim().toLowerCase();
  if (status !== "success" && status !== "failure") return { ok: false };
  const nextStep = (fields.nextStep ?? "").trim();
  if (nextStep === "") return { ok: false };
  const s = fields.summary ?? {};
  const f = fields.feedback ?? {};
  for (const v of [s.completed, s.commits, s.notCompleted, s.issuesDiscovered, s.verification, s.notes]) {
    if (v === undefined || v.trim() === "") return { ok: false };
  }
  for (const v of [f.reasonForNextStep, f.requiredActions, f.relevantContext, f.expectedResult]) {
    if (v === undefined || v.trim() === "") return { ok: false };
  }

  return {
    ok: true,
    report: {
      status: status as "success" | "failure",
      nextStep,
      summary: {
        completed: s.completed!.trim(),
        commits: s.commits!.trim(),
        notCompleted: s.notCompleted!.trim(),
        issuesDiscovered: s.issuesDiscovered!.trim(),
        verification: s.verification!.trim(),
        notes: s.notes!.trim(),
      },
      feedback: {
        reasonForNextStep: f.reasonForNextStep!.trim(),
        requiredActions: f.requiredActions!.trim(),
        relevantContext: f.relevantContext!.trim(),
        expectedResult: f.expectedResult!.trim(),
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
    case "COMPLETED":
      (fields.summary ??= {}).completed = value;
      return;
    case "COMMITS":
      (fields.summary ??= {}).commits = value;
      return;
    case "NOT COMPLETED":
      (fields.summary ??= {}).notCompleted = value;
      return;
    case "ISSUES DISCOVERED":
      (fields.summary ??= {}).issuesDiscovered = value;
      return;
    case "VERIFICATION":
      (fields.summary ??= {}).verification = value;
      return;
    case "NOTES":
      (fields.summary ??= {}).notes = value;
      return;
    case "REASON FOR NEXT STEP":
      (fields.feedback ??= {}).reasonForNextStep = value;
      return;
    case "REQUIRED ACTIONS":
      (fields.feedback ??= {}).requiredActions = value;
      return;
    case "RELEVANT CONTEXT":
      (fields.feedback ??= {}).relevantContext = value;
      return;
    case "EXPECTED RESULT":
      (fields.feedback ??= {}).expectedResult = value;
      return;
    case "SUMMARY":
    case "FEEDBACK":
      // Section headers carry no value.
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
  // HITL reports require a matching completed Question reply observed by the
  // runtime wrapper. Agent nodes ignore this field.
  hitlAuthorized?: boolean;
  // report seam: when provided and the parsed report is valid, invoked
  // with the parsed report; the caller delivers via deliverReport.
  report?: (report: Report) => Promise<void>;
}

// handleIdle implements the nudge policy:
//   agent + invalid -> send the exact report contract through the session API
//   agent + valid -> report (if a report sink is wired) and do not nudge
//   hitl + valid + matching Question approval -> report
//   hitl + valid without approval -> ask for Question approval
//   hitl + invalid after approval -> ask for a corrected report
//   hitl + invalid without approval -> silence
//   aborted turn -> no action
export async function handleIdle(input: IdleInput): Promise<void> {
  if (input.lastMessageCompleted === false) {
    return;
  }
  const parsed = parseReport(input.lastMessage);
  if (parsed.ok) {
    if (input.nodeType === "hitl" && input.hitlAuthorized !== true) {
      await input.session.sendPrompt(HITL_APPROVAL_REQUIRED_PROMPT);
      return;
    }
    if (input.report) {
      await input.report(parsed.report);
    }
    return;
  }
  if (input.nodeType === "agent") {
    await input.session.sendPrompt(INVALID_REPORT_PROMPT);
    return;
  }
  if (input.hitlAuthorized === true) {
    await input.session.sendPrompt(HITL_APPROVED_INVALID_REPORT_PROMPT);
  }
}

export const HITL_APPROVAL_REQUIRED_PROMPT = `Your report was not submitted because it was not approved by the user. Present the complete report through OpenCode's Question tool with exactly two options: Approve and Reject.`;

export const HITL_APPROVED_INVALID_REPORT_PROMPT = `The report approved by the user did not match the required contract. Regenerate it, present it through the Question tool with Approve and Reject, and output it only after a new approval.`;

export const INVALID_REPORT_PROMPT = `Your last message did not contain a complete, valid report.
Reply using this exact contract:

STATUS: success | failure
NEXT STEP: <one valid node name>

SUMMARY:
COMPLETED: <text or None>
COMMITS: <commit IDs or None>
NOT COMPLETED: <text or None>
ISSUES DISCOVERED: <text or None>
VERIFICATION: <text or None>
NOTES: <text or None>

FEEDBACK:
REASON FOR NEXT STEP: <text or None>
REQUIRED ACTIONS: <text or None>
RELEVANT CONTEXT: <text or None>
EXPECTED RESULT: <text or None>`;

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
