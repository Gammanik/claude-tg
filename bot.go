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

type Reminder struct {
	ID, Text string
	At       time.Time
	ThreadID int
}

type Bot struct {
	api         *tgbotapi.BotAPI
	cfg         Config
	chatID      int64
	topics      *TopicManager
	repoMu      sync.RWMutex
	owner, repo string
	tasksMu     sync.Mutex
	tasks       map[string]*Task
	remindersMu sync.Mutex
	reminders   []Reminder
}

func NewBot(cfg Config) *Bot {
	id, _ := strconv.ParseInt(cfg.AllowedChatID, 10, 64)
	b := &Bot{cfg: cfg, chatID: id,
		owner: cfg.DefaultOwner, repo: cfg.DefaultRepo,
		tasks: make(map[string]*Task),
	}
	b.topics = NewTopicManager(cfg.TelegramToken, id)
	return b
}

func (b *Bot) Start() error {
	api, err := tgbotapi.NewBotAPI(b.cfg.TelegramToken)
	if err != nil {
		return err
	}
	b.api = api
	log.Printf("✅ @%s online", api.Self.UserName)
	go b.reminderLoop()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	for update := range api.GetUpdatesChan(u) {
		if update.CallbackQuery != nil {
			go b.handleCallback(update.CallbackQuery)
			continue
		}
		if update.Message == nil || update.Message.Chat.ID != b.chatID {
			continue
		}
		go b.handleMessage(update.Message)
	}
	return nil
}

// ── Routing ───────────────────────────────────────────────────

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	threadID := 0
	if msg.Voice != nil {
		b.handleVoice(msg, threadID)
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	b.route(text, threadID)
}

func (b *Bot) route(text string, threadID int) {
	lower := strings.ToLower(text)

	if containsAny(lower, "себя", "себе", "yourself", "claude-tg") {
		b.repoMu.Lock()
		b.owner, b.repo = "Gammanik", "claude-tg"
		b.repoMu.Unlock()
	}

	switch {
	case text == "/start" || text == "/help":
		b.tg(b.helpText(), threadID)
		return
	case text == "/status":
		b.sendStatus(threadID)
		return
	case text == "/prs":
		b.sendPRs(threadID)
		return
	case text == "/tasks":
		b.sendTasks(threadID)
		return
	case text == "/reminders":
		b.sendReminders(threadID)
		return
	case strings.HasPrefix(text, "/repo "):
		b.setRepo(strings.TrimPrefix(text, "/repo "), threadID)
		return
	case strings.HasPrefix(text, "/remind "):
		b.addReminder(strings.TrimPrefix(text, "/remind "), threadID)
		return
	case strings.HasPrefix(text, "/cancel "):
		b.cancelTask(strings.TrimPrefix(text, "/cancel "), threadID)
		return
	}

	if act := detectDirectAction(text); act != nil {
		b.handleDirectAction(act, threadID)
		return
	}
	if looksLikeReminder(lower) {
		b.handleReminderNLP(text, threadID)
		return
	}
	if looksLikeTask(lower) {
		b.runCodingTask(text, threadID)
		return
	}
	b.chat(text, threadID)
}

