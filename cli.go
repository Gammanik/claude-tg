package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// CLI - интерфейс командной строки для взаимодействия с ботом
type CLI struct {
	bot *Bot
}

func NewCLI(bot *Bot) *CLI {
	return &CLI{bot: bot}
}

// RunInteractive - интерактивный режим (REPL)
func (c *CLI) RunInteractive() {
	o, r := c.bot.currentRepo()
	fmt.Printf("🤖 claude-tg CLI (repo: %s/%s)\n", o, r)
	fmt.Println("Пиши как в Telegram - бот автоматически определит что делать.")
	fmt.Println("Команды: /status, /history, /repo <owner/name>, /exit")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Обработка команд
		if strings.HasPrefix(line, "/") {
			c.handleCommand(line)
			continue
		}

		// Автоматическое определение типа сообщения (как в боте)
		c.routeCLI(line)
	}
}

func (c *CLI) handleCommand(line string) {
	parts := strings.SplitN(line, " ", 2)
	cmd := parts[0]
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}

	switch cmd {
	case "/exit", "/quit":
		os.Exit(0)

	case "/status":
		c.showStatus()

	case "/history":
		c.showHistory(arg)

	case "/repo":
		if arg == "" {
			o, r := c.bot.currentRepo()
			fmt.Printf("📦 Текущий репозиторий: %s/%s\n", o, r)
		} else {
			parts := strings.Split(arg, "/")
			if len(parts) != 2 {
				fmt.Println("❌ Формат: owner/repo")
				return
			}
			c.bot.repoMu.Lock()
			c.bot.owner, c.bot.repo = parts[0], parts[1]
			c.bot.repoMu.Unlock()
			fmt.Printf("✅ Переключено на %s/%s\n", parts[0], parts[1])
		}

	default:
		fmt.Printf("❌ Неизвестная команда: %s\n", cmd)
		fmt.Println("Доступные: /status /history /repo /exit")
	}
}

func (c *CLI) routeCLI(text string) {
	lower := strings.ToLower(text)

	// Переключение на свой репо
	if containsAny(lower, "себя", "себе", "yourself", "claude-tg", "самого бота") {
		c.bot.repoMu.Lock()
		c.bot.owner, c.bot.repo = "Gammanik", "claude-tg"
		c.bot.repoMu.Unlock()
		fmt.Println("🔧 Переключился на свой репозиторий: Gammanik/claude-tg")
	}

	// Определяем тип запроса
	if looksLikeTask(lower) {
		// Задача на выполнение
		isSelf := containsAny(lower, "себя", "себе", "yourself", "claude-tg", "самого бота")
		c.runTask(text, isSelf)
	} else {
		// Обычный вопрос
		c.ask(text)
	}
}

func (c *CLI) runTask(description string, selfModify bool) {
	o, r := c.bot.currentRepo()

	// Если selfModify - переключаемся на свой репо
	if selfModify {
		c.bot.repoMu.Lock()
		c.bot.owner, c.bot.repo = "Gammanik", "claude-tg"
		c.bot.repoMu.Unlock()
		o, r = c.bot.owner, c.bot.repo
		fmt.Printf("🔧 Самомодификация: %s/%s\n", o, r)
	}

	taskID := fmt.Sprintf("cli-%d", time.Now().UnixMilli())
	task := &Task{
		ID:          taskID,
		Description: description,
		Owner:       o,
		Repo:        r,
		Steps:       make(chan Step, 100),
		StartedAt:   time.Now(),
	}

	c.bot.tasksMu.Lock()
	c.bot.tasks[taskID] = task
	c.bot.tasksMu.Unlock()

	// Создаём агента с прогресс-трекером
	// Для CLI используем threadID=0 (основной чат)
	pt := NewProgressTracker(c.bot, description, o, r, 0)
	agent := NewAgent(c.bot.cfg, o, r).WithProgress(pt).WithBot(c.bot, 0)

	// Запускаем агента
	go agent.Run(task)

	// Стримим прогресс в терминал
	c.streamTaskProgress(task, pt)

	// Восстанавливаем репо если было selfModify
	if selfModify {
		c.bot.repoMu.Lock()
		c.bot.owner, c.bot.repo = o, r
		c.bot.repoMu.Unlock()
	}
}

