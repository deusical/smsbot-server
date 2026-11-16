import type { Job } from "./api";

// Why the server refused to send, in words, keyed by the fixed reason codes in
// api/openapi.yaml (Job.reason). Each finishes "Not sent: …".
const reasons: Record<string, string> = {
  sim_missing:
    "your phone has no SIM chosen for this conversation. In Google Messages on the phone, set a default SIM for messages, or send one message here from the phone, then try again",
  outgoing_identity_changed:
    "the number your phone sends from in this conversation changed, for example after a SIM change. Open the conversation, then send again",
  baseline_missing:
    "this conversation hasn't been checked since the service started. Open it, then send again",
  participant_changed: "the person in this conversation changed",
  roster_changed: "the people in this group changed. Check who is in it, then send again",
  roster_mismatch: "the people in this group are not the ones you were shown. Check who is in it, then send again",
  conversation_ineligible: "this conversation is read-only or has no number to send from",
  conversation_lookup_failed: "your phone didn't answer when asked about this conversation",
  not_ready: "your phone wasn't connected",
  readiness_changed: "your phone disconnected while sending",
  context_done: "it timed out before reaching your phone",
  invalid_parts: "the message was empty or an attachment wasn't ready",
  reply_protocol_not_rcs: "replies need RCS, and this conversation uses SMS",
  reaction_protocol_not_rcs: "reactions need RCS, and this conversation uses SMS",
  reaction_target_missing: "the message to react to is gone",
  upstream_rejected: "Google Messages on your phone refused it",
};

/** The reason a request wasn't sent, in words, or "" when none was kept. */
export function refusalText(reason?: string): string {
  if (!reason) return "";
  return reasons[reason] ?? reason.replaceAll("_", " ");
}

// Why a worker task stopped, in words, keyed by the codes the worker reports.
const taskCodes: Record<string, string> = {
  task_failed: "The worker couldn't finish this task",
  progress_stalled:
    "The worker stopped getting data from the site for too long and gave up. Try the link again",
  process_failed: "The program that downloads or compresses the video failed",
  worker_unauthorized: "The worker refused the server's token",
  task_rejected: "The worker didn't accept this task",
  video_too_long: "The video is too long to fit within the size limit",
  size_limit: "The video couldn't be compressed small enough to send",
  single_video_required: "The link is not a single video, for example a photo gallery",
  unsupported_platform: "That link isn't from a supported site",
  audio_missing: "Audio was lost while compressing the video",
  video_required: "The downloaded file didn't contain a video",
  invalid_duration: "Couldn't read the video's length",
  duration_changed: "The compressed video's length didn't match the original",
  worker_unavailable: "The video worker wasn't available",
  download_worker_unavailable: "No download worker is configured",
  task_output_too_large: "The finished file is too large to send",
  task_inputs_unavailable: "The input file from the previous step isn't available",
  task_output_unavailable: "The worker didn't return a finished file",
  execution_expired: "The request ran out of time before it could finish",
  budget_exceeded_or_unknown:
    "Daily AI spending limit reached, or an earlier request's cost wasn't reported",
  provider_empty_answer: "The AI model returned an empty answer",
  provider_token_limit_empty:
    "The AI model ran out of output tokens before writing an answer",
  model_unavailable: "The selected AI model isn't in the model catalog",
  provider_key_missing: "No API key is configured for the selected AI provider",
  generation_unsupported:
    "The selected AI model doesn't support these generation settings",
};

/** A job's result for display: the refusal in words when known, else its code. */
export function jobResult(job: Pick<Job, "code" | "reason">): string {
  const why = refusalText(job.reason);
  if (why) return `Not sent: ${why}.`;
  return job.code ? (taskCodes[job.code] ?? job.code.replaceAll("_", " ")) : "";
}
