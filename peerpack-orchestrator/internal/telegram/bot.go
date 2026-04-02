package telegram

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/gammanik/peerpack-bot/internal/agent"
	"github.com/gammanik/peerpack-bot/internal/github"
)

type Config struct {
	Token         string
	AllowedChatID string
	Agent         *agent.Agent
	GitHubClient  *github.Client
}

type Bot struct {
	api     *tgbotapi.BotAPI
	cfg     Config
	chatID  int64
	tasks   sync.Map // taskID -> *agent.Task
}

func NewBot(cfg Config) *Bot {
	id, err := strconv.ParseInt(cfg.AllowedChatID, 10, 64)
	if err != nil {
		log.Fatalf("invalid TELEGRAM_CHAT_ID: %v", err)
	}
	return &Bot{cfg: cfg, chatID: id}
}

func (b *Bot) Start() error {
	api, err := tgbotapi.NewBotAPI(b.cfg.Token)
	if err != nil {
		return fmt.Errorf("telegram init: %w", err)
	}
	b.api = api
	log.Printf("Authorized as @%s", api.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := api.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			b.handleCallback(update.CallbackQuery)
			continue
		}
		if update.Message.Chat.ID != b.chatID {
			continue // игнорируем чужих
		}
		go b.handleMessage(update.Message)
	}
	return nil
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)

	switch {
	case text == "/start" || text == "/help":
		b.send(helpText)

	case text == "/status":
		b.handleStatus()

	case text == "/prs":
		b.handleListPRs()

	case strings.HasPrefix(text, "/cancel"):
		b.send("❌ Отмена задачи пока не реализована — убей задачу вручную если нужно")

	default:
		// Любое другое сообщение — это задача для агента
		b.handleTask(text)
	}
}

func (b *Bot) handleTask(taskDesc string) {
	b.send("⚙️ Получил задачу, анализирую репо...")

	task, err := b.cfg.Agent.StartTask(taskDesc)
	if err != nil {
		b.send(fmt.Sprintf("❌ Ошибка запуска: %v", err))
		return
	}

	b.tasks.Store(task.ID, task)
	b.send(fmt.Sprintf("🔍 Задача #%s запущена\nВетка: `%s`", task.ID, task.Branch))

	// Стримим прогресс агента
	for step := range task.Steps {
		switch step.Type {
		case agent.StepThought:
			b.send(fmt.Sprintf("💭 _%s_", step.Content))
		case agent.StepAction:
			b.send(fmt.Sprintf("🔧 `%s`", step.Content))
		case agent.StepPR:
			b.sendWithButtons(
				fmt.Sprintf("🚀 PR создан!\n[#%d](%s)\n\nЖду тесты...", step.PRNumber, step.PRURL),
				step.PRNumber,
			)
			// Запускаем watching в горутине
			go b.watchPR(task.ID, step.PRNumber)
		case agent.StepError:
			b.send(fmt.Sprintf("❌ Ошибка: %s", step.Content))
		case agent.StepDone:
			b.send(fmt.Sprintf("✅ Готово!\n%s", step.Content))
		}
	}
}

func (b *Bot) watchPR(taskID string, prNumber int) {
	result := b.cfg.GitHubClient.WatchChecks(prNumber)

	switch result.Status {
	case "success":
		err := b.cfg.GitHubClient.MergePR(prNumber)
		if err != nil {
			b.send(fmt.Sprintf("⚠️ Тесты прошли, но автомерж не удался: %v\nМержи вручную: https://github.com/Gammanik/PeerPack/pull/%d", err, prNumber))
			return
		}
		b.send(fmt.Sprintf("✅ PR #%d смержен! Задача завершена 🎉", prNumber))
		b.tasks.Delete(taskID)

	case "failure":
		// Достаём лог и пробуем починить
		failLog := b.cfg.GitHubClient.GetFailedCheckLog(prNumber)
		b.send(fmt.Sprintf("❌ Тесты упали:\n```\n%s\n```\nПробую починить...", truncate(failLog, 800)))
		b.cfg.Agent.FixFailedTests(taskID, failLog)

	case "timeout":
		b.send(fmt.Sprintf("⏰ Тесты не завершились за 20 мин. Проверь вручную:\nhttps://github.com/Gammanik/PeerPack/pull/%d", prNumber))
	}
}

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	if q == nil || q.Message == nil {
		return
	}
	// Кнопка "Закрыть PR"
	if strings.HasPrefix(q.Data, "close_pr:") {
		prNum, _ := strconv.Atoi(strings.TrimPrefix(q.Data, "close_pr:"))
		b.cfg.GitHubClient.ClosePR(prNum)
		b.send(fmt.Sprintf("🗑️ PR #%d закрыт", prNum))
	}
	b.api.Request(tgbotapi.NewCallback(q.ID, ""))
}

func (b *Bot) handleStatus() {
	count := 0
	b.tasks.Range(func(_, _ any) bool { count++; return true })
	if count == 0 {
		b.send("😴 Нет активных задач")
		return
	}
	b.send(fmt.Sprintf("⚙️ Активных задач: %d", count))
}

func (b *Bot) handleListPRs() {
	prs, err := b.cfg.GitHubClient.ListOpenPRs()
	if err != nil {
		b.send(fmt.Sprintf("❌ %v", err))
		return
	}
	if len(prs) == 0 {
		b.send("🟢 Открытых PR нет")
		return
	}
	var sb strings.Builder
	sb.WriteString("📋 *Открытые PR:*\n")
	for _, pr := range prs {
		sb.WriteString(fmt.Sprintf("• [#%d %s](%s)\n", pr.Number, pr.Title, pr.URL))
	}
	b.send(sb.String())
}

func (b *Bot) send(text string) {
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("send error: %v", err)
	}
}

func (b *Bot) sendWithButtons(text string, prNumber int) {
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = true
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(
				"🗑️ Закрыть PR",
				fmt.Sprintf("close_pr:%d", prNumber),
			),
		),
	)
	b.api.Send(msg)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}

const helpText = `🤖 *PeerPack Dev Bot*

Просто напиши задачу обычным текстом:
_"добавь экран трекинга посылки"_
_"рефактори SearchCouriers на страницы"_
_"добавь валидацию в форму создания посылки"_

Я создам ветку, напишу код, открою PR и смержу когда тесты пройдут.

*Команды:*
/status — активные задачи
/prs — открытые PR
/help — это сообщение`
