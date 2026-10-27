export type CaptureGap = {
 conversation: string; since: string; until: string; reason: string;
 state: string; checked_at?: string; retry_at?: string; kind?: string; observed_at?: string; pages?: number; checked?: number; last_progress_at?: string;
};
export type State = {
  api_version: number;
  inbox_capture_gap?: boolean;
  conversations_revision?: string;
  inbox_capture_gap_unknown?: boolean;
  inbox_capture_gaps?: CaptureGap[];
  paused: boolean;
  transport_ready: boolean;
  connection_state?: string;
  connection_retry_at?: string;
  transport: string;
  fixture: boolean;
  capabilities: string[];
};
export type Conversation = {
 archived?: boolean;
  id: string;
  participant_id: string;
  label: string;
  protocol: string;
  group: boolean;
  writable: boolean;
  enrolled: boolean;
  last_message_at?: string;
  unread?: boolean;
  pinned?: boolean;
  preview?: string;
  preview_from_me?: boolean;
};
export type Job = {
  id: string;
  conversation_id: string;
  command: string;
  state: string;
  created_at: string;
  transaction_id?: string;
  code?: string;
  model?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  cost?: number;
  requested_reasoning?: string;
};
export type Attachment = {
 preview_only?: boolean;
 archive_state?: string;
 archive_error?: string;
  id: string;
  name: string;
  mime: string;
  size: number;
};
export type MessageCapabilities = {reply: boolean; react: boolean; edit: boolean; version: string; pending?: string; reason?: string; edit_reason: string};
export type Quote = {id?: string; text: string; direction?: string; available: boolean};
export type Message = {
 pending_action?: string;
 protocol?: string;
 reactions?: {emoji: string; count: number; mine: boolean; unsupported?: boolean}[];
 quote?: Quote;
 revision?: number;
  attachments?: Attachment[];
  id: string;
  direction: string;
  at: string;
  text: string;
  origin?: string;
  status?: string;
};
export type AttachmentLimits = {
  max_files: number;
  max_file_mb: number;
  max_total_mb: number;
};
export const defaultAttachmentLimits: AttachmentLimits = {
  max_files: 4,
  max_file_mb: 10,
  max_total_mb: 20,
};
export type InboxSettings = {
 archive_retention_days?: number;
  attachment_limits?: AttachmentLimits;
  enabled: boolean;
  scope: "all" | "selected";
  conversations: { conversation: string; participant: string }[];
  load_older: boolean;
  automatic_replies: boolean;
  mark_read_when_viewed?: boolean;
  revision: number;
};
export function inboxAllows(
  s: InboxSettings | null | undefined,
  c: Conversation,
): boolean {
  return (
    !!s?.enabled &&
    !c.group &&
    c.writable &&
    (s.scope === "all" ||
      s.conversations.some(
        (b) => b.conversation === c.id && b.participant === c.participant_id,
      ))
  );
}
export type Generation = {
  reasoning: string;
  answer_length: string;
  tokens: number;
  temperature?: number;
};
export type Model = {
  id: string;
  name: string;
  provider?: string;
  remote_id?: string;
  context_length: number;
  parameters: string[];
  has_reasoning: boolean;
  reasoning_mandatory: boolean;
  reasoning_efforts: string[] | null;
  pricing_known?: boolean;
  input_modalities?: string[];
};
export type CompatibleProviderStatus = {
  base_url: string;
  configured: boolean;
};
export type Command = {
  name: string;
  description: string;
  instructions: string;
  type: "ai" | "fixed";
  enabled: boolean;
  model: string;
  profile_id: string;
  memory: boolean;
  approval: boolean;
  allowed_conversations: string[];
  generation?: Generation;
};
type Profile = {
  id: string;
  name: string;
  model: string;
  instructions: string;
};
export type Settings = {
  default_model: string;
  hourly_limit: number;
  daily_limit: number;
  daily_budget: number;
  quiet_enabled: boolean;
  quiet_start: number;
  quiet_end: number;
  timezone: string;
  memory_hours: number;
  retention_days: number;
  max_chars: number;
  favorites: string[];
  model_generation: Record<string, Generation>;
  owner_commands?: boolean;
};
export type Configuration = {
  settings: Settings;
  commands: Command[];
  profiles: Profile[];
};
export type History = { messages: Message[]; next_cursor?: string; change_cursor?: number };
export class ApiError extends Error {
  constructor(
    public status: number,
    code: string,
  ) {
    super(code);
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch("/api/v1" + path, {
    method,
    signal: signal ?? (method === "GET" ? AbortSignal.timeout(25000) : undefined),
    credentials: "same-origin",
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
  const data = await response.json();
  if (!response.ok)
    throw new ApiError(response.status, data.error ?? "request_failed");
  return data as T;
}
