package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	api    *tgbotapi.BotAPI
	cfg    Config
	chatID int64

	// Текущий активный репо (меняется командой /repo)
	repoMu sync.RWMutex
	owner  string
	repo   string

	// Активные задачи
	tasksMu sync.Mutex
	tasks   map[string]*Task
}

func NewBot(cfg Config) *Bot {
	id, _ := strconv.ParseInt(cfg.AllowedChatID, 10, 64)
	return &Bot{
		cfg:    cfg,
		chatID: id,
		owner:  cfg.DefaultOwner,
		repo:   cfg.DefaultRepo,
		tasks:  make(map[string]*Task),
	}
}

func (b *Bot) Start() error {
	api, err := tgbotapi.NewBotAPI(b.cfg.TelegramToken)
	if err != nil {
		return fmt.Errorf("telegram init: %w", err)
	}
	b.api = api
	log.Printf("Authorized as @%s", api.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	for update := range api.GetUpdatesChan(u) {
		// Только наш chat
		if update.Message != nil && update.Message.Chat.ID != b.chatID {
			continue
		}
		if update.CallbackQuery != nil {
			go b.handleCallback(update.CallbackQuery)
			continue
		}
		if update.Message == nil {
			continue
		}
		go b.handleMessage(update.Message)
	}
	return nil
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	// Голосовое сообщение
	if msg.Voice != nil {
		b.handleVoice(msg)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	switch {
	case text == "/start" || text == "/help":
		b.send(helpText(b.currentRepo()))

	case text == "/status":
		b.handleStatus()

	case text == "/prs":
		b.handlePRs()

	case text == "/tasks":
		b.handleTaskList()

	case strings.HasPrefix(text, "/repo "):
		// /repo Gammanik/SkyFarm  — переключить репо
		b.handleSetRepo(strings.TrimPrefix(text, "/repo "))

	case strings.HasPrefix(text, "/cancel "):
		taskID := strings.TrimPrefix(text, "/cancel ")
		b.cancelTask(taskID)

	default:
		// Любой текст — задача для агента
		b.runTask(text)
	}
}

func (b *Bot) handleVoice(msg *tgbotapi.Message) {
	b.send("🎤 Распознаю голосовое...")

	// Скачиваем OGG файл
	fileURL, err := b.api.GetFileDirectURL(msg.Voice.FileID)
	if err != nil {
		b.send(fmt.Sprintf("❌ Не смог получить файл: %v", err))
		return
	}

	// Транскрибируем через Groq Whisper (бесплатно) или OpenAI
	voice := NewVoice(b.cfg)
	text, err := voice.Transcribe(fileURL)
	if err != nil {
		b.send(fmt.Sprintf("❌ Транскрипция не удалась: %v", err))
		return
	}

	b.send(fmt.Sprintf("📝 Распознал: _%s_\n\nЗапускаю...", text))
	b.runTask(text)
}

func (b *Bot) runTask(description string) {
	owner, repo := b.currentRepo()
	taskID := fmt.Sprintf("%d", time.Now().Unix())

	task := &Task{
		ID:          taskID,
		Description: description,
		Owner:       owner,
		Repo:        repo,
		Steps:       make(chan Step, 100),
	}

	b.tasksMu.Lock()
	b.tasks[taskID] = task
	b.tasksMu.Unlock()

	b.send(fmt.Sprintf("⚙️ Задача `%s` запущена\nРепо: `%s/%s`", taskID, owner, repo))

	// Запускаем агента
	agent := NewAgent(b.cfg, owner, repo)
	go agent.Run(task)

	// Читаем шаги и отправляем в TG
	go b.streamSteps(task)
}

func (b *Bot) streamSteps(task *Task) {
	var lastPRNum int
	var lastPRURL string

	for step := range task.Steps {
		switch step.Type {
		case StepThought:
			b.send(fmt.Sprintf("💭 _%s_", step.Content))

		case StepAction:
			b.send(fmt.Sprintf("🔧 `%s`", step.Content))

		case StepPR:
			lastPRNum = step.PRNumber
			lastPRURL = step.PRURL
			b.sendButtons(
				fmt.Sprintf("🚀 [PR #%d](%s) открыт\nЖду тесты CI...", step.PRNumber, step.PRURL),
				[]Button{
					{Label: "🗑 Закрыть", Data: fmt.Sprintf("close:%d", step.PRNumber)},
				},
			)
			go b.watchCI(task.ID, step.PRNumber)

		case StepError:
			b.send(fmt.Sprintf("❌ %s", step.Content))

		case StepDone:
			b.send(fmt.Sprintf("✅ Готово!\n%s", step.Content))
			// Отправляем голосовое резюме если настроен TTS
			if b.cfg.OpenAIKey != "" {
				go b.sendVoiceSummary(step.Content)
			}
			b.removeTask(task.ID)
		}
	}

	_ = lastPRNum
	_ = lastPRURL
}

func (b *Bot) watchCI(taskID string, prNum int) {
	gh := NewGitHubClient(b.cfg.GitHubToken, b.getTaskOwner(taskID), b.getTaskRepo(taskID))
	result := gh.WatchChecks(prNum)

	switch result {
	case "success":
		if err := gh.MergePR(prNum); err != nil {
			b.send(fmt.Sprintf("⚠️ Тесты ок, но автомерж упал: %v", err))
		} else {
			b.send(fmt.Sprintf("✅ PR #%d смержен автоматически 🎉", prNum))
			b.removeTask(taskID)
		}

	case "failure":
		log_ := gh.GetFailLog(prNum)
		b.send(fmt.Sprintf("❌ Тесты упали:\n```\n%s\n```\nПробую починить...", truncate(log_, 600)))

		// Субагент-фиксер
		b.tasksMu.Lock()
		task := b.tasks[taskID]
		b.tasksMu.Unlock()

		if task != nil {
			agent := NewAgent(b.cfg, task.Owner, task.Repo)
			fixTask := &Task{
				ID:          taskID + "-fix",
				Description: fmt.Sprintf("Fix failing tests. Error log:\n%s", log_),
				Owner:       task.Owner,
				Repo:        task.Repo,
				Branch:      task.Branch,
				Steps:       make(chan Step, 50),
			}
			go agent.Run(fixTask)
			go b.streamSteps(fixTask)
		}

	case "timeout":
		b.send(fmt.Sprintf("⏰ CI не завершился за 20 мин\nhttps://github.com/%s/%s/pull/%d",
			b.getTaskOwner(taskID), b.getTaskRepo(taskID), prNum))
	}
}

// Голосовой ответ-резюме
func (b *Bot) sendVoiceSummary(text string) {
	voice := NewVoice(b.cfg)
	oggData, err := voice.Synthesize(text)
	if err != nil {
		return // молча, TTS опциональный
	}
	voiceMsg := tgbotapi.NewVoice(b.chatID, tgbotapi.FileBytes{
		Name:  "summary.ogg",
		Bytes: oggData,
	})
	b.api.Send(voiceMsg)
}

// /repo owner/name
func (b *Bot) handleSetRepo(arg string) {
	parts := strings.Split(strings.TrimSpace(arg), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		b.send("❌ Формат: `/repo owner/reponame`")
		return
	}
	b.repoMu.Lock()
	b.owner = parts[0]
	b.repo = parts[1]
	b.repoMu.Unlock()
	b.send(fmt.Sprintf("✅ Репо переключено на `%s/%s`", parts[0], parts[1]))
}

func (b *Bot) handleStatus() {
	b.repoMu.RLock()
	owner, repo := b.owner, b.repo
	b.repoMu.RUnlock()

	b.tasksMu.Lock()
	count := len(b.tasks)
	b.tasksMu.Unlock()

	b.send(fmt.Sprintf("📊 *Статус*\nРепо: `%s/%s`\nАктивных задач: %d", owner, repo, count))
}

func (b *Bot) handlePRs() {
	owner, repo := b.currentRepo()
	gh := NewGitHubClient(b.cfg.GitHubToken, owner, repo)
	prs, err := gh.ListPRs()
	if err != nil {
		b.send(fmt.Sprintf("❌ %v", err))
		return
	}
	if len(prs) == 0 {
		b.send("🟢 Открытых PR нет")
		return
	}
	var sb strings.Builder
	sb.WriteString("📋 *Open PRs:*\n")
	for _, pr := range prs {
		sb.WriteString(fmt.Sprintf("• [#%d %s](%s)\n", pr.Number, pr.Title, pr.URL))
	}
	b.send(sb.String())
}

func (b *Bot) handleTaskList() {
	b.tasksMu.Lock()
	defer b.tasksMu.Unlock()
	if len(b.tasks) == 0 {
		b.send("😴 Нет активных задач")
		return
	}
	var sb strings.Builder
	sb.WriteString("⚙️ *Активные задачи:*\n")
	for id, t := range b.tasks {
		sb.WriteString(fmt.Sprintf("• `%s` — %s\n", id, truncate(t.Description, 50)))
	}
	b.send(sb.String())
}

func (b *Bot) cancelTask(taskID string) {
	b.tasksMu.Lock()
	task, ok := b.tasks[taskID]
	if ok {
		close(task.Steps)
		delete(b.tasks, taskID)
	}
	b.tasksMu.Unlock()
	if ok {
		b.send(fmt.Sprintf("🛑 Задача `%s` отменена", taskID))
	} else {
		b.send(fmt.Sprintf("❓ Задача `%s` не найдена", taskID))
	}
}

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	b.api.Request(tgbotapi.NewCallback(q.ID, ""))
	if strings.HasPrefix(q.Data, "close:") {
		prNum, _ := strconv.Atoi(strings.TrimPrefix(q.Data, "close:"))
		owner, repo := b.currentRepo()
		gh := NewGitHubClient(b.cfg.GitHubToken, owner, repo)
		gh.ClosePR(prNum)
		b.send(fmt.Sprintf("🗑 PR #%d закрыт", prNum))
	}
}

// Helpers
func (b *Bot) currentRepo() (string, string) {
	b.repoMu.RLock()
	defer b.repoMu.RUnlock()
	return b.owner, b.repo
}

func (b *Bot) getTaskOwner(taskID string) string {
	b.tasksMu.Lock()
	defer b.tasksMu.Unlock()
	if t, ok := b.tasks[taskID]; ok {
		return t.Owner
	}
	return b.owner
}

func (b *Bot) getTaskRepo(taskID string) string {
	b.tasksMu.Lock()
	defer b.tasksMu.Unlock()
	if t, ok := b.tasks[taskID]; ok {
		return t.Repo
	}
	return b.repo
}

func (b *Bot) removeTask(taskID string) {
	b.tasksMu.Lock()
	delete(b.tasks, taskID)
	b.tasksMu.Unlock()
}

type Button struct {
	Label string
	Data  string
}

func (b *Bot) send(text string) {
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("send: %v", err)
	}
}

func (b *Bot) sendButtons(text string, buttons []Button) {
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	var row []tgbotapi.InlineKeyboardButton
	for _, btn := range buttons {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(btn.Label, btn.Data))
	}
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(row)
	b.api.Send(msg)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}

func helpText(owner, repo string) string {
	return fmt.Sprintf(`🤖 *claude-tg dev bot*

Текущее репо: `+"`%s/%s`"+`

*Пиши задачи текстом или голосовым:*
_"добавь экран трекинга посылки"_
_"рефактори SearchCouriers на страницы"_
_"исправь баг с датами"_

Я создам ветку → напишу код + тест → открою PR → автомерж если CI зелёный.

*Команды:*
`+"`/repo owner/name`"+` — переключить репо
`+"`/prs`"+` — открытые PR
`+"`/tasks`"+` — активные задачи
`+"`/cancel <id>`"+` — отменить задачу
`+"`/status`"+` — статус
`+"`/help`"+` — это меню`, owner, repo)
}
