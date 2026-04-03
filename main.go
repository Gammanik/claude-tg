package main

import (
	"flag"
	"log"
	"os"

	"github.com/joho/godotenv"
)

func main() {
	// Загружаем .env файл (игнорируем ошибки - переменные могут быть в окружении)
	godotenv.Load()

	// CLI флаги
	cliMode := flag.Bool("cli", false, "Интерактивный CLI режим")
	taskCmd := flag.String("task", "", "Выполнить задачу и выйти")
	selfCmd := flag.String("self", "", "Самомодификация (выполнить задачу на claude-tg)")
	askCmd := flag.String("ask", "", "Задать вопрос боту")
	historyCmd := flag.Bool("history", false, "Показать историю сообщений")
	statusCmd := flag.Bool("status", false, "Показать статус бота")
	flag.Parse()

	cfg := Config{
		TelegramToken: mustEnv("TELEGRAM_BOT_TOKEN"),
		AllowedChatID: mustEnv("TELEGRAM_CHAT_ID"),
		GitHubToken:   mustEnv("GITHUB_TOKEN"),
		DefaultOwner:  getEnv("GITHUB_DEFAULT_OWNER", "Gammanik"),
		DefaultRepo:   getEnv("GITHUB_DEFAULT_REPO", "PeerPack"),
		LLMProvider:   getEnv("LLM_PROVIDER", "deepseek"),
		DeepSeekKey:   os.Getenv("DEEPSEEK_API_KEY"),
		AnthropicKey:  os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIKey:     os.Getenv("OPENAI_API_KEY"), // для Whisper STT + TTS
		GroqKey:       os.Getenv("GROQ_API_KEY"),   // бесплатный Whisper
	}

	bot := NewBot(cfg)

	// CLI режимы
	cli := NewCLI(bot)

	// Инициализируем историю для CLI режима
	if *cliMode || *taskCmd != "" || *selfCmd != "" || *askCmd != "" || *historyCmd || *statusCmd {
		api, err := bot.initAPI()
		if err != nil {
			log.Fatal(err)
		}
		bot.api = api
		bot.history = NewMessageHistory(api, bot.chatID)
		bot.limits = NewUsageLimits()
	}

	switch {
	case *cliMode:
		log.Printf("🤖 claude-tg CLI mode (provider=%s)", cfg.LLMProvider)
		cli.RunInteractive()

	case *taskCmd != "":
		cli.runTask(*taskCmd, false)

	case *selfCmd != "":
		cli.runTask(*selfCmd, true)

	case *askCmd != "":
		cli.ask(*askCmd)

	case *historyCmd:
		cli.showHistory("")

	case *statusCmd:
		cli.showStatus()

	default:
		// Обычный режим бота
		log.Printf("🤖 claude-tg starting (provider=%s)", cfg.LLMProvider)
		if err := bot.Start(); err != nil {
			log.Fatal(err)
		}
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
