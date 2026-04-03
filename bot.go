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

type Bot struct {
	api     *tgbotapi.BotAPI
	cfg     Config
	chatID  int64
	llm     *LLMClient
	topics  *TopicManager
	history *MessageHistory

	repoMu      sync.RWMutex
	owner, repo string

	tasksMu   sync.Mutex
	tasks     map[string]*Task
	approvals map[string]chan bool
}

func NewBot(cfg Config) *Bot {
	id, _ := strconv.ParseInt(cfg.AllowedChatID, 10, 64)
	b := &Bot{
		cfg:       cfg,
		chatID:    id,
		llm:       NewLLMClient(cfg.AnthropicKey, cfg.DeepSeekKey, cfg.LLMProvider),
		owner:     cfg.DefaultOwner,
		repo:      cfg.DefaultRepo,
		tasks:     make(map[string]*Task),
		approvals: make(map[string]chan bool),
	}
	b.topics = NewTopicManager(cfg.TelegramToken, id)
	return b
}

type extMessage struct {
	tgbotapi.Message
	MessageThreadID int `json:"message_thread_id,omitempty"`
}

type extUpdate struct {
	UpdateID      int                     `json:"update_id"`
	Message       *extMessage             `json:"message,omitempty"`
	CallbackQuery *tgbotapi.CallbackQuery `json:"callback_query,omitempty"`
}

