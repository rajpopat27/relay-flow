import { describe, expect, test } from "bun:test";
import { parseReport } from "./index";

const complete = `STATUS: success
NEXT STEP: end
SUMMARY: implemented the handler
FEEDBACK: None`;

describe("parseReport", () => {
  test("parses the concise contract into the existing wire shape", () => {
    const r = parseReport(complete);
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.report).toEqual({
        status: "success",
        nextStep: "end",
        summary: {
          completed: "implemented the handler", commits: "None", notCompleted: "None",
          issuesDiscovered: "None", verification: "None", notes: "None",
        },
        feedback: {
          reasonForNextStep: "None", requiredActions: "None",
          relevantContext: "None", expectedResult: "None",
        },
      });
    }
  });

  test("parses labels with surrounding punctuation", () => {
    const punctuated = complete.replace(/^([A-Z][A-Z ]*):/gm, "- **$1**:");
    const r = parseReport(punctuated);
    expect(r.ok).toBe(true);
    if (r.ok) expect(r.report.summary.completed).toBe("implemented the handler");
  });

  for (const field of ["STATUS: success\n", "NEXT STEP: end\n", "SUMMARY: implemented the handler\n", "FEEDBACK: None"]) {
    test(`missing ${field.split(":")[0]} is invalid`, () => {
      expect(parseReport(complete.replace(field, "")).ok).toBe(false);
    });
  }

  test("unsupported STATUS value is invalid", () => {
    expect(parseReport(complete.replace("STATUS: success", "STATUS: done")).ok).toBe(false);
  });

  test("unknown and duplicate labels are invalid", () => {
    expect(parseReport(complete.replace("SUMMARY:", "X: value\nSUMMARY:")).ok).toBe(false);
    expect(parseReport(complete.replace("STATUS: success", "STATUS: success\nSTATUS: failure")).ok).toBe(false);
  });

  test("schema parser defers end-feedback and route semantics to the server", () => {
    const endFeedback = parseReport(complete.replace("FEEDBACK: None", "FEEDBACK: needs work"));
    expect(endFeedback.ok).toBe(true);
    if (endFeedback.ok) expect(endFeedback.report.feedback.requiredActions).toBe("needs work");
    expect(parseReport(complete.replace("NEXT STEP: end", "NEXT STEP: unknown")).ok).toBe(true);
    expect(parseReport(complete.replace("STATUS: success", "STATUS: failure")).ok).toBe(true);
  });

  test("normal routes may have detailed feedback", () => {
    const r = parseReport("STATUS: failure\nNEXT STEP: coder\nSUMMARY: first pass\nFEEDBACK: fix the failing test");
    expect(r.ok).toBe(true);
    if (r.ok) expect(r.report.feedback.requiredActions).toBe("fix the failing test");
  });

  test("ordinary prose and empty fields are invalid", () => {
    expect(parseReport("I finished the work, let me know what you think.").ok).toBe(false);
    expect(parseReport(complete.replace("SUMMARY: implemented the handler", "SUMMARY:")).ok).toBe(false);
  });

  test("multiline summary and feedback are preserved", () => {
    const multi = "STATUS: failure\nNEXT STEP: coder\nSUMMARY: implemented the handler\nTESTS: added coverage\nFEEDBACK: review these changes\nREPRO: go test ./...\n- update docs";
    const r = parseReport(multi);
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.report.summary.completed).toBe("implemented the handler\nTESTS: added coverage");
      expect(r.report.feedback.requiredActions).toBe("review these changes\nREPRO: go test ./...\n- update docs");
    }
  });
});
