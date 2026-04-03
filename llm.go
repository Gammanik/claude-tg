package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ModelTier определяет уровень модели
type ModelTier int

const (
	TierHaiku  ModelTier = iota // Быстрая дешёвая - парсинг, роутинг
	TierSonnet                  // Средняя - чат, простые задачи
	TierOpus                    // Мощная - сложные задачи, планирование
)

// LLMClient - единый клиент для всех LLM провайдеров с иерархией моделей
type LLMClient struct {
	anthropicKey string
	deepseekKey  string
	provider     string // "anthropic" или "deepseek"
}

func NewLLMClient(anthropicKey, deepseekKey, provider string) *LLMClient {
	return &LLMClient{
		anthropicKey: anthropicKey,
		deepseekKey:  deepseekKey,
		provider:     provider,
	}
}

// Call вызывает модель нужного уровня
func (c *LLMClient) Call(tier ModelTier, system, user string) (string, error) {
	if c.provider == "anthropic" || c.provider == "claude" {
		return c.callAnthropic(tier, system, user, false)
	}
	return c.callDeepSeek(system, user)
}

// Stream вызывает модель со стримингом (только для Anthropic Sonnet/Opus)
func (c *LLMClient) Stream(tier ModelTier, system, user string, onChunk func(string)) (string, error) {
	if c.provider != "anthropic" && c.provider != "claude" {
		// Fallback на обычный вызов для не-Anthropic
		return c.callDeepSeek(system, user)
	}
	return c.callAnthropic(tier, system, user, true, onChunk)
}

// callAnthropic вызывает Anthropic API с выбором модели по tier
func (c *LLMClient) callAnthropic(tier ModelTier, system, user string, stream bool, callbacks ...func(string)) (string, error) {
	if c.anthropicKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY не найден. Получить: https://console.anthropic.com/settings/keys")
	}

	model := c.selectAnthropicModel(tier)
	maxTokens := c.getMaxTokens(tier)

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}

	// Добавляем prompt caching для Sonnet/Opus (экономит токены)
	if tier >= TierSonnet && len(system) > 1000 {
		body["system"] = []map[string]any{
			{"type": "text", "text": system, "cache_control": map[string]string{"type": "ephemeral"}},
		}
	}

	if stream {
		body["stream"] = true
		return c.streamAnthropic(body, callbacks...)
	}

	return c.callAnthropicOnce(body)
}

func (c *LLMClient) selectAnthropicModel(tier ModelTier) string {
	switch tier {
	case TierHaiku:
		return "claude-haiku-4-5-20251001"
	case TierSonnet:
		return "claude-sonnet-4-5-20250929"
	case TierOpus:
		return "claude-opus-4-5-20251101"
	default:
		return "claude-sonnet-4-5-20250929"
	}
}

func (c *LLMClient) getMaxTokens(tier ModelTier) int {
	switch tier {
	case TierHaiku:
		return 1024
	case TierSonnet:
		return 4096
	case TierOpus:
		return 16384
	default:
		return 4096
	}
}

func (c *LLMClient) callAnthropicOnce(body map[string]any) (string, error) {
	reqBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("x-api-key", c.anthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Anthropic API %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("Anthropic: %s", result.Error.Message)
	}
	for _, c := range result.Content {
		if c.Type == "text" && c.Text != "" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("пустой ответ от Anthropic")
}

func (c *LLMClient) streamAnthropic(body map[string]any, callbacks ...func(string)) (string, error) {
	var onChunk func(string)
	if len(callbacks) > 0 {
		onChunk = callbacks[0]
	}

	reqBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("x-api-key", c.anthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var full strings.Builder
	lastUpdate := time.Now()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}

		if ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
			full.WriteString(ev.Delta.Text)
			// Обновляем каждые 500ms
			if onChunk != nil && time.Since(lastUpdate) > 500*time.Millisecond {
				onChunk(full.String())
				lastUpdate = time.Now()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if full.Len() > 0 {
			return full.String(), nil
		}
		return "", fmt.Errorf("scan: %w", err)
	}

	result := full.String()
	if result == "" {
		// Fallback на обычный вызов
		delete(body, "stream")
		return c.callAnthropicOnce(body)
	}
	return result, nil
}

func (c *LLMClient) callDeepSeek(system, user string) (string, error) {
	if c.deepseekKey == "" {
		return "", fmt.Errorf("DEEPSEEK_API_KEY не найден. Получить: https://platform.deepseek.com")
	}

	body, _ := json.Marshal(map[string]any{
		"model":       "deepseek-chat",
		"max_tokens":  8192,
		"temperature": 0.1,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})

	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.deepseekKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("DeepSeek API %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("DeepSeek: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("пустой ответ от DeepSeek")
	}
	return result.Choices[0].Message.Content, nil
}

// RouteIntent классифицирует запрос пользователя (используя Haiku - быстро и дёшево)
func (c *LLMClient) RouteIntent(text, owner, repo string) (string, error) {
	system := fmt.Sprintf(`You are a request router for GitHub bot. Repo: %s/%s.

Classify user intent into ONE of:
- "list_prs" - show pull requests
- "list_repos" - list user repositories (queries like "my repos", "what repos", "list repositories")
- "merge_pr" - merge specific PR
- "close_pr" - close PR
- "code" - any code changes/fixes/features
- "chat" - questions, general conversation

Output ONLY the intent name, nothing else.`, owner, repo)

	return c.Call(TierHaiku, system, text)
}
