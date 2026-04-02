package main

import (
	"log"
	"os"

	"github.com/gammanik/peerpack-bot/internal/agent"
	"github.com/gammanik/peerpack-bot/internal/github"
	"github.com/gammanik/peerpack-bot/internal/telegram"
)

func main() {
	cfg := &Config{
		TelegramToken:  mustEnv("TELEGRAM_BOT_TOKEN"),
		AllowedChatID:  mustEnv("TELEGRAM_CHAT_ID"),   // твой chat_id: 155741924
		GitHubToken:    mustEnv("GITHUB_TOKEN"),
		GitHubOwner:    getEnv("GITHUB_OWNER", "Gammanik"),
		GitHubRepo:     getEnv("GITHUB_REPO", "PeerPack"),
		LLMProvider:    getEnv("LLM_PROVIDER", "deepseek"), // "deepseek" | "claude"
		DeepSeekAPIKey: os.Getenv("DEEPSEEK_API_KEY"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
	}

	gh := github.NewClient(cfg.GitHubToken, cfg.GitHubOwner, cfg.GitHubRepo)
	ag := agent.NewAgent(agent.Config{
		Provider:        cfg.LLMProvider,
		DeepSeekKey:     cfg.DeepSeekAPIKey,
		AnthropicKey:    cfg.AnthropicAPIKey,
		GitHubClient:    gh,
	})

	bot := telegram.NewBot(telegram.Config{
		Token:         cfg.TelegramToken,
		AllowedChatID: cfg.AllowedChatID,
		Agent:         ag,
		GitHubClient:  gh,
	})

	log.Println("🤖 PeerPack bot starting...")
	if err := bot.Start(); err != nil {
		log.Fatal(err)
	}
}

type Config struct {
	TelegramToken   string
	AllowedChatID   string
	GitHubToken     string
	GitHubOwner     string
	GitHubRepo      string
	LLMProvider     string
	DeepSeekAPIKey  string
	AnthropicAPIKey string
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
