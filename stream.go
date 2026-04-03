package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// streamClaude — надёжный SSE стриминг через bufio.Scanner
// Обновляет Telegram каждые 400мс
func (b *Bot) streamClaude(system, userText string, onChunk func(string)) (string, error) {
	log.Printf("streamClaude: start, userText=%q", truncate(userText, 50))

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-4-5-20251101",
		"max_tokens": 2048,
		"stream":     true,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": userText}},
	})

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", b.cfg.AnthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	log.Printf("streamClaude: sending request to Anthropic API")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("streamClaude: request error - %v", err)
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("streamClaude: got response, status=%d", resp.StatusCode)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("streamClaude: API error - %s", string(body))
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(body))
	}

	var full strings.Builder
	lastUpdate := time.Now()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// SSE формат: "data: {...}"
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
			// Обновляем TG не чаще 1 раза в 400мс
			if time.Since(lastUpdate) > 400*time.Millisecond {
				onChunk(full.String())
				lastUpdate = time.Now()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		// Если что-то уже накопили — возвращаем это
		if full.Len() > 0 {
			return full.String(), nil
		}
		return "", fmt.Errorf("scan: %w", err)
	}

	result := full.String()
	if result == "" {
		// Fallback — пробуем без стриминга
		return b.callClaudeOnce(system, userText)
	}
	return result, nil
}

// callClaudeOnce — обычный (не стриминговый) вызов, используется как fallback
func (b *Bot) callClaudeOnce(system, userText string) (string, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-4-5-20251101",
		"max_tokens": 2048,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": userText}},
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("x-api-key", b.cfg.AnthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

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
		return "", fmt.Errorf("Claude: %s", result.Error.Message)
	}
	for _, c := range result.Content {
		if c.Type == "text" && c.Text != "" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("пустой ответ от Claude")
}

// callHaiku — дешёвый Claude Haiku для парсинга (напоминалки, роутинг)
func (b *Bot) callHaiku(system, text string) (string, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 300,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": text}},
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
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
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	for _, c := range result.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("empty haiku response")
}
