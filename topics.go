package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// TopicManager управляет форум-тредами в Telegram-группе
// Каждый проект (репо) получает свой тред
type TopicManager struct {
	mu     sync.RWMutex
	topics map[string]int // "owner/repo" -> message_thread_id
	chatID int64
	token  string
}

func NewTopicManager(token string, chatID int64) *TopicManager {
	return &TopicManager{
		topics: map[string]int{
			// Дефолтные топики создаём при первом использовании
		},
		chatID: chatID,
		token:  token,
	}
}

// GetOrCreate возвращает thread_id для репо, создаёт топик если нет
func (tm *TopicManager) GetOrCreate(owner, repo string) int {
	key := owner + "/" + repo
	tm.mu.RLock()
	id, ok := tm.topics[key]
	tm.mu.RUnlock()
	if ok {
		return id
	}

	// Создаём новый топик
	name := repoEmoji(repo) + " " + repo
	threadID, err := tm.createTopic(name, topicColor(repo))
	if err != nil {
		// Если не получилось (нет прав, не форум-группа) — возвращаем 0 (основной чат)
		return 0
	}

	tm.mu.Lock()
	tm.topics[key] = threadID
	tm.mu.Unlock()
	return threadID
}

// GetGeneral возвращает thread_id для общего топика (не привязанного к репо)
func (tm *TopicManager) GetGeneral() int {
	tm.mu.RLock()
	id, ok := tm.topics["general"]
	tm.mu.RUnlock()
	if ok {
		return id
	}
	threadID, err := tm.createTopic("🤖 General", 0x6FB9F0)
	if err != nil {
		return 0
	}
	tm.mu.Lock()
	tm.topics["general"] = threadID
	tm.mu.Unlock()
	return threadID
}

func (tm *TopicManager) createTopic(name string, color int) (int, error) {
	payload := map[string]any{
		"chat_id": tm.chatID,
		"name":    name,
	}
	if color != 0 {
		payload["icon_color"] = color
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/createForumTopic", tm.token)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageThreadID int `json:"message_thread_id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return 0, fmt.Errorf("createForumTopic: %s", result.Description)
	}
	return result.Result.MessageThreadID, nil
}

// renameTopic — переименовывает топик (например, добавляет ✅ когда задача готова)
func (tm *TopicManager) editTopicName(threadID int, name string) {
	payload := map[string]any{
		"chat_id":           tm.chatID,
		"message_thread_id": threadID,
		"name":              name,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/editForumTopic", tm.token)
	http.Post(url, "application/json", bytes.NewReader(body))
}

// DeleteTopic — удаляет топик по имени (owner/repo)
func (tm *TopicManager) DeleteTopic(name string) error {
	tm.mu.Lock()
	threadID, ok := tm.topics[name]
	if !ok {
		tm.mu.Unlock()
		return fmt.Errorf("топик '%s' не найден", name)
	}
	delete(tm.topics, name)
	tm.mu.Unlock()

	// Закрываем топик в Telegram
	payload := map[string]any{
		"chat_id":           tm.chatID,
		"message_thread_id": threadID,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/closeForumTopic", tm.token)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return fmt.Errorf("closeForumTopic: %s", result.Description)
	}
	return nil
}

// GetAllTopics — возвращает список всех топиков
func (tm *TopicManager) GetAllTopics() []string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	topics := make([]string, 0, len(tm.topics))
	for name := range tm.topics {
		topics = append(topics, name)
	}
	return topics
}

func repoEmoji(repo string) string {
	switch repo {
	case "PeerPack", "peerpack":
		return "📦"
	case "claude-tg", "claude_tg":
		return "🤖"
	case "SkyFarm", "skyfarm":
		return "🌱"
	default:
		return "💻"
	}
}

func topicColor(repo string) int {
	// Цвета топиков (Telegram поддерживает 7 цветов)
	colors := []int{0x6FB9F0, 0xFFD67E, 0xCB86DB, 0x8EEE98, 0xFF93B2, 0xFB6F5F}
	hash := 0
	for _, c := range repo {
		hash = (hash*31 + int(c)) % len(colors)
	}
	return colors[hash]
}