func looksLikeTask(lower string) bool {
	for _, kw := range []string{
		"добавь", "сделай", "создай", "напиши", "исправь", "fix", "add", "create",
		"рефактори", "refactor", "удали", "реализуй", "implement", "перепиши",
		"rewrite", "обнови", "update", "измени", "поменяй", "смени", "deploy",
		"покрой тестами",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func looksLikeReminder(lower string) bool {
	for _, kw := range []string{"напомни", "remind me", "напоминалк", "поставь таймер"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// ── Chat ──────────────────────────────────────────────────────

func (b *Bot) chat(text string, threadID int) {
	phID := b.tg("💭 _думаю..._", threadID)
	o, r := b.currentRepo()
	system := fmt.Sprintf(
		`Ты AI-ассистент разработчика Никиты. Репо: %s/%s. Отвечай кратко на русском.`, o, r)

	full, err := b.streamClaude(system, text, func(partial string) {
		b.edit(phID, partial+" ▌")
	})
	if err != nil {
		b.edit(phID, "❌ "+err.Error())
		return
	}
	if full == "" {
		full = "_(нет ответа)_"
	}
	b.edit(phID, full)
}

func (b *Bot) streamClaude(system, userText string, onChunk func(string)) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "stream": true,
		"system":   system,
		"messages": []map[string]string{{"role": "user", "content": userText}},
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", b.cfg.AnthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var full strings.Builder
	last := time.Now()
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			for _, line := range strings.Split(string(buf[:n]), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var ev struct {
					Delta struct {
						Text string `json:"text"`
					} `json:"delta"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil && ev.Delta.Text != "" {
					full.WriteString(ev.Delta.Text)
					if time.Since(last) > 400*time.Millisecond {
						onChunk(full.String())
						last = time.Now()
					}
				}
			}
		}
		if err == io.EOF || err != nil {
			break
		}
	}
	return full.String(), nil
}

func (b *Bot) callHaiku(system, text string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": "claude-haiku-4-5-20251001", "max_tokens": 300,
		"system":   system,
		"messages": []map[string]string{{"role": "user", "content": text}},
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", b.cfg.AnthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Content) > 0 {
		return result.Content[0].Text, nil
	}
	return "", fmt.Errorf("empty")
}

// ── Voice ─────────────────────────────────────────────────────

func (b *Bot) handleVoice(msg *tgbotapi.Message, threadID int) {
	phID := b.tg("🎤 _распознаю..._", threadID)
	fileURL, err := b.api.GetFileDirectURL(msg.Voice.FileID)
	if err != nil {
		b.edit(phID, "❌ "+err.Error())
		return
	}
	text, err := NewVoice(b.cfg).Transcribe(fileURL)
	if err != nil {
		b.edit(phID, "❌ STT: "+err.Error()+
			"\n\n👉 Добавь `GROQ_API_KEY` в Railway Variables (бесплатно: console.groq.com)")
		return
	}
	b.edit(phID, "🎤 _"+text+"_")
	time.Sleep(200 * time.Millisecond)
	b.route(text, threadID)
}

// ── Direct actions ────────────────────────────────────────────

func (b *Bot) handleDirectAction(act *directAction, threadID int) {
	switch act.kind {
	case "set_avatar":
		phID := b.tg("🎨 _Генерирую аватарку..._", threadID)
		if err := b.setBotAvatar(); err != nil {
			b.edit(phID, fmt.Sprintf("❌ %v\n\nДобавь `OPENAI_API_KEY` для DALL-E генерации", err))
		} else {
			b.edit(phID, "✅ Аватарка обновлена!")
		}
	case "set_name":
		b.tg("⚠️ Имя меняется через @BotFather → /setname", threadID)
	}
}

// ── Coding agent с живым прогрессом ──────────────────────────

func (b *Bot) runCodingTask(description string, _ int) {
	o, r := b.currentRepo()

	// Определяем тред для этого репо
	taskThreadID := b.topics.GetOrCreate(o, r)
	// Если не форум-группа (GetOrCreate вернул 0) — пишем в основной чат
	if taskThreadID == 0 {
		taskThreadID = 0
	}

	taskID := strconv.FormatInt(time.Now().UnixMilli(), 10)
	task := &Task{
		ID: taskID, Description: description,
		Owner: o, Repo: r,
		Steps: make(chan Step, 100),
	}
	b.tasksMu.Lock()
	b.tasks[taskID] = task
	b.tasksMu.Unlock()

	// Создаём живой трекер прогресса
	pt := NewProgressTracker(b, description, o, r, taskThreadID)

	agent := NewAgent(b.cfg, o, r).WithProgress(pt)
	go agent.Run(task)
	go b.drainSteps(task, pt, taskThreadID)
}

// drainSteps читает шаги агента (для передачи в watchCI и финального сообщения)
func (b *Bot) drainSteps(task *Task, pt *ProgressTracker, threadID int) {
	var prNum int
	var prURL string

	for step := range task.Steps {
		switch step.Type {
		case StepPR:
			prNum = step.PRNumber
			prURL = step.PRURL
			pt.SetPR(prNum, prURL)
			go b.watchCI(task, prNum, prURL, pt, threadID)

		case StepError:
			pt.Error(step.Content)
			b.removeTask(task.ID)

		case StepDone:
			pt.Finish()
			// Отправляем итог + календарь
			b.sendTaskResult(task, step.Content, prNum, prURL, threadID)
			b.removeTask(task.ID)
		}
		// Thought и Action уже обрабатываются в ProgressTracker
	}
}

func (b *Bot) watchCI(task *Task, prNum int, prURL string, pt *ProgressTracker, threadID int) {
	gh := NewGitHubClient(b.cfg.GitHubToken, task.Owner, task.Repo)

	// Обновляем прогресс — CI ожидание
	if pt != nil {
		pt.StartStep("wait_ci", fmt.Sprintf("PR #%d", prNum))
	}

	switch gh.WatchChecks(prNum) {
	case "success":
		if pt != nil {
			pt.DoneStep(false)
		}
		if err := gh.MergePR(prNum); err != nil {
			b.tg(fmt.Sprintf("⚠️ Тесты ✅ мерж ❌: %v\n🔗 %s", err, prURL), threadID)
		} else {
			if pt != nil {
				pt.Finish()
			}
			b.sendTaskResult(task, "PR смержен автоматически", prNum, prURL, threadID)
		}
		b.removeTask(task.ID)

	case "failure":
		if pt != nil {
			pt.DoneStep(true)
		}
		log_ := gh.GetFailLog(prNum)
		b.tg("❌ Тесты:\n```\n"+truncate(log_, 500)+"\n```\nФикшу...", threadID)
		fix := &Task{
			ID: task.ID + "-fix", Description: "Fix CI. Log:\n" + log_,
			Owner: task.Owner, Repo: task.Repo, Branch: task.Branch,
			Steps: make(chan Step, 50),
		}
		fixPT := NewProgressTracker(b, "Fix CI tests", task.Owner, task.Repo, threadID)
		go NewAgent(b.cfg, task.Owner, task.Repo).WithProgress(fixPT).Run(fix)
		go b.drainSteps(fix, fixPT, threadID)

	case "timeout":
		if pt != nil {
			pt.DoneStep(true)
		}
		b.tg("⏰ CI timeout\n🔗 "+prURL, threadID)
	}
}

func (b *Bot) sendTaskResult(task *Task, summary string, prNum int, prURL string, threadID int) {
	duration := time.Since(task.StartedAt)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ *Задача завершена* за %s\n\n", fmtDuration(duration)))
	if prURL != "" {
		sb.WriteString(fmt.Sprintf("🔗 [PR #%d](%s) смержен\n\n", prNum, prURL))
	}
	if summary != "" && summary != "PR смержен автоматически" {
		sb.WriteString("_" + truncate(summary, 200) + "_\n\n")
	}

	// Ссылка на добавление в Google Calendar
	if prURL != "" || summary != "" {
		ev := buildTaskEvent(task.Description, task.Owner, task.Repo, prURL, duration)
		calLink := GoogleCalendarLink(ev)
		sb.WriteString(fmt.Sprintf("📅 [Добавить в календарь](%s)", calLink))
	}

	b.tg(sb.String(), threadID)

	// Голосовое резюме
	if b.cfg.OpenAIKey != "" {
		go func() {
			text := fmt.Sprintf("Задача выполнена за %s. %s", fmtDuration(duration), truncate(summary, 200))
			if ogg, err := NewVoice(b.cfg).Synthesize(text); err == nil {
				b.api.Send(tgbotapi.NewVoice(b.chatID,
					tgbotapi.FileBytes{Name: "done.ogg", Bytes: ogg}))
			}
		}()
	}
}

// ── Reminders ─────────────────────────────────────────────────

func (b *Bot) handleReminderNLP(text string, threadID int) {
	phID := b.tg("⏰ _разбираю..._", threadID)
	raw, err := b.callHaiku(
		`Parse reminder from Russian. JSON only:
{"text":"what to remind","minutes":N}`,
		text)
	if err != nil {
		b.edit(phID, "❌ "+err.Error())
		return
	}
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndex(raw, "}"); j >= 0 {
		raw = raw[:j+1]
	}
	var parsed struct {
		Text    string `json:"text"`
		Minutes int    `json:"minutes"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed.Minutes <= 0 {
		b.edit(phID, "")
		b.chat(text, threadID)
		return
	}
	at := time.Now().Add(time.Duration(parsed.Minutes) * time.Minute)
	id := strconv.FormatInt(time.Now().UnixMilli(), 10)
	b.remindersMu.Lock()
	b.reminders = append(b.reminders, Reminder{ID: id, Text: parsed.Text, At: at, ThreadID: threadID})
	b.remindersMu.Unlock()
	b.edit(phID, fmt.Sprintf("⏰ Напомню: _%s_\n🕐 %s", parsed.Text, at.Format("02 Jan 15:04")))
}

func (b *Bot) addReminder(arg string, threadID int) {
	at, text := parseReminderCmd(arg)
	if at.IsZero() {
		b.tg("❌ Пример: `/remind митинг через 30 минут`", threadID)
		return
	}
	id := strconv.FormatInt(time.Now().UnixMilli(), 10)
	b.remindersMu.Lock()
	b.reminders = append(b.reminders, Reminder{ID: id, Text: text, At: at, ThreadID: threadID})
	b.remindersMu.Unlock()
	b.tg(fmt.Sprintf("⏰ Напомню: _%s_\n🕐 %s", text, at.Format("02 Jan 15:04")), threadID)
}

func parseReminderCmd(arg string) (time.Time, string) {
	lower := strings.ToLower(arg)
	var minutes int
	if i := strings.Index(lower, "через "); i >= 0 {
		parts := strings.Fields(lower[i+6:])
		if len(parts) >= 2 {
			n, _ := strconv.Atoi(parts[0])
			switch {
			case strings.Contains(parts[1], "мин"):
				minutes = n
			case strings.Contains(parts[1], "час"):
				minutes = n * 60
			case strings.Contains(parts[1], "ден"), strings.Contains(parts[1], "сут"):
				minutes = n * 1440
			}
		}
	}
	if strings.Contains(lower, "завтра") && minutes == 0 {
		minutes = 1440
	}
	if minutes == 0 {
		return time.Time{}, ""
	}
	text := arg
	if i := strings.Index(strings.ToLower(text), " через "); i > 0 {
		text = text[:i]
	}
	return time.Now().Add(time.Duration(minutes) * time.Minute), strings.TrimSpace(text)
}

func (b *Bot) reminderLoop() {
	for {
		time.Sleep(30 * time.Second)
		now := time.Now()
		b.remindersMu.Lock()
		var keep []Reminder
		for _, r := range b.reminders {
			if now.After(r.At) {
				b.tg("⏰ *Напоминание:* "+r.Text, r.ThreadID)
			} else {
				keep = append(keep, r)
			}
		}
		b.reminders = keep
		b.remindersMu.Unlock()
	}
}

func (b *Bot) sendReminders(threadID int) {
	b.remindersMu.Lock()
	defer b.remindersMu.Unlock()
	if len(b.reminders) == 0 {
		b.tg("🔕 Нет напоминалок", threadID)
		return
	}
	var sb strings.Builder
	sb.WriteString("⏰ *Напоминалки:*\n")
	for _, r := range b.reminders {
		sb.WriteString(fmt.Sprintf("• %s _(в %s)_\n", r.Text, r.At.Format("02 Jan 15:04")))
	}
	b.tg(sb.String(), threadID)
}

// ── Commands ──────────────────────────────────────────────────

func (b *Bot) setRepo(arg string, threadID int) {
	p := strings.Split(strings.TrimSpace(arg), "/")
	if len(p) != 2 {
		b.tg("❌ Формат: `/repo owner/name`", threadID)
		return
	}
	b.repoMu.Lock()
	b.owner, b.repo = p[0], p[1]
	b.repoMu.Unlock()
	b.tg(fmt.Sprintf("✅ Репо: `%s/%s`", p[0], p[1]), threadID)
}

func (b *Bot) sendStatus(threadID int) {
	o, r := b.currentRepo()
	b.tasksMu.Lock()
	n := len(b.tasks)
	b.tasksMu.Unlock()
	b.remindersMu.Lock()
	nr := len(b.reminders)
	b.remindersMu.Unlock()
	b.tg(fmt.Sprintf("📊 Репо: `%s/%s` | Задач: %d | Напоминалок: %d", o, r, n, nr), threadID)
}

func (b *Bot) sendPRs(threadID int) {
	o, r := b.currentRepo()
	prs, err := NewGitHubClient(b.cfg.GitHubToken, o, r).ListPRs()
	if err != nil {
		b.tg("❌ "+err.Error(), threadID)
		return
	}
	if len(prs) == 0 {
		b.tg("🟢 Нет открытых PR", threadID)
		return
	}
	var sb strings.Builder
	sb.WriteString("📋 *Open PRs:*\n")
	for _, pr := range prs {
		sb.WriteString(fmt.Sprintf("• [#%d %s](%s)\n", pr.Number, pr.Title, pr.URL))
	}
	b.tg(sb.String(), threadID)
}

func (b *Bot) sendTasks(threadID int) {
	b.tasksMu.Lock()
	defer b.tasksMu.Unlock()
	if len(b.tasks) == 0 {
		b.tg("😴 Нет активных задач", threadID)
		return
	}
	var sb strings.Builder
	sb.WriteString("⚙️ *Задачи:*\n")
	for id, t := range b.tasks {
		sb.WriteString(fmt.Sprintf("• `%s` — %s\n", id, truncate(t.Description, 50)))
	}
	b.tg(sb.String(), threadID)
}

func (b *Bot) cancelTask(id string, threadID int) {
	b.tasksMu.Lock()
	t, ok := b.tasks[id]
	if ok {
		close(t.Steps)
		delete(b.tasks, id)
	}
	b.tasksMu.Unlock()
	if ok {
		b.tg(fmt.Sprintf("🛑 `%s` отменена", id), threadID)
	} else {
		b.tg("❓ Задача не найдена", threadID)
	}
}

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	b.api.Request(tgbotapi.NewCallback(q.ID, ""))
	if strings.HasPrefix(q.Data, "close:") {
		n, _ := strconv.Atoi(strings.TrimPrefix(q.Data, "close:"))
		o, r := b.currentRepo()
		NewGitHubClient(b.cfg.GitHubToken, o, r).ClosePR(n)
		b.tg(fmt.Sprintf("🗑 PR #%d закрыт", n), 0)
	}
}

// ── Helpers ───────────────────────────────────────────────────

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

func (b *Bot) tg(text string, threadID int) int {
	return b.sendMessageRaw(text, threadID)
}

func (b *Bot) edit(msgID int, text string) {
	if msgID == 0 || text == "" {
		return
	}
	b.editRaw(msgID, text)
}

func (b *Bot) helpText() string {
	o, r := b.currentRepo()
	return fmt.Sprintf("🤖 *claude-tg*\nРепо: `%s/%s`\n\n"+
		"Пиши или говори:\n"+
		"— задача → живой прогресс в треде + PR + календарь\n"+
		"— вопрос → стриминг ответа\n"+
		"— напомни → таймер\n"+
		"— поменяй аватарку → меняет фото\n\n"+
		"Каждый репо получает свой тред в группе автоматически.\n\n"+
		"*Команды:*\n"+
		"`/repo owner/name` | `/prs` | `/tasks`\n"+
		"`/reminders` | `/status`", o, r)
}

type Button struct{ Label, Data string }

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
