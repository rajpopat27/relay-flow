import { describe, expect, test } from "bun:test";

// 3.38: plugin parse tests per specs/structured-node-reporting "Every node
// report follows the complete contract". The plugin reads the last
// completed assistant message on idle and parses the complete report
// contract (STATUS / NEXT STEP / all SUMMARY and FEEDBACK sections);
// missing or malformed sections are invalid.
//
// The parser is implemented by section 4.16 (plugin runtime). These tests
// import the parse entry point the plugin exports for tests.

import { parseReport } from "./index";

const complete = `
STATUS: success
NEXT STEP: end

SUMMARY:
COMPLETED: implemented the handler
COMMITS: abc123
NOT COMPLETED: None
ISSUES DISCOVERED: None
VERIFICATION: ran the tests
NOTES: None

FEEDBACK:
REASON FOR NEXT STEP: None
REQUIRED ACTIONS: None
RELEVANT CONTEXT: None
EXPECTED RESULT: None
`;

describe("parseReport", () => {
  test("parses a complete report", () => {
    const r = parseReport(complete);
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.report.status).toBe("success");
      expect(r.report.nextStep).toBe("end");
      expect(r.report.summary.completed).toContain("implemented");
      expect(r.report.summary.commits).toBe("abc123");
      expect(r.report.summary.notCompleted).toBe("None");
      expect(r.report.feedback.reasonForNextStep).toBe("None");
    }
  });

  test("parses labels with surrounding punctuation", () => {
    const punctuated = complete.replace(
      /^([A-Z][A-Z ]*):/gm,
      "- **$1**:",
    );
    const r = parseReport(punctuated);
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.report.status).toBe("success");
      expect(r.report.summary.completed).toBe("implemented the handler");
      expect(r.report.feedback.expectedResult).toBe("None");
    }
  });

  test("missing STATUS is invalid", () => {
    const r = parseReport(complete.replace("STATUS: success\n", ""));
    expect(r.ok).toBe(false);
  });

  test("missing NEXT STEP is invalid", () => {
    const r = parseReport(complete.replace("NEXT STEP: end\n", ""));
    expect(r.ok).toBe(false);
  });

  test("missing a SUMMARY subsection is invalid", () => {
    const r = parseReport(complete.replace("VERIFICATION: ran the tests\n", ""));
    expect(r.ok).toBe(false);
  });

  test("missing COMMITS is invalid", () => {
    const r = parseReport(complete.replace("COMMITS: abc123\n", ""));
    expect(r.ok).toBe(false);
  });

  test("missing a FEEDBACK subsection is invalid", () => {
    const r = parseReport(complete.replace("EXPECTED RESULT: None\n", ""));
    expect(r.ok).toBe(false);
  });

  test("unsupported STATUS value is invalid", () => {
    const r = parseReport(complete.replace("STATUS: success", "STATUS: done"));
    expect(r.ok).toBe(false);
  });

  test("unknown labels are invalid", () => {
    const r = parseReport(complete.replace(
      "COMMITS: abc123",
      "X: value\nCOMMITS: abc123",
    ));
    expect(r.ok).toBe(false);
  });

  test("duplicate labels are invalid", () => {
    const r = parseReport(complete.replace(
      "STATUS: success",
      "STATUS: success\nSTATUS: failure",
    ));
    expect(r.ok).toBe(false);
  });

  test("literal None is accepted as intentionally empty", () => {
    const r = parseReport(complete);
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.report.summary.issuesDiscovered).toBe("None");
    }
  });

  test("an aborted/ordinary-prose message is not a report", () => {
    const r = parseReport("I finished the work, let me know what you think.");
    expect(r.ok).toBe(false);
  });

  test("multiline fields are preserved", () => {
    const multi = complete.replace(
      "COMPLETED: implemented the handler",
      "COMPLETED: implemented the handler\n- added tests\n- updated docs"
    );
    const r = parseReport(multi);
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.report.summary.completed).toBe(
        "implemented the handler\n- added tests\n- updated docs",
      );
    }
  });
});
