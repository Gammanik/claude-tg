package main

import (
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// MessageHistory - хранилище истории сообщений для поиска
type MessageHistory struct {
	bot      *tgbotapi.BotAPI
	chatID   int64
	messages []HistoryMessage
}

type HistoryMessage struct {
	MessageID int
	ThreadID  int
	From      string
	Text      string
	Timestamp time.Time
}

func NewMessageHistory(bot *tgbotapi.BotAPI, chatID int64) *MessageHistory {
	return &MessageHistory{
		bot:      bot,
		chatID:   chatID,
		messages: make([]HistoryMessage, 0, 1000),
	}
}

// AddMessage - добавляет сообщение в историю
func (h *MessageHistory) AddMessage(msgID, threadID int, from, text string) {
	h.messages = append(h.messages, HistoryMessage{
		MessageID: msgID,
		ThreadID:  threadID,
		From:      from,
		Text:      text,
		Timestamp: time.Now(),
	})

	// Ограничиваем размер истории последними 1000 сообщениями
	if len(h.messages) > 1000 {
		h.messages = h.messages[len(h.messages)-1000:]
	}
}

// Search - поиск по истории сообщений
func (h *MessageHistory) Search(query string, limit int) []HistoryMessage {
	if limit <= 0 {
		limit = 10
	}

	query = strings.ToLower(query)
	var results []HistoryMessage

	// Поиск с конца (самые свежие сообщения)
	for i := len(h.messages) - 1; i >= 0 && len(results) < limit; i-- {
		msg := h.messages[i]
		if strings.Contains(strings.ToLower(msg.Text), query) {
			results = append(results, msg)
		}
	}

	return results
}

// SearchInThread - поиск в конкретном топике/треде
func (h *MessageHistory) SearchInThread(threadID int, query string, limit int) []HistoryMessage {
	if limit <= 0 {
		limit = 10
	}

	query = strings.ToLower(query)
	var results []HistoryMessage

	for i := len(h.messages) - 1; i >= 0 && len(results) < limit; i-- {
		msg := h.messages[i]
		if msg.ThreadID == threadID && strings.Contains(strings.ToLower(msg.Text), query) {
			results = append(results, msg)
		}
	}

	return results
}

// GetRecentMessages - получить последние N сообщений
func (h *MessageHistory) GetRecentMessages(count int) []HistoryMessage {
	if count <= 0 {
		count = 10
	}
	if count > len(h.messages) {
		count = len(h.messages)
	}

	return h.messages[len(h.messages)-count:]
}

// FormatResults - форматирует результаты поиска
func FormatSearchResults(results []HistoryMessage) string {
	if len(results) == 0 {
		return "Ничего не найдено"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Найдено %d сообщений:\n\n", len(results)))

	for i, msg := range results {
		timeAgo := time.Since(msg.Timestamp).Round(time.Minute)
		text := truncateS(msg.Text, 150)
		sb.WriteString(fmt.Sprintf("%d. [%s назад] %s: %s\n",
			i+1, fmtDuration(timeAgo), msg.From, text))
	}

	return sb.String()
}

// GetThreadSummary - получает саммари для треда (последние N сообщений)
func (h *MessageHistory) GetThreadSummary(threadID int, count int) string {
	if count <= 0 {
		count = 20
	}

	var threadMessages []HistoryMessage
	for i := len(h.messages) - 1; i >= 0 && len(threadMessages) < count; i-- {
		msg := h.messages[i]
		if msg.ThreadID == threadID {
			threadMessages = append([]HistoryMessage{msg}, threadMessages...)
		}
	}

	if len(threadMessages) == 0 {
		return "Нет сообщений в этом треде"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Последние %d сообщений в треде:\n\n", len(threadMessages)))

	for _, msg := range threadMessages {
		timeStr := msg.Timestamp.Format("15:04")
		text := truncateS(msg.Text, 100)
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", timeStr, msg.From, text))
	}

	return sb.String()
}
