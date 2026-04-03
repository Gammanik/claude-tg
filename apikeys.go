package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// APIKeyManager управляет API ключами с динамическим запросом через Telegram
type APIKeyManager struct {
	mu   sync.RWMutex
	keys map[string]string // service → key
	bot  *Bot
}

// NewAPIKeyManager создает новый менеджер API ключей
func NewAPIKeyManager(bot *Bot) *APIKeyManager {
	return &APIKeyManager{
		keys: make(map[string]string),
		bot:  bot,
	}
}

// RequestKey запрашивает API ключ для сервиса через approval кнопки
// Если ключ уже есть - возвращает его
// Если нет - показывает инструкции и ждет добавления ключа в .env
func (m *APIKeyManager) RequestKey(service string, threadID int) (string, error) {
	// Проверяем, есть ли уже ключ в памяти
	m.mu.RLock()
	if key, ok := m.keys[service]; ok {
		m.mu.RUnlock()
		return key, nil
	}
	m.mu.RUnlock()

	// Проверяем в environment
	envKey := strings.ToUpper(service) + "_API_KEY"
	if key := os.Getenv(envKey); key != "" {
		// Сохраняем в памяти
		m.mu.Lock()
		m.keys[service] = key
		m.mu.Unlock()
		return key, nil
	}

	// Инструкции где взять ключ
	instructions := map[string]string{
		"openai":    "Получить ключ: https://platform.openai.com/api-keys",
		"groq":      "Получить бесплатный ключ: https://console.groq.com/keys",
		"anthropic": "Получить ключ: https://console.anthropic.com/settings/keys",
		"deepseek":  "Получить ключ: https://platform.deepseek.com",
	}

	instruction := instructions[service]
	if instruction == "" {
		instruction = fmt.Sprintf("Добавьте %s в .env файл", envKey)
	}

	// Отправляем сообщение с кнопками
	msg := fmt.Sprintf("🔑 Требуется API ключ для *%s*\n\n%s\n\nПосле добавления `%s` в .env файл нажмите 'Готово'",
		service, instruction, envKey)

	ch := make(chan bool, 1)
	taskID := fmt.Sprintf("apikey-%s-%d", service, time.Now().Unix())

	m.bot.approvalsMu.Lock()
	m.bot.approvals[taskID] = ch
	m.bot.approvalsMu.Unlock()

	m.bot.sendWithButtons(msg, [][]map[string]any{
		{
			{"text": "✅ Готово, ключ добавлен", "callback_data": "approve:" + taskID},
			{"text": "❌ Отмена", "callback_data": "reject:" + taskID},
		},
	}, threadID)

	select {
	case ok := <-ch:
		if !ok {
			return "", fmt.Errorf("user cancelled API key request for %s", service)
		}

		// Перечитываем .env
		godotenv.Load()
		key := os.Getenv(envKey)
		if key == "" {
			return "", fmt.Errorf("key %s not found in .env after approval", envKey)
		}

		// Сохраняем в памяти
		m.mu.Lock()
		m.keys[service] = key
		m.mu.Unlock()

		m.bot.tg(fmt.Sprintf("✅ API ключ для *%s* добавлен", service), threadID)
		return key, nil

	case <-time.After(10 * time.Minute):
		m.bot.approvalsMu.Lock()
		delete(m.bot.approvals, taskID)
		m.bot.approvalsMu.Unlock()
		return "", fmt.Errorf("timeout waiting for API key for %s", service)
	}
}

// GetKey возвращает ключ если он уже есть (без запроса)
func (m *APIKeyManager) GetKey(service string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keys[service]
}

// HasKey проверяет наличие ключа
func (m *APIKeyManager) HasKey(service string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.keys[service]
	if ok {
		return true
	}

	// Проверяем в environment
	envKey := strings.ToUpper(service) + "_API_KEY"
	return os.Getenv(envKey) != ""
}
