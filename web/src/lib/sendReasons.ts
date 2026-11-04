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

/** A job's result for display: the refusal in words when known, else its code. */
export function jobResult(job: Pick<Job, "code" | "reason">): string {
  const why = refusalText(job.reason);
  if (why) return `Not sent: ${why}.`;
  return job.code ? job.code.replaceAll("_", " ") : "";
}
