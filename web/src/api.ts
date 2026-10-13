export type State = {
  api_version: number;
  paused: boolean;
  transport_ready: boolean;
  transport: string;
  fixture: boolean;
  capabilities: string[];
};
export type Conversation = {
  id: string;
  participant_id: string;
  label: string;
  protocol: string;
  group: boolean;
  writable: boolean;
  enrolled: boolean;
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
export type Message = {
  id: string;
  direction: string;
  at: string;
  text: string;
};
export type Client = { id: string; label: string; token?: string };
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
export type Profile = {
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
};
export type Configuration = {
  settings: Settings;
  commands: Command[];
  profiles: Profile[];
};
export type History = { messages: Message[]; next_cursor?: string };
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
): Promise<T> {
  const response = await fetch("/api/v1" + path, {
    method,
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
