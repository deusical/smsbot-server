export type CaptureGap = {
  conversation: string;
  since: string;
  until: string;
  reason: string;
  state: string;
  checked_at?: string;
  retry_at?: string;
  kind?: string;
  observed_at?: string;
  pages?: number;
  checked?: number;
  last_progress_at?: string;
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
  /** The people in a group, other than you. */
  members?: { id: string; name?: string }[];
  /** Names exactly who is in a group; sending to it checks this. */
  roster?: string;
  /** For a group: who may use the bot there, and the roster approved for them. */
  enrolled_members?: string[];
  bot_roster?: string;
  shared_ai?: boolean;
  writable: boolean;
  enrolled: boolean;
  last_message_at?: string;
  unread?: boolean;
  pinned?: boolean;
  preview?: string;
  preview_from_me?: boolean;
};
export type Job = {
  preparation_stage?: string;
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
  /** The command's steps and their progress, with each AI step's usage. */
  steps?: JobStep[];
};
export type JobStep = {
  id: string;
  module: string;
  done: boolean;
  used?: boolean;
  model?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  cost?: number;
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
export type MessageCapabilities = {
  reply: boolean;
  react: boolean;
  edit: boolean;
  version: string;
  pending?: string;
  reason?: string;
  edit_reason: string;
};
export type Quote = {
  id?: string;
  text: string;
  direction?: string;
  available: boolean;
};
export type Message = {
  pending_action?: string;
  protocol?: string;
  reactions?: {
    emoji: string;
    count: number;
    mine: boolean;
    unsupported?: boolean;
  }[];
  quote?: Quote;
  revision?: number;
  attachments?: Attachment[];
  id: string;
  direction: string;
  at: string;
  text: string;
  origin?: string;
  status?: string;
  /** Who sent an incoming message in a group. */
  sender?: string;
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
  /** Include group conversations when scope is "all". */
  groups?: boolean;
  automatic_replies: boolean;
  mark_read_when_viewed?: boolean;
  revision: number;
};
/** A conversation whose exact identity is known: one to send to, or a group. */
export const collectable = (c: Conversation) =>
  !!c.participant_id && (c.writable || c.group);
export function inboxAllows(
  s: InboxSettings | null | undefined,
  c: Conversation,
): boolean {
  return (
    !!s?.enabled &&
    collectable(c) &&
    (s.scope === "all"
      ? !c.group || !!s.groups
      : s.conversations.some(
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
export type KindSchema = {
  type: string;
  title?: string;
  ui?: string;
  default?: unknown;
  minimum?: number;
  maximum?: number;
  effective_maximum?: number;
  enum?: string[];
  unit?: string;
  properties?: Record<string, KindSchema>;
};
export type CommandKind = {
  id: string;
  title: string;
  legacy_type: Command["type"];
  version: number;
  threads: boolean;
  retry_class: string;
  resource_class: string;
  schema: KindSchema;
};
export type Command = {
  kind?: string;
  settings?: Record<string, unknown>;
  revision?: number;
  execution_minutes?: number;
  name: string;
  description: string;
  instructions: string;
  type: "ai" | "fixed" | "download";
  enabled: boolean;
  model: string;
  profile_id: string;
  memory: boolean;
  approval: boolean;
  allowed_conversations: string[];
  generation?: Generation;
  /** The steps of a kind "pipeline" command. */
  pipeline?: Pipeline;
};
export type ModulePort = {
  name: string;
  title: string;
  types: string[];
  required?: boolean;
};
export type CommandModule = {
  id: string;
  version: number;
  title: string;
  description: string;
  inputs: ModulePort[];
  outputs: ModulePort[];
  effect: "pure" | "local" | "paid" | "sink";
  retry_class: string;
  resource_class: string;
  immediate?: boolean;
  long_running?: boolean;
  settings: KindSchema;
};
export type PipelineStep = {
  id: string;
  module: string;
  version?: number;
  inputs?: Record<string, string>;
  settings?: Record<string, unknown>;
};
export type Pipeline = {
  execution_minutes?: number;
  steps: PipelineStep[];
};
export type PreviewStep = {
  id: string;
  module: string;
  outputs: Record<string, string>;
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
  ai_photo_max_mb?: number;
  favorites: string[];
  model_generation: Record<string, Generation>;
  owner_commands?: boolean;
};
export type Configuration = {
  revision?: number;
  settings: Settings;
  commands: Command[];
  profiles: Profile[];
};
export type History = {
  messages: Message[];
  next_cursor?: string;
  change_cursor?: number;
};
export class ApiError extends Error {
  constructor(
    public status: number,
    code: string,
    /** Extra fields of the error response, such as an invalid pipeline's step. */
    public detail: Record<string, string> = {},
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
    signal:
      signal ?? (method === "GET" ? AbortSignal.timeout(25000) : undefined),
    credentials: "same-origin",
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
  const data = await response.json();
  if (!response.ok)
    throw new ApiError(response.status, data.error ?? "request_failed", data);
  return data as T;
}
