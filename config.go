package main

// Config holds all runtime configuration
type Config struct {
	// Telegram
	TelegramToken string
	AllowedChatID string

	// GitHub
	GitHubToken  string
	DefaultOwner string
	DefaultRepo  string
	DirectCommit bool // Коммитить в main напрямую без PR

	// LLM Provider (anthropic | deepseek)
	LLMProvider  string
	AnthropicKey string
	DeepSeekKey  string

	// Voice (optional)
	OpenAIKey string // TTS (платный)
	GroqKey   string // STT бесплатный
}
