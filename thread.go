package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
)

// sendThreadRaw — отправляет сообщение в тред через raw Telegram API
// (go-telegram-bot-api v5.5.1 не поддерживает message_thread_id нативно)
func (b *Bot) sendRaw(text string, threadID int) {
	payload := map[string]any{
		"chat_id":                  b.chatID,
		"text":                     text,
		"parse_mode":               "Markdown",
		"disable_web_page_preview": true,
	}
	if threadID != 0 {
		payload["message_thread_id"] = threadID
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.cfg.TelegramToken)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("sendRaw: %v", err)
		return
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
}

// sendWithButtons — отправляет сообщение с inline-кнопками через raw API
func (b *Bot) sendWithButtons(text string, keyboard [][]map[string]any, threadID int) {
	payload := map[string]any{
		"chat_id":                  strconv.FormatInt(b.chatID, 10),
		"text":                     text,
		"parse_mode":               "Markdown",
		"disable_web_page_preview": true,
		"reply_markup":             map[string]any{"inline_keyboard": keyboard},
	}
	if threadID != 0 {
		payload["message_thread_id"] = threadID
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.cfg.TelegramToken)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("sendWithButtons: %v", err)
		return
	}
	resp.Body.Close()
}

// editRaw — редактирует сообщение через raw API
func (b *Bot) editRaw(msgID int, text string) {
	payload := map[string]any{
		"chat_id":                  b.chatID,
		"message_id":               msgID,
		"text":                     text,
		"parse_mode":               "Markdown",
		"disable_web_page_preview": true,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/editMessageText", b.cfg.TelegramToken)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("editRaw: msgID=%d err=%v", msgID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("editRaw: msgID=%d status=%d body=%s", msgID, resp.StatusCode, string(body))
	}
}

// sendMessageRaw — возвращает message_id для последующего редактирования
func (b *Bot) sendMessageRaw(text string, threadID int) int {
	payload := map[string]any{
		"chat_id":                  strconv.FormatInt(b.chatID, 10),
		"text":                     text,
		"parse_mode":               "Markdown",
		"disable_web_page_preview": true,
	}
	if threadID != 0 {
		payload["message_thread_id"] = threadID
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.cfg.TelegramToken)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("sendMessageRaw: error - %v", err)
		return 0
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("sendMessageRaw: decode error - %v", err)
		return 0
	}

	if !result.OK {
		log.Printf("sendMessageRaw: API error - %s", result.Description)
		return 0
	}

	msgID := result.Result.MessageID
	log.Printf("sendMessageRaw: sent msgID=%d threadID=%d", msgID, threadID)
	return msgID
}
