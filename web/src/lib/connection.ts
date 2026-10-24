import type { State } from "./api";

/** Short label for the phone connection state shown in the navigation rail. */
export function connectionLabel(status?: string): string {
  switch (status) {
    case "reconnecting": return "Reconnecting to phone";
    case "phone_unreachable": return "Waiting for phone";
    case "login_required": return "Phone login required";
    case "session_conflict": return "Messages session conflict";
    case "account_changed": return "Phone account changed";
    case "storage_error": return "Session could not be saved";
    case "connection_error": return "Phone connection needs attention";
    case "receive_overload": return "Phone event queue overloaded";
    default: return "Phone offline";
  }
}

/** One-line explanation of what an offline phone means for sending. */
export function connectionSummary(state: State): string {
  const status = state.connection_state;
  if (status && !["reconnecting", "phone_unreachable", "connected"].includes(status)) {
    return connectionLabel(status) + ". Sending is blocked. Resolve the issue and restart the service; review before going live.";
  }
  return state.paused
    ? "Reconnecting to your phone. Sending will stay paused."
    : "Reconnecting to your phone. Sending is blocked and will resume after verification. You can pause to prevent resuming.";
}
