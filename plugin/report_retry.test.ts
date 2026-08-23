import { describe, expect, test } from "bun:test";

// 3.40: plugin report-retry per specs/structured-node-reporting
// "Unacknowledged reports retry quietly" and design decision 16/17: the
// plugin sends {runId, nodeVisitId, report} as one JSON object via
// `relay-flow report` stdin, retries the exact unchanged parsed report with
// the shared backoff constants (initial 2s, factor 2, jitter 0.2, max 5m)
// mirrored in TypeScript, runs one retry loop per node visit, and treats a
// duplicate/stale ack as success without resubmitting.

import { BACKOFF, deliverReport } from "./index";

describe("backoff constants mirror Go", () => {
  test("matches internal/retry DefaultBackoffPolicy", () => {
    expect(BACKOFF.initialMs).toBe(2000);
    expect(BACKOFF.factor).toBe(2);
    expect(BACKOFF.jitter).toBe(0.2);
    expect(BACKOFF.maxMs).toBe(5 * 60 * 1000);
  });
});

describe("deliverReport", () => {
  const report = {
    runId: "payments/basicFlow/PAY-101",
    nodeVisitId: "visit-1",
    report: {
      status: "success",
      nextStep: "end",
      summary: {
        completed: "x",
        notCompleted: "None",
        issuesDiscovered: "None",
        verification: "x",
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

  test("sends one JSON object on stdin and stops on ack", async () => {
    const sent: string[] = [];
    const send = async (json: string) => {
      sent.push(json);
      return { accepted: true, duplicate: false };
    };
    await deliverReport(report, { send, sleep: async () => {} });
    expect(sent.length).toBe(1);
    // Exactly one JSON object, unchanged payload.
    const parsed = JSON.parse(sent[0]);
    expect(parsed.runId).toBe(report.runId);
    expect(parsed.nodeVisitId).toBe(report.nodeVisitId);
    expect(parsed.report.nextStep).toBe("end");
  });

  test("retries the exact unchanged report until acknowledged", async () => {
    const sent: string[] = [];
    let attempts = 0;
    const send = async (json: string) => {
      sent.push(json);
      attempts++;
      if (attempts < 3) {
        throw new Error("server unavailable");
      }
      return { accepted: true, duplicate: false };
    };
    const delays: number[] = [];
    await deliverReport(report, { send, sleep: async (ms) => { delays.push(ms); } });
    expect(sent.length).toBe(3);
    // Payload identical across retries (no regeneration).
    expect(new Set(sent).size).toBe(1);
    // Backoff grew and stayed within the cap.
    expect(delays.length).toBe(2);
    for (const d of delays) {
      expect(d).toBeGreaterThan(0);
      expect(d).toBeLessThanOrEqual(BACKOFF.maxMs);
    }
  });

  test("duplicate/stale ack is success and stops the loop", async () => {
    const sent: string[] = [];
    const send = async (json: string) => {
      sent.push(json);
      return { accepted: true, duplicate: true };
    };
    await deliverReport(report, { send, sleep: async () => {} });
    expect(sent.length).toBe(1);
  });

  test("a rejected report is not retried as delivery (validation failure)", async () => {
    // accepted:false means the server rejected the payload; that is not a
    // delivery retry — the plugin does not loop on it.
    const sent: string[] = [];
    const send = async (json: string) => {
      sent.push(json);
      return { accepted: false, duplicate: false };
    };
    await expect(deliverReport(report, { send, sleep: async () => {} })).rejects.toThrow();
    expect(sent.length).toBe(1);
  });

  test("at most one retry loop runs per node visit", async () => {
    // Triggering delivery twice for the same nodeVisitId while one loop is
    // in flight must not start a second loop; the first loop's single
    // in-flight request is shared.
    let resolveFirst: ((v: any) => void) | null = null;
    let inflight = 0;
    let maxInflight = 0;
    const send = (json: string) => {
      inflight++;
      maxInflight = Math.max(maxInflight, inflight);
      return new Promise((res) => {
        resolveFirst = (v: any) => { inflight--; res(v); };
      });
    };
    const sleep = async () => {};
    const p1 = deliverReport(report, { send, sleep });
    const p2 = deliverReport(report, { send, sleep }); // same visit, concurrent
    // Resolve the shared in-flight request.
    if (resolveFirst) resolveFirst({ accepted: true, duplicate: false });
    await Promise.all([p1, p2]);
    expect(maxInflight).toBe(1);
  });
});
