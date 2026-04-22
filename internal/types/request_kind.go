package types

const (
	KindAnthropicMessages     = "anthropic_messages"
	KindAnthropicCountTokens  = "anthropic_count_tokens"
	KindOpenAIChatCompletions = "openai_chat_completions"
	KindOpenAIResponses       = "openai_responses"
	KindGoogleGenerateContent = "google_generate_content"
)

var AllRequestKinds = []string{
	KindAnthropicMessages,
	KindAnthropicCountTokens,
	KindOpenAIChatCompletions,
	KindOpenAIResponses,
	KindGoogleGenerateContent,
}

func IsValidRequestKind(s string) bool {
	for _, k := range AllRequestKinds {
		if k == s {
			return true
		}
	}
	return false
}
