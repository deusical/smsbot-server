import { describe, expect, it } from "vitest";
import { jobResult, refusalText } from "./sendReasons";

describe("send refusal reasons", () => {
  it("explains a missing SIM in words", () => {
    expect(jobResult({ code: "not_submitted", reason: "sim_missing" })).toMatch(
      /^Not sent: your phone has no SIM chosen for this conversation\. .+\.$/,
    );
  });
  it("falls back to the code when no reason was kept", () => {
    expect(jobResult({ code: "not_submitted" })).toBe("not submitted");
    expect(jobResult({})).toBe("");
  });
  it("says why a worker task stopped", () => {
    expect(jobResult({ code: "progress_stalled" })).toMatch(/^The worker stopped getting data/);
    expect(jobResult({ code: "task_failed" })).toBe("The worker couldn't finish this task");
    expect(jobResult({ code: "brand_new_code" })).toBe("brand new code");
  });
  it("shows an unlisted reason readably rather than hiding it", () => {
    expect(refusalText("some_new_check")).toBe("some new check");
    expect(refusalText(undefined)).toBe("");
  });
});