func (b *Bot) Start() error {
	api, err := tgbotapi.NewBotAPI(b.cfg.TelegramToken)
	if err != nil {
		return err
	}
	b.api = api
	b.history = NewMessageHistory(api, b.chatID)

	// Валидация GitHub токена
	if b.cfg.GitHubToken != "" {
		gh := NewGitHubClient(b.cfg.GitHubToken, "test", "test")
		if login, err := gh.ValidateToken(); err != nil {
			log.Printf("⚠️  GitHub token invalid: %v", err)
		} else {
			log.Printf("✅ GitHub: @%s", login)
		}
	}

	log.Printf("✅ Bot @%s online (provider=%s)", api.Self.UserName, b.cfg.LLMProvider)

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
		log.Printf("decode: %v", err)
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

// ── Message routing ───────────────────────────────────────────

func (b *Bot) handleMessage(msg *tgbotapi.Message, threadID int) {
	// Голосовые сообщения
	if msg.Voice != nil {
		b.handleVoice(msg, threadID)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// Сохраняем в историю
	from := "user"
	if msg.From != nil && msg.From.UserName != "" {
		from = msg.From.UserName
	} else if msg.From != nil {
		from = msg.From.FirstName
	}
	if b.history != nil {
		b.history.AddMessage(msg.MessageID, threadID, from, text)
	}

	// Команды
	switch {
	case text == "/start" || text == "/help":
		b.tg(b.helpText(), threadID)
		return
	case text == "/status":
		b.sendStatus(threadID)
		return
	case text == "/prs":
		b.listPRs(threadID)
		return
	case strings.HasPrefix(text, "/repo "):
		b.setRepo(strings.TrimPrefix(text, "/repo "), threadID)
		return
	case strings.HasPrefix(text, "/repos "):
		username := strings.TrimSpace(strings.TrimPrefix(text, "/repos "))
		b.listRepos(username, threadID)
		return
	case text == "/repos":
		o, _ := b.currentRepo()
		b.listRepos(o, threadID)
		return
	}

	// Автоопределение намерения
	b.route(text, threadID)
}

func (b *Bot) route(text string, threadID int) {
	// Пытаемся извлечь owner/repo из текста (например: "покажи PR для gammanik/app")
	if owner, repo, found := extractRepo(text); found {
		b.repoMu.Lock()
		b.owner, b.repo = owner, repo
		b.repoMu.Unlock()
		log.Printf("📍 Переключено на репозиторий: %s/%s", owner, repo)
	}

	o, r := b.currentRepo()

	// Используем Haiku для быстрой классификации
	intent, err := b.llm.RouteIntent(text, o, r)
	if err != nil {
		log.Printf("route error: %v, fallback to chat", err)
		b.chat(text, threadID)
		return
	}

	intent = strings.TrimSpace(strings.ToLower(intent))
	log.Printf("intent: %s", intent)

	switch {
	case strings.Contains(intent, "switch_repo"):
		// Пытаемся извлечь owner/repo из текста
		if owner, repo, found := extractRepo(text); found {
			b.repoMu.Lock()
			b.owner, b.repo = owner, repo
			b.repoMu.Unlock()
			b.tg(fmt.Sprintf("✅ Переключено на `%s/%s`", owner, repo), threadID)
			return
		}
		// Если формат owner/repo не найден, пробуем просто имя репо
		words := strings.Fields(text)
		for i, word := range words {
			word = strings.ToLower(word)
			if (word == "на" || word == "to") && i+1 < len(words) {
				repoName := strings.TrimSpace(words[i+1])
				// Удаляем лишние символы
				repoName = strings.Trim(repoName, ".,!?;:")
				b.repoMu.Lock()
				// Если не указан owner, используем текущий
				if !strings.Contains(repoName, "/") {
					repoName = b.owner + "/" + repoName
				}
				parts := strings.Split(repoName, "/")
				if len(parts) == 2 {
					b.owner, b.repo = parts[0], parts[1]
					b.repoMu.Unlock()
					b.tg(fmt.Sprintf("✅ Переключено на `%s/%s`", parts[0], parts[1]), threadID)
					return
				}
				b.repoMu.Unlock()
			}
		}
		b.tg("❌ Не могу определить репозиторий. Используй формат: `/repo owner/name` или укажи `owner/repo` в сообщении", threadID)
	case strings.Contains(intent, "list_prs"):
		b.listPRs(threadID)
	case strings.Contains(intent, "list_repos"):
		// Пытаемся извлечь username из текста, иначе используем текущий owner
		username := o
		// Простой парсинг для "какие у меня" -> текущий owner, "какие у USER" -> USER
		words := strings.Fields(strings.ToLower(text))
		for i, word := range words {
			if (word == "у" || word == "for" || word == "пользователя") && i+1 < len(words) {
				username = words[i+1]
				break
			}
		}
		b.listRepos(username, threadID)
	case strings.Contains(intent, "merge_pr"):
		prNum := extractPRNumber(text)
		if prNum == 0 {
			b.tg("❌ Укажи номер PR (например: 'смержи #5')", threadID)
			return
		}
		b.mergePR(prNum, threadID)
	case strings.Contains(intent, "close_pr"):
		prNum := extractPRNumber(text)
		if prNum == 0 {
			b.tg("❌ Укажи номер PR (например: 'закрой #5')", threadID)
			return
		}
		b.closePR(prNum, threadID)
	case strings.Contains(intent, "code"):
		b.runTask(text, threadID)
	default:
		b.chat(text, threadID)
	}
}

// ── Voice ─────────────────────────────────────────────────────

func (b *Bot) handleVoice(msg *tgbotapi.Message, threadID int) {
	phID := b.tg("🎤 _распознаю..._", threadID)
	fileURL, err := b.api.GetFileDirectURL(msg.Voice.FileID)
	if err != nil {
		b.edit(phID, "❌ "+err.Error())
		return
	}

	voice := NewVoice(b.cfg)
	text, err := voice.Transcribe(fileURL)
	if err != nil {
		b.edit(phID, "❌ STT: "+err.Error())
		return
	}

	b.edit(phID, "🎤 _"+text+"_")
	time.Sleep(200 * time.Millisecond)
	b.route(text, threadID)
}

func (b *Bot) sendVoice(text string, threadID int) {
	if b.cfg.OpenAIKey == "" {
		return
	}
	voice := NewVoice(b.cfg)
	ogg, err := voice.Synthesize(text)
	if err == nil {
		msg := tgbotapi.NewVoice(b.chatID, tgbotapi.FileBytes{Name: "voice.ogg", Bytes: ogg})
		// TODO: add threadID support via raw API
		b.api.Send(msg)
	}
}

// ── Chat ──────────────────────────────────────────────────────

func (b *Bot) chat(text string, threadID int) {
	o, r := b.currentRepo()
	system := fmt.Sprintf(`Ты AI-ассистент в Telegram боте для работы с GitHub.

ТЕКУЩИЙ РЕПОЗИТОРИЙ: %s/%s

ВАЖНЫЕ ПРАВИЛА:
1. Отвечай ТОЛЬКО о текущем репозитории (%s/%s)
2. Если не знаешь что-то конкретное о репозитории - честно скажи это
3. НЕ выдумывай информацию о файлах, структуре или коде которых нет
4. НЕ путай с другими проектами/языками
5. Если пользователь просит что-то сделать (добавить код, изменить файлы) -
   скажи что для этого нужно попросить напрямую (не как вопрос), чтобы включился агент

Что ты можешь:
- Отвечать на общие вопросы о разработке
- Давать советы и рекомендации
- Объяснять концепции и паттерны
- Обсуждать архитектуру и подходы

Что делает агент (не ты):
- Читает реальные файлы из репозитория
- Делает изменения в коде
- Создаёт коммиты

Отвечай кратко на русском.`, o, r, o, r)

	phID := b.tg("💭", threadID)

	// Используем Sonnet со стримингом
	full, err := b.llm.Stream(TierSonnet, system, text, func(partial string) {
		b.edit(phID, partial+" ▌")
	})

	if err != nil {
		log.Printf("chat error: %v", err)
		b.edit(phID, fmt.Sprintf("❌ %v", err))
		return
	}

	b.edit(phID, full)

	// Голосовое сообщение для длинных ответов
	if len(full) > 300 {
		go b.sendVoice(removeMarkdown(truncate(full, 500)), threadID)
	}
}

func removeMarkdown(text string) string {
	text = strings.ReplaceAll(text, "```", "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "_", "")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "> ", "")
	return text
}

// ── Task execution ────────────────────────────────────────────

func (b *Bot) runTask(description string, threadID int) {
	o, r := b.currentRepo()

	taskID := strconv.FormatInt(time.Now().UnixMilli(), 10)
	task := &Task{
		ID:          taskID,
		Description: description,
		Owner:       o,
		Repo:        r,
		Steps:       make(chan Step, 100),
		StartedAt:   time.Now(),
	}

	b.tasksMu.Lock()
	b.tasks[taskID] = task
	b.tasksMu.Unlock()

	// Запускаем агента (используется Opus для coding)
	agent := NewAgent(b.cfg, b.llm, o, r).WithBot(b, threadID)
	go agent.Run(task)
	go b.drainSteps(task, threadID)
}

func (b *Bot) drainSteps(task *Task, threadID int) {
	var msgID int
	var prNum int
	var prURL string

	for step := range task.Steps {
		switch step.Type {
		case StepThought:
			text := "💭 _" + truncate(step.Content, 300) + "_"
			if msgID == 0 {
				msgID = b.tg(text, threadID)
			} else {
				b.edit(msgID, text)
			}

		case StepAction:
			text := "⚡ `" + step.Content + "`"
			b.edit(msgID, text)

		case StepResult:
			b.tg("✓ _"+truncate(step.Content, 400)+"_", threadID)
			msgID = 0

		case StepPR:
			prNum = step.PRNumber
			prURL = step.PRURL
			b.tg(fmt.Sprintf("🚀 [PR #%d](%s) создан", prNum, prURL), threadID)
			go b.watchCI(task, prNum, prURL, threadID)

		case StepError:
			b.tg("❌ "+step.Content, threadID)
			b.removeTask(task.ID)

		case StepDone:
			elapsed := time.Since(task.StartedAt)
			result := fmt.Sprintf("✅ Готово за %s\n\n_%s_", fmtDuration(elapsed), step.Content)
			if prURL != "" {
				result += fmt.Sprintf("\n\n🔗 [PR #%d](%s)", prNum, prURL)
			}
			b.tg(result, threadID)
			b.removeTask(task.ID)

			// Голосовое резюме
			if b.cfg.OpenAIKey != "" {
				voiceText := fmt.Sprintf("Задача выполнена за %s. %s", fmtDuration(elapsed), truncate(step.Content, 200))
				go b.sendVoice(voiceText, threadID)
			}
		}
	}
}

func (b *Bot) watchCI(task *Task, prNum int, prURL string, threadID int) {
	gh := NewGitHubClient(b.cfg.GitHubToken, task.Owner, task.Repo)

	status := gh.WatchChecks(prNum)
	switch status {
	case "success":
		msg := fmt.Sprintf("✅ Тесты прошли\n[PR #%d](%s)\n\nСмержить?", prNum, prURL)
		if !b.requestApproval(task.ID+"-merge", msg, threadID) {
			b.tg("⏸ Мерж отложен", threadID)
			return
		}
		if err := gh.MergePR(prNum); err != nil {
			b.tg(fmt.Sprintf("❌ Мерж failed: %v", err), threadID)
		} else {
			b.tg(fmt.Sprintf("✅ PR #%d смержен", prNum), threadID)
		}

	case "failure":
		logs := gh.GetFailLog(prNum)
		b.tg("❌ Тесты:\n```\n"+truncate(logs, 500)+"\n```", threadID)

	case "timeout":
		b.tg("⏰ CI timeout\n🔗 "+prURL, threadID)
	}
}

// ── PR management ─────────────────────────────────────────────

func (b *Bot) listPRs(threadID int) {
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
	sb.WriteString(fmt.Sprintf("📋 *Открытые PR для %s/%s:*\n\n", o, r))
	for i, pr := range prs {
		sb.WriteString(fmt.Sprintf("%d. [#%d](%s) - %s\n", i+1, pr.Number, pr.URL, pr.Title))
	}

	// Кнопки для каждого PR
	var keyboard [][]map[string]any
	for _, pr := range prs {
		row := []map[string]any{
			{"text": fmt.Sprintf("✅ Merge #%d", pr.Number), "callback_data": fmt.Sprintf("merge:%d", pr.Number)},
			{"text": fmt.Sprintf("❌ Close #%d", pr.Number), "callback_data": fmt.Sprintf("close:%d", pr.Number)},
		}
		keyboard = append(keyboard, row)
	}

	b.sendWithButtons(sb.String(), keyboard, threadID)
}

func (b *Bot) mergePR(prNum int, threadID int) {
	o, r := b.currentRepo()
	gh := NewGitHubClient(b.cfg.GitHubToken, o, r)

	prs, err := gh.ListPRs()
	if err != nil {
		b.tg("❌ "+err.Error(), threadID)
		return
	}

	var prTitle string
	found := false
	for _, pr := range prs {
		if pr.Number == prNum {
			found = true
			prTitle = pr.Title
			break
		}
	}

	if !found {
		b.tg(fmt.Sprintf("❌ PR #%d не найден", prNum), threadID)
		return
	}

	msg := fmt.Sprintf("🔀 Смержить PR #%d?\n\n*%s*", prNum, prTitle)
	taskID := fmt.Sprintf("merge-pr-%d-%d", prNum, time.Now().Unix())

	if !b.requestApproval(taskID, msg, threadID) {
		b.tg("⏸ Мерж отменен", threadID)
		return
	}

	if err := gh.MergePR(prNum); err != nil {
		b.tg(fmt.Sprintf("❌ %v", err), threadID)
		return
	}

	b.tg(fmt.Sprintf("✅ PR #%d смержен", prNum), threadID)
}

func (b *Bot) listRepos(username string, threadID int) {
	gh := NewGitHubClient(b.cfg.GitHubToken, "", "")
	result, err := gh.GetUserRepos(username)
	if err != nil {
		b.tg("❌ "+err.Error(), threadID)
		return
	}
	// Отправляем без markdown чтобы избежать ошибок парсинга
	b.tgPlain(result, threadID)
}

func (b *Bot) closePR(prNum int, threadID int) {
	o, r := b.currentRepo()
	gh := NewGitHubClient(b.cfg.GitHubToken, o, r)

	prs, err := gh.ListPRs()
	if err != nil {
		b.tg("❌ "+err.Error(), threadID)
		return
	}

	var prTitle string
	found := false
	for _, pr := range prs {
		if pr.Number == prNum {
			found = true
			prTitle = pr.Title
			break
		}
	}

	if !found {
		b.tg(fmt.Sprintf("❌ PR #%d не найден", prNum), threadID)
		return
	}

	msg := fmt.Sprintf("🗑 Закрыть PR #%d?\n\n*%s*", prNum, prTitle)
	taskID := fmt.Sprintf("close-pr-%d-%d", prNum, time.Now().Unix())

	if !b.requestApproval(taskID, msg, threadID) {
		b.tg("⏸ Отменено", threadID)
		return
	}

	if err := gh.ClosePR(prNum); err != nil {
		b.tg(fmt.Sprintf("❌ %v", err), threadID)
		return
	}

	b.tg(fmt.Sprintf("🗑 PR #%d закрыт", prNum), threadID)
}

// ── Callbacks ─────────────────────────────────────────────────

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	b.api.Request(tgbotapi.NewCallback(q.ID, ""))

	switch {
	case strings.HasPrefix(q.Data, "approve:"):
		b.resolveApproval(strings.TrimPrefix(q.Data, "approve:"), true)
	case strings.HasPrefix(q.Data, "reject:"):
		b.resolveApproval(strings.TrimPrefix(q.Data, "reject:"), false)
	case strings.HasPrefix(q.Data, "merge:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(q.Data, "merge:"))
		go b.mergePR(n, 0)
	case strings.HasPrefix(q.Data, "close:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(q.Data, "close:"))
		go b.closePR(n, 0)
	}
}

func (b *Bot) requestApproval(taskID, text string, threadID int) bool {
	ch := make(chan bool, 1)
	b.tasksMu.Lock()
	b.approvals[taskID] = ch
	b.tasksMu.Unlock()

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
	b.tasksMu.Lock()
	ch := b.approvals[taskID]
	delete(b.approvals, taskID)
	b.tasksMu.Unlock()
	if ch != nil {
		ch <- approved
	}
}

// ── Commands ──────────────────────────────────────────────────

func (b *Bot) setRepo(arg string, threadID int) {
	parts := strings.Split(strings.TrimSpace(arg), "/")
	if len(parts) != 2 {
		b.tg("❌ Формат: `/repo owner/name`", threadID)
		return
	}
	b.repoMu.Lock()
	b.owner, b.repo = parts[0], parts[1]
	b.repoMu.Unlock()
	b.tg(fmt.Sprintf("✅ Репо: `%s/%s`", parts[0], parts[1]), threadID)
}

func (b *Bot) sendStatus(threadID int) {
	o, r := b.currentRepo()
	b.tasksMu.Lock()
	n := len(b.tasks)
	b.tasksMu.Unlock()
	b.tg(fmt.Sprintf("📊 Репо: `%s/%s` | Задач: %d", o, r, n), threadID)
}

func (b *Bot) helpText() string {
	o, r := b.currentRepo()
	return fmt.Sprintf(`🤖 *claude-tg* — AI-ассистент для GitHub

Текущий репозиторий: `+"`%s/%s`"+`

Просто пиши или говори:
— задачу → агент создаст PR
— вопрос → чат с AI
— "покажи PR для owner/repo" → список PR любого репо

*Команды:*
`+"`/repo owner/name`"+` — переключить репозиторий
`+"`/repos [username]`"+` — список репозиториев пользователя
`+"`/prs`"+` — показать PR
`+"`/status`"+` — текущий статус

💡 *Автоопределение:* упомяни любой репозиторий в формате `+"`owner/repo`"+` — бот автоматически переключится на него`, o, r)
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

// ── Telegram helpers ──────────────────────────────────────────

func (b *Bot) tg(text string, threadID int) int {
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	// TODO: add threadID support via raw API
	sent, err := b.api.Send(msg)
	if err != nil {
		log.Printf("send error: %v", err)
		return 0
	}
	return sent.MessageID
}

func (b *Bot) tgPlain(text string, threadID int) int {
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.DisableWebPagePreview = true
	// TODO: add threadID support via raw API
	sent, err := b.api.Send(msg)
	if err != nil {
		log.Printf("send error: %v", err)
		return 0
	}
	return sent.MessageID
}

func (b *Bot) edit(msgID int, text string) {
	if msgID == 0 || text == "" {
		return
	}
	msg := tgbotapi.NewEditMessageText(b.chatID, msgID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	b.api.Send(msg)
}

func (b *Bot) sendWithButtons(text string, keyboard [][]map[string]any, threadID int) int {
	var inlineKeyboard [][]tgbotapi.InlineKeyboardButton
	for _, row := range keyboard {
		var btnRow []tgbotapi.InlineKeyboardButton
		for _, btn := range row {
			btnRow = append(btnRow, tgbotapi.NewInlineKeyboardButtonData(
				btn["text"].(string),
				btn["callback_data"].(string),
			))
		}
		inlineKeyboard = append(inlineKeyboard, btnRow)
	}

	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(inlineKeyboard...)
	// TODO: add threadID support via raw API

	sent, err := b.api.Send(msg)
	if err != nil {
		log.Printf("send error: %v", err)
		return 0
	}
	return sent.MessageID
}

// ── Helpers ───────────────────────────────────────────────────

// extractRepo извлекает owner/repo из текста (например: "покажи PR для gammanik/app")
// Возвращает owner, repo, found
func extractRepo(text string) (string, string, bool) {
	// Ищем паттерн owner/repo (латиница, цифры, дефис, подчеркивание)
	// Примеры: gammanik/peerpack, Gammanik/PeerPack, user123/my-repo_v2
	for i := 0; i < len(text)-2; i++ {
		if text[i] == '/' {
			// Ищем владельца слева от /
			ownerStart := i - 1
			for ownerStart >= 0 && isRepoChar(text[ownerStart]) {
				ownerStart--
			}
			ownerStart++

			// Ищем название репо справа от /
			repoEnd := i + 1
			for repoEnd < len(text) && isRepoChar(text[repoEnd]) {
				repoEnd++
			}

			if ownerStart < i && repoEnd > i+1 {
				owner := text[ownerStart:i]
				repo := text[i+1 : repoEnd]
				if len(owner) > 0 && len(repo) > 0 {
					return owner, repo, true
				}
			}
		}
	}
	return "", "", false
}

func isRepoChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

func extractPRNumber(text string) int {
	text = strings.ToLower(text)
	for i := 0; i < len(text); i++ {
		if text[i] == '#' && i+1 < len(text) {
			var num int
			fmt.Sscanf(text[i+1:], "%d", &num)
			if num > 0 {
				return num
			}
		}
	}

	// Словесные числа
	words := map[string]int{
		"первый": 1, "второй": 2, "третий": 3, "четвертый": 4,
		"пятый": 5, "шестой": 6, "седьмой": 7, "восьмой": 8,
	}
	for word, num := range words {
		if strings.Contains(text, word) {
			return num
		}
	}

	return 0
}
