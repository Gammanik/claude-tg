package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

func main() {
	// Загружаем .env файл
	godotenv.Load()

	cfg := Config{
		TelegramToken: mustEnv("TELEGRAM_BOT_TOKEN"),
		AllowedChatID: mustEnv("TELEGRAM_CHAT_ID"),
		GitHubToken:   mustEnv("GITHUB_TOKEN"),
		DefaultOwner:  getEnv("GITHUB_DEFAULT_OWNER", "Gammanik"),
		DefaultRepo:   getEnv("GITHUB_DEFAULT_REPO", "PeerPack"),
		LLMProvider:   getEnv("LLM_PROVIDER", "anthropic"),
		AnthropicKey:  os.Getenv("ANTHROPIC_API_KEY"),
		DeepSeekKey:   os.Getenv("DEEPSEEK_API_KEY"),
		OpenAIKey:     os.Getenv("OPENAI_API_KEY"),
		GroqKey:       os.Getenv("GROQ_API_KEY"),
	}

	log.Printf("🤖 claude-tg starting (provider=%s)", cfg.LLMProvider)

	bot := NewBot(cfg)
	if err := bot.Start(); err != nil {
		log.Fatal(err)
	}
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("required env var %q not set", k)
	}
	return v
}

func getEnv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
