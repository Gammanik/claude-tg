package main

// Config holds all runtime configuration
type Config struct {
	TelegramToken string
	AllowedChatID string

	GitHubToken  string
	DefaultOwner string
	DefaultRepo  string

	LLMProvider  string // "deepseek" | "claude"
	DeepSeekKey  string
	AnthropicKey string

	// Voice
	OpenAIKey string // Whisper STT + TTS (платный но дёшево)
	GroqKey   string // Whisper STT (бесплатный тир — лучше для старта)
}
