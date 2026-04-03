package main

import (
	"encoding/json"
	"fmt"
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
	approvalsMu sync.Mutex
	approvals   map[string]chan bool // taskID → ответ пользователя
	history     *MessageHistory      // история сообщений для поиска
	limits      *UsageLimits         // лимиты использования API
}

func NewBot(cfg Config) *Bot {
	id, _ := strconv.ParseInt(cfg.AllowedChatID, 10, 64)
	b := &Bot{cfg: cfg, chatID: id,
		owner: cfg.DefaultOwner, repo: cfg.DefaultRepo,
		tasks:     make(map[string]*Task),
		approvals: make(map[string]chan bool),
	}
	b.topics = NewTopicManager(cfg.TelegramToken, id)
	return b
}

// extMessage — расширяет tgbotapi.Message полем message_thread_id,
// которое отсутствует в go-telegram-bot-api v5.5.1
type extMessage struct {
	tgbotapi.Message
	MessageThreadID int `json:"message_thread_id,omitempty"`
}

type extUpdate struct {
	UpdateID      int                     `json:"update_id"`
	Message       *extMessage             `json:"message,omitempty"`
	CallbackQuery *tgbotapi.CallbackQuery `json:"callback_query,omitempty"`
}

func (b *Bot) initAPI() (*tgbotapi.BotAPI, error) {
	return tgbotapi.NewBotAPI(b.cfg.TelegramToken)
}

func (b *Bot) Start() error {
	api, err := b.initAPI()
	if err != nil {
		return err
	}
	b.api = api
	b.history = NewMessageHistory(api, b.chatID)
	b.limits = NewUsageLimits()
	log.Printf("✅ @%s online", api.Self.UserName)
	go b.reminderLoop()

	client := &http.Client{Timeout: 65 * time.Second}
	offset := 0
	for {
		updates, newOffset := b.fetchUpdates(offset, client)
		offset = newOffset
		for _, upd := range updates {
			if upd.CallbackQuery != nil {
				go b.handleCallback(upd.CallbackQuery)
				continue
			}
			if upd.Message == nil || upd.Message.Chat.ID != b.chatID {
				continue
			}
			go b.handleMessage(&upd.Message.Message, upd.Message.MessageThreadID)
		}
	}
}

func (b *Bot) fetchUpdates(offset int, client *http.Client) ([]extUpdate, int) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=60&allowed_updates=message,callback_query",
		b.cfg.TelegramToken, offset)
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("getUpdates: %v", err)
		time.Sleep(5 * time.Second)
		return nil, offset
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool        `json:"ok"`
		Result []extUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("getUpdates decode: %v", err)
		return nil, offset
	}

	newOffset := offset
	for _, u := range result.Result {
		if u.UpdateID+1 > newOffset {
			newOffset = u.UpdateID + 1
		}
	}
	return result.Result, newOffset
}

// ── Routing ───────────────────────────────────────────────────