func (c *CLI) streamTaskProgress(task *Task, pt *ProgressTracker) {
	lastThought := ""

	for step := range task.Steps {
		switch step.Type {
		case StepThought:
			thought := truncate(step.Content, 100)
			if thought != lastThought {
				fmt.Printf("💭 %s\n", thought)
				lastThought = thought
			}

		case StepAction:
			fmt.Printf("⚡ %s\n", step.Content)

		case StepResult:
			fmt.Printf("✓ %s\n", truncate(step.Content, 150))

		case StepPR:
			fmt.Printf("\n🚀 PR #%d создан: %s\n", step.PRNumber, step.PRURL)

		case StepError:
			fmt.Printf("\n❌ Ошибка: %s\n", step.Content)
			c.bot.removeTask(task.ID)
			return

		case StepDone:
			fmt.Printf("\n✅ Задача завершена: %s\n", truncate(step.Content, 200))

			// Показываем финальную статистику
			if pt != nil && pt.tokens != nil {
				fmt.Println("\n" + pt.tokens.Format())
			}

			elapsed := time.Since(task.StartedAt)
			fmt.Printf("\n⏱ Время выполнения: %s\n", fmtDuration(elapsed.Round(time.Second)))

			c.bot.removeTask(task.ID)
			return
		}
	}
}

func (c *CLI) ask(question string) {
	o, r := c.bot.currentRepo()
	system := fmt.Sprintf(
		`Ты AI-ассистент разработчика Никиты. Репо: %s/%s. Отвечай кратко на русском.`, o, r)

	fmt.Print("🤔 ")

	answer, err := c.bot.streamClaude(system, question, func(partial string) {
		// Перезаписываем строку для live-стриминга в терминале
		fmt.Printf("\r🤔 %s", truncate(partial, 200))
	})

	if err != nil {
		fmt.Printf("\n❌ %v\n", err)
		return
	}

	fmt.Printf("\r✅ %s\n", answer)
}

func (c *CLI) showHistory(query string) {
	if c.bot.history == nil {
		fmt.Println("❌ История недоступна")
		return
	}

	var messages []HistoryMessage
	if query == "" {
		messages = c.bot.history.GetRecentMessages(20)
		fmt.Println("📜 Последние 20 сообщений:")
	} else {
		messages = c.bot.history.Search(query, 10)
		fmt.Printf("🔍 Результаты поиска \"%s\":\n", query)
	}

	if len(messages) == 0 {
		fmt.Println("Нет сообщений")
		return
	}

	fmt.Println()
	for _, msg := range messages {
		timestamp := msg.Timestamp.Format("02.01 15:04")
		threadInfo := ""
		if msg.ThreadID != 0 {
			threadInfo = fmt.Sprintf(" [тред %d]", msg.ThreadID)
		}
		fmt.Printf("[%s]%s %s: %s\n", timestamp, threadInfo, msg.From, truncate(msg.Text, 100))
	}
}

func (c *CLI) showStatus() {
	o, r := c.bot.currentRepo()

	c.bot.tasksMu.Lock()
	taskCount := len(c.bot.tasks)
	c.bot.tasksMu.Unlock()

	c.bot.remindersMu.Lock()
	reminderCount := len(c.bot.reminders)
	c.bot.remindersMu.Unlock()

	fmt.Printf("📊 Статус бота\n")
	fmt.Printf("  Репо: %s/%s\n", o, r)
	fmt.Printf("  Активных задач: %d\n", taskCount)
	fmt.Printf("  Напоминаний: %d\n", reminderCount)

	// Лимиты
	if c.bot.limits != nil {
		ok, warning := c.bot.limits.CheckLimit(0)
		if ok && warning == "" {
			fmt.Printf("  Лимиты: ✅ OK\n")
		} else if warning != "" {
			fmt.Printf("  Лимиты: ⚠️  %s\n", warning)
		}
	}
}
