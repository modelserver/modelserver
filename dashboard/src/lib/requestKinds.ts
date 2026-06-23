// REQUEST_KINDS mirrors types.AllRequestKinds in
// internal/types/request_kind.go. Hard-coded in the frontend so kind
// dropdowns render with zero network roundtrips; the enum changes
// only when a new wire protocol is added, which is already a
// coordinated backend+frontend change.
//
// Shared between the project-scoped RequestsPage and the admin
// All-Requests page so the two dropdowns can't drift.
export const REQUEST_KINDS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "anthropic_messages", label: "Anthropic Messages" },
  { value: "anthropic_count_tokens", label: "Anthropic Count Tokens" },
  { value: "openai_chat_completions", label: "OpenAI Chat Completions" },
  { value: "openai_responses", label: "OpenAI Responses" },
  { value: "openai_responses_compact", label: "OpenAI Responses Compact" },
  { value: "openai_images_generations", label: "OpenAI Images Generations" },
  { value: "openai_images_edits", label: "OpenAI Images Edits" },
  { value: "google_generate_content", label: "Google Generate Content" },
] as const;