func (b *Bot) handleMessage(msg *tgbotapi.Message, threadID int) {
	if msg.Voice != nil {
		b.handleVoice(msg, threadID)
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// Сохраняем в историю для поиска
	from := "user"
	if msg.From != nil && msg.From.UserName != "" {
		from = msg.From.UserName
	} else if msg.From != nil {
		from = msg.From.FirstName
	}
	if b.history != nil {
		b.history.AddMessage(msg.MessageID, threadID, from, text)
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

	// Сначала проверяем на задачу - это позволит использовать Agent со всеми тулами
	if looksLikeTask(lower) {
		b.runCodingTask(text, threadID)
		return
	}

	// Напоминания
	if looksLikeReminder(lower) {
		b.handleReminderNLP(text, threadID)
		return
	}

	// Остальное - обычный чат
	b.chat(text, threadID)
}

func looksLikeTask(lower string) bool {
	taskKeywords := []string{
		"добавь", "сделай", "создай", "напиши", "исправь", "fix", "add", "create",
		"рефактори", "refactor", "удали", "реализуй", "implement", "перепиши",
		"rewrite", "обнови", "update", "измени", "поменяй", "смени", "deploy",
		"покрой тестами",
	}

	// Ключевые слова для запросов, требующих тулы
	toolKeywords := []string{
		"мои репо", "my repos", "активные проект", "active project",
		"топик", "topic", "аватарк", "avatar",
		"список репо", "list repos", "github репо",
		"по каким проект", "what projects", "работа идет",
	}

	for _, kw := range taskKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	for _, kw := range toolKeywords {
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
	phID := b.tg("🤔 _анализирую вопрос..._", threadID)
	log.Printf("chat: msgID=%d threadID=%d text=%q", phID, threadID, text)

	o, r := b.currentRepo()
	system := fmt.Sprintf(
		`Ты AI-ассистент разработчика Никиты. Репо: %s/%s. Отвечай кратко на русском.`, o, r)

	full, err := b.streamClaude(system, text, func(partial string) {
		b.edit(phID, partial+" ▌")
	})
	if err != nil {
		errMsg := "❌ " + err.Error()
		log.Printf("chat: error streaming - %v", err)
		b.edit(phID, errMsg)
		return
	}
	if full == "" {
		full = "_(нет ответа)_"
	}
	log.Printf("chat: done, full length=%d", len(full))
	b.edit(phID, full)

	// Голосовое сообщение для длинных ответов (> 300 символов)
	if b.cfg.OpenAIKey != "" && len(full) > 300 {
		go b.sendVoice(removeMarkdown(truncate(full, 500)), threadID)
	}
}

// sendVoice отправляет голосовое сообщение в тред (если указан)
func (b *Bot) sendVoice(text string, threadID int) {
	if ogg, err := NewVoice(b.cfg).Synthesize(text); err == nil {
		msg := tgbotapi.NewVoice(b.chatID, tgbotapi.FileBytes{Name: "voice.ogg", Bytes: ogg})
		// tgbotapi v5.5.1 не поддерживает MessageThreadID для голосовых, используем send без тредов
		// TODO: использовать raw API для отправки в треды
		b.api.Send(msg)
	}
}

// removeMarkdown убирает основные markdown символы для голосового озвучивания
func removeMarkdown(text string) string {
	// Убираем ``` блоки кода
	text = strings.ReplaceAll(text, "```", "")
	// Убираем ** жирный
	text = strings.ReplaceAll(text, "**", "")
	// Убираем __ курсив
	text = strings.ReplaceAll(text, "__", "")
	// Убираем _ курсив
	text = strings.ReplaceAll(text, "_", "")
	// Убираем ` inline code
	text = strings.ReplaceAll(text, "`", "")
	// Убираем > цитаты
	text = strings.ReplaceAll(text, "> ", "")
	return text
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
		Steps:     make(chan Step, 100),
		StartedAt: time.Now(),
	}
	b.tasksMu.Lock()
	b.tasks[taskID] = task
	b.tasksMu.Unlock()

	// Создаём живой трекер прогресса
	pt := NewProgressTracker(b, description, o, r, taskThreadID)

	agent := NewAgent(b.cfg, o, r).WithProgress(pt).WithBot(b, taskThreadID)
	go agent.Run(task)
	go b.drainSteps(task, pt, taskThreadID)
}

// drainSteps читает шаги агента и стримит мысли агента в Telegram
func (b *Bot) drainSteps(task *Task, pt *ProgressTracker, threadID int) {
	var prNum int
	var prURL string
	var thoughtMsgID int // единое сообщение для стриминга мыслей агента

	for step := range task.Steps {
		switch step.Type {
		case StepThought:
			// Улучшенный формат мыслей с иконкой лампочки
			text := "💡 _" + truncate(step.Content, 300) + "_"
			if thoughtMsgID == 0 {
				thoughtMsgID = b.tg(text, threadID)
			} else {
				b.edit(thoughtMsgID, text)
			}

		case StepAction:
			if thoughtMsgID != 0 {
				// Визуально отличаем действие от мысли - показываем детали вызова
				actionText := formatToolCall(step.Content)
				b.edit(thoughtMsgID, actionText)
			}

		case StepResult:
			// Отправляем результат выполнения тула как новое сообщение
			resultText := "✓ _" + truncate(step.Content, 400) + "_"
			b.tg(resultText, threadID)
			thoughtMsgID = 0 // сбрасываем для следующей мысли

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
			b.sendTaskResult(task, step.Content, prNum, prURL, threadID)
			b.removeTask(task.ID)
		}
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
		mergeText := fmt.Sprintf("✅ Тесты прошли\n[PR #%d](%s)\n\nМержить?", prNum, prURL)
		if !b.requestApproval(task.ID+"-merge", mergeText, threadID) {
			b.tg(fmt.Sprintf("⏸ Мерж отложен → [PR #%d](%s)", prNum, prURL), threadID)
			b.removeTask(task.ID)
			return
		}
		if err := gh.MergePR(prNum); err != nil {
			b.tg(fmt.Sprintf("⚠️ Мерж ❌: %v\n🔗 %s", err, prURL), threadID)
		} else {
			if pt != nil {
				pt.Finish()
			}
			b.sendTaskResult(task, "PR смержен", prNum, prURL, threadID)
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
			Steps:     make(chan Step, 50),
			StartedAt: time.Now(),
		}
		fixPT := NewProgressTracker(b, "Fix CI tests", task.Owner, task.Repo, threadID)
		go NewAgent(b.cfg, task.Owner, task.Repo).WithProgress(fixPT).WithBot(b, threadID).Run(fix)
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
	sb.WriteString(fmt.Sprintf("📝 *%s*\n\n", truncate(task.Description, 100)))

	if prURL != "" {
		sb.WriteString(fmt.Sprintf("🔗 [PR #%d](%s) смержен\n\n", prNum, prURL))
	}

	if summary != "" && summary != "PR смержен автоматически" {
		sb.WriteString("💬 _" + truncate(summary, 300) + "_\n\n")
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
		text := fmt.Sprintf("Задача выполнена за %s. %s", fmtDuration(duration), truncate(summary, 200))
		go b.sendVoice(text, threadID)
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
	switch {
	case strings.HasPrefix(q.Data, "approve:"):
		b.resolveApproval(strings.TrimPrefix(q.Data, "approve:"), true)
	case strings.HasPrefix(q.Data, "reject:"):
		b.resolveApproval(strings.TrimPrefix(q.Data, "reject:"), false)
	case strings.HasPrefix(q.Data, "close:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(q.Data, "close:"))
		o, r := b.currentRepo()
		NewGitHubClient(b.cfg.GitHubToken, o, r).ClosePR(n)
		b.tg(fmt.Sprintf("🗑 PR #%d закрыт", n), 0)
	}
}

// requestApproval отправляет кнопки и блокирует горутину агента до ответа пользователя
func (b *Bot) requestApproval(taskID, text string, threadID int) bool {
	ch := make(chan bool, 1)
	b.approvalsMu.Lock()
	b.approvals[taskID] = ch
	b.approvalsMu.Unlock()

	b.sendWithButtons(text, [][]map[string]any{
		{
			{"text": "✅ Да", "callback_data": "approve:" + taskID},
			{"text": "❌ Отмена", "callback_data": "reject:" + taskID},
		},
	}, threadID)

	select {
	case ok := <-ch:
		return ok
	case <-time.After(10 * time.Minute):
		b.resolveApproval(taskID, false)
		return false
	}
}

func (b *Bot) resolveApproval(taskID string, approved bool) {
	b.approvalsMu.Lock()
	ch := b.approvals[taskID]
	delete(b.approvals, taskID)
	b.approvalsMu.Unlock()
	if ch != nil {
		ch <- approved
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

// formatToolCall - форматирует вызов тула с иконкой и параметрами
func formatToolCall(toolCall string) string {
	// toolCall имеет формат "tool_name(args)"
	// Например: "read_file(src/App.tsx)" или "search_code(useEffect)"

	icons := map[string]string{
		"read_file":      "📖",
		"write_file":     "✏️",
		"list_files":     "📁",
		"search_code":    "🔍",
		"search_history": "🔎",
		"get_summary":    "📋",
		"get_user_repos": "📂",
		"set_avatar":     "🎨",
		"manage_topics":  "📌",
		"spawn_subagent": "🤖",
		"orchestrate":    "🎯",
		"create_pr":      "🚀",
		"done":           "✅",
	}

	// Извлекаем имя тула и аргументы
	parts := strings.SplitN(toolCall, "(", 2)
	if len(parts) < 2 {
		return "⚡ _" + truncate(toolCall, 300) + "_"
	}

	toolName := parts[0]
	args := strings.TrimSuffix(parts[1], ")")

	icon := icons[toolName]
	if icon == "" {
		icon = "⚙️"
	}

	// Форматируем красиво с параметрами
	if args != "" {
		return fmt.Sprintf("%s `%s`\n_→ %s_", icon, toolName, truncate(args, 250))
	}
	return fmt.Sprintf("%s `%s`", icon, toolName)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
