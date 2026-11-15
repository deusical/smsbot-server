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
    expect(jobResult({ code: "single_video_required" })).toBe(
      "The link is not a single video, for example a photo gallery",
    );
    expect(jobResult({ code: "video_too_long" })).toBe(
      "The video is too long to fit within the size limit",
    );
    expect(jobResult({ code: "size_limit" })).toBe(
      "The video couldn't be compressed small enough to send",
    );
    expect(jobResult({ code: "unsupported_platform" })).toBe("That link isn't from a supported site");
    expect(jobResult({ code: "audio_missing" })).toBe("Audio was lost while compressing the video");
    expect(jobResult({ code: "video_required" })).toBe("The downloaded file didn't contain a video");
    expect(jobResult({ code: "invalid_duration" })).toBe("Couldn't read the video's length");
    expect(jobResult({ code: "duration_changed" })).toBe(
      "The compressed video's length didn't match the original",
    );
    expect(jobResult({ code: "worker_unavailable" })).toBe("The video worker wasn't available");
    expect(jobResult({ code: "download_worker_unavailable" })).toBe(
      "No download worker is configured",
    );
    expect(jobResult({ code: "task_output_too_large" })).toBe("The finished file is too large to send");
    expect(jobResult({ code: "task_inputs_unavailable" })).toBe(
      "The input file from the previous step isn't available",
    );
    expect(jobResult({ code: "task_output_unavailable" })).toBe(
      "The worker didn't return a finished file",
    );
    expect(jobResult({ code: "execution_expired" })).toBe(
      "The request ran out of time before it could finish",
    );
    expect(jobResult({ code: "brand_new_code" })).toBe("brand new code");
  });
  it("shows an unlisted reason readably rather than hiding it", () => {
    expect(refusalText("some_new_check")).toBe("some new check");
    expect(refusalText(undefined)).toBe("");
  });
});
