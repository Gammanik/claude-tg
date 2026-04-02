package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

	repoMu sync.RWMutex
	owner  string
	repo   string

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
	log.Printf("✅ @%s online", api.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	for update := range api.GetUpdatesChan(u) {
		if update.CallbackQuery != nil {
			go b.handleCallback(update.CallbackQuery)
			continue
		}
		if update.Message == nil {
			continue
		}
		if update.Message.Chat.ID != b.chatID {
			continue
		}
		go b.handleMessage(update.Message)
	}
	return nil
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
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
		b.send(b.helpText())
	case text == "/status":
		b.sendStatus()
	case text == "/prs":
		b.sendPRs()
	case text == "/tasks":
		b.sendTasks()
	case strings.HasPrefix(text, "/repo "):
		b.setRepo(strings.TrimPrefix(text, "/repo "))
	case strings.HasPrefix(text, "/cancel "):
		b.cancelTask(strings.TrimPrefix(text, "/cancel "))
	default:
		if b.looksLikeTask(text) {
			b.runCodingTask(text)
		} else {
			b.chat(text)
		}
	}
}

func (b *Bot) looksLikeTask(text string) bool {
	keywords := []string{
		"добавь", "сделай", "создай", "напиши", "исправь", "fix", "add", "create",
		"рефактори", "refactor", "удали", "перенеси", "реализуй", "implement",
		"покрой тестами", "обнови", "update", "измени", "переименуй", "deploy",
	}
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// chat — разговор со стримингом (ответ обновляется по мере генерации)
func (b *Bot) chat(text string) {
	ph := b.send("💭 _думаю..._")
	o, r := b.currentRepo()
	system := fmt.Sprintf(`Ты AI-ассистент разработчика Никиты. Текущее репо: %s/%s.
Отвечай кратко и по делу на русском. Можешь обсуждать код, архитектуру, идеи.
Если нужно что-то изменить в коде — скажи что нужно написать задачу явно.`, o, r)

	full, err := b.streamClaude(system, text, func(partial string) {
		b.editMsg(ph.MessageID, partial+" ▌")
	})
	if err != nil {
		b.editMsg(ph.MessageID, "❌ "+err.Error())
		return
	}
	b.editMsg(ph.MessageID, full)
}

func (b *Bot) streamClaude(system, userText string, onChunk func(string)) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"stream":     true,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": userText}},
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", b.cfg.AnthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var full strings.Builder
	lastUpd := time.Now()
	buf := make([]byte, 4096)

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			for _, line := range strings.Split(string(buf[:n]), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					break
				}
				var ev struct {
					Delta struct {
						Text string `json:"text"`
					} `json:"delta"`
				}
				if json.Unmarshal([]byte(data), &ev) == nil && ev.Delta.Text != "" {
					full.WriteString(ev.Delta.Text)
					if time.Since(lastUpd) > 400*time.Millisecond {
						onChunk(full.String())
						lastUpd = time.Now()
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	return full.String(), nil
}

func (b *Bot) handleVoice(msg *tgbotapi.Message) {
	ph := b.send("🎤 _распознаю..._")
	fileURL, err := b.api.GetFileDirectURL(msg.Voice.FileID)
	if err != nil {
		b.editMsg(ph.MessageID, "❌ "+err.Error())
		return
	}
	text, err := NewVoice(b.cfg).Transcribe(fileURL)
	if err != nil {
		b.editMsg(ph.MessageID, "❌ STT: "+err.Error()+
			"\n\n👉 Добавь `GROQ_API_KEY` в Railway Variables (бесплатно: console.groq.com)")
		return
	}
	b.editMsg(ph.MessageID, "🎤 _"+text+"_")
	time.Sleep(300 * time.Millisecond)
	if b.looksLikeTask(text) {
		b.runCodingTask(text)
	} else {
		b.chat(text)
	}
}

func (b *Bot) runCodingTask(description string) {
	o, r := b.currentRepo()
	taskID := strconv.FormatInt(time.Now().UnixMilli(), 10)
	task := &Task{
		ID: taskID, Description: description,
		Owner: o, Repo: r,
		Steps: make(chan Step, 100),
	}
	b.tasksMu.Lock()
	b.tasks[taskID] = task
	b.tasksMu.Unlock()

	b.send(fmt.Sprintf("⚙️ Задача `%s`\nРепо: `%s/%s`\n\n_%s_",
		taskID, o, r, truncate(description, 80)))

	agent := NewAgent(b.cfg, o, r)
	go agent.Run(task)
	go b.streamSteps(task)
}

func (b *Bot) streamSteps(task *Task) {
	for step := range task.Steps {
		switch step.Type {
		case StepThought:
			b.send("💭 _" + step.Content + "_")
		case StepAction:
			b.send("🔧 `" + step.Content + "`")
		case StepPR:
			b.sendWithButtons(
				fmt.Sprintf("🚀 [PR #%d](%s) открыт — жду CI...", step.PRNumber, step.PRURL),
				[]Button{{"🗑 Закрыть PR", fmt.Sprintf("close:%d", step.PRNumber)}},
			)
			go b.watchCI(task, step.PRNumber)
		case StepError:
			b.send("❌ " + step.Content)
			b.removeTask(task.ID)
		case StepDone:
			b.send("✅ " + step.Content)
			if b.cfg.OpenAIKey != "" {
				go func(content string) {
					if ogg, err := NewVoice(b.cfg).Synthesize(truncate(content, 300)); err == nil {
						b.api.Send(tgbotapi.NewVoice(b.chatID,
							tgbotapi.FileBytes{Name: "done.ogg", Bytes: ogg}))
					}
				}(step.Content)
			}
			b.removeTask(task.ID)
		}
	}
}

func (b *Bot) watchCI(task *Task, prNum int) {
	gh := NewGitHubClient(b.cfg.GitHubToken, task.Owner, task.Repo)
	switch gh.WatchChecks(prNum) {
	case "success":
		if err := gh.MergePR(prNum); err != nil {
			b.send(fmt.Sprintf("⚠️ Тесты ✅, автомерж ❌: %v", err))
		} else {
			b.send(fmt.Sprintf("✅ PR #%d смержен 🎉", prNum))
			b.removeTask(task.ID)
		}
	case "failure":
		log_ := gh.GetFailLog(prNum)
		b.send("❌ Тесты упали:\n```\n" + truncate(log_, 500) + "\n```\nПробую починить...")
		fix := &Task{
			ID: task.ID + "-fix", Description: "Fix CI tests. Log:\n" + log_,
			Owner: task.Owner, Repo: task.Repo, Branch: task.Branch,
			Steps: make(chan Step, 50),
		}
		go NewAgent(b.cfg, task.Owner, task.Repo).Run(fix)
		go b.streamSteps(fix)
	case "timeout":
		b.send(fmt.Sprintf("⏰ CI timeout\nhttps://github.com/%s/%s/pull/%d",
			task.Owner, task.Repo, prNum))
	}
}

func (b *Bot) setRepo(arg string) {
	p := strings.Split(strings.TrimSpace(arg), "/")
	if len(p) != 2 {
		b.send("❌ Формат: `/repo owner/name`")
		return
	}
	b.repoMu.Lock()
	b.owner, b.repo = p[0], p[1]
	b.repoMu.Unlock()
	b.send(fmt.Sprintf("✅ Репо: `%s/%s`", p[0], p[1]))
}

func (b *Bot) sendStatus() {
	o, r := b.currentRepo()
	b.tasksMu.Lock()
	n := len(b.tasks)
	b.tasksMu.Unlock()
	b.send(fmt.Sprintf("📊 Репо: `%s/%s` | Задач: %d", o, r, n))
}

func (b *Bot) sendPRs() {
	o, r := b.currentRepo()
	prs, err := NewGitHubClient(b.cfg.GitHubToken, o, r).ListPRs()
	if err != nil {
		b.send("❌ " + err.Error())
		return
	}
	if len(prs) == 0 {
		b.send("🟢 Нет открытых PR")
		return
	}
	var sb strings.Builder
	sb.WriteString("📋 *Open PRs:*\n")
	for _, pr := range prs {
		sb.WriteString(fmt.Sprintf("• [#%d %s](%s)\n", pr.Number, pr.Title, pr.URL))
	}
	b.send(sb.String())
}

func (b *Bot) sendTasks() {
	b.tasksMu.Lock()
	defer b.tasksMu.Unlock()
	if len(b.tasks) == 0 {
		b.send("😴 Нет активных задач")
		return
	}
	var sb strings.Builder
	sb.WriteString("⚙️ *Задачи:*\n")
	for id, t := range b.tasks {
		sb.WriteString(fmt.Sprintf("• `%s` — %s\n", id, truncate(t.Description, 50)))
	}
	b.send(sb.String())
}

func (b *Bot) cancelTask(id string) {
	b.tasksMu.Lock()
	t, ok := b.tasks[id]
	if ok {
		close(t.Steps)
		delete(b.tasks, id)
	}
	b.tasksMu.Unlock()
	if ok {
		b.send(fmt.Sprintf("🛑 `%s` отменена", id))
	} else {
		b.send("❓ Задача не найдена")
	}
}

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	b.api.Request(tgbotapi.NewCallback(q.ID, ""))
	if strings.HasPrefix(q.Data, "close:") {
		n, _ := strconv.Atoi(strings.TrimPrefix(q.Data, "close:"))
		o, r := b.currentRepo()
		NewGitHubClient(b.cfg.GitHubToken, o, r).ClosePR(n)
		b.send(fmt.Sprintf("🗑 PR #%d закрыт", n))
	}
}

func (b *Bot) currentRepo() (string, string) {
	b.repoMu.RLock()
	defer b.repoMu.RUnlock()
	return b.owner, b.repo
}

func (b *Bot) removeTask(id string) {
	b.tasksMu.Lock()
	delete(b.tasks, id)
	b.tasksMu.Unlock()
}

func (b *Bot) send(text string) tgbotapi.Message {
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	m, _ := b.api.Send(msg)
	return m
}

func (b *Bot) editMsg(id int, text string) {
	e := tgbotapi.NewEditMessageText(b.chatID, id, text)
	e.ParseMode = "Markdown"
	b.api.Send(e)
}

func (b *Bot) sendWithButtons(text string, buttons []Button) {
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

func (b *Bot) helpText() string {
	o, r := b.currentRepo()
	return fmt.Sprintf("🤖 *claude-tg*\nРепо: `%s/%s`\n\n"+
		"Пиши что угодно:\n"+
		"— вопросы и разговор → отвечу сразу\n"+
		"— задачи на код → ветка + код + PR + автомерж\n\n"+
		"*Самомодификация:*\n"+
		"`/repo Gammanik/claude-tg` → давай задачи на меня 🔄\n\n"+
		"*Команды:*\n"+
		"`/repo owner/name` — сменить репо\n"+
		"`/prs` — открытые PR\n"+
		"`/tasks` — активные задачи\n"+
		"`/status` — статус", o, r)
}

type Button struct{ Label, Data string }

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
