package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Intent - распознанное намерение пользователя
type Intent struct {
	Type   string            `json:"type"`   // "list_prs", "merge_pr", "close_pr", "coding_task", "chat", etc.
	Params map[string]string `json:"params"` // параметры (pr_number, title, etc.)
}

// RouteRequest использует LLM для определения намерения пользователя
func (b *Bot) RouteRequest(text string) (*Intent, error) {
	o, r := b.currentRepo()

	system := fmt.Sprintf(`You are a request router for a GitHub bot. Repo: %s/%s.

Classify user requests into one of these intents and extract parameters:

**Intents:**
- "list_prs" - show open pull requests (keywords: покажи pr, мои пр, открытые пр, list prs, show prs)
- "merge_pr" - merge a specific PR (keywords: смержи, мердж, merge, влей)
  params: pr_number (extract from text like "#5", "пятый", "номер 5")
- "close_pr" - close PR without merging (keywords: закрой, удали пр, close pr)
  params: pr_number
- "coding_task" - code changes (keywords: добавь, создай, исправь, fix, add, create, refactor, update)
- "chat" - general conversation, questions (anything else)

**Output JSON only:**
{
  "type": "intent_type",
  "params": {"key": "value"}
}

**Examples:**
User: "покажи мои pr"
{"type": "list_prs", "params": {}}

User: "смержи пятый пр"
{"type": "merge_pr", "params": {"pr_number": "5"}}

User: "добавь новую фичу логина"
{"type": "coding_task", "params": {}}

User: "как дела?"
{"type": "chat", "params": {}}

User: "закрой pr #123"
{"type": "close_pr", "params": {"pr_number": "123"}}`, o, r)

	messages := []msg{
		{Role: "system", Content: system},
		{Role: "user", Content: text},
	}

	var response string
	var err error
	if b.cfg.LLMProvider == "claude" {
		response, err = callClaude(b.cfg.AnthropicKey, messages)
	} else {
		response, err = callDeepSeek(b.cfg.DeepSeekKey, messages)
	}
	if err != nil {
		return nil, err
	}

	// Извлекаем JSON из ответа (может быть в коде или просто текстом)
	var intent Intent
	response = strings.TrimSpace(response)
	// Удаляем markdown код блоки если есть
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	if err := json.Unmarshal([]byte(response), &intent); err != nil {
		// Fallback - пробуем найти JSON в тексте
		if start := strings.Index(response, "{"); start >= 0 {
			if end := strings.LastIndex(response, "}"); end > start {
				jsonStr := response[start : end+1]
				if err2 := json.Unmarshal([]byte(jsonStr), &intent); err2 == nil {
					return &intent, nil
				}
			}
		}
		return nil, fmt.Errorf("failed to parse intent: %w", err)
	}

	return &intent, nil
}

// extractPRNumber - извлекает номер PR из текста различными способами
func extractPRNumber(text string) int {
	text = strings.ToLower(text)

	// Паттерны для поиска номера
	patterns := []string{
		`#(\d+)`,           // #123
		`pr\s*(\d+)`,       // pr 123
		`номер\s*(\d+)`,    // номер 123
		`(\d+)-?й`,         // 5-й, пятый
		`(\d+)\s*пр`,       // 5 пр
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(text); len(matches) > 1 {
			if num, err := strconv.Atoi(matches[1]); err == nil {
				return num
			}
		}
	}

	// Словесные числа
	words := map[string]int{
		"первый": 1, "второй": 2, "третий": 3, "четвертый": 4,
		"пятый": 5, "шестой": 6, "седьмой": 7, "восьмой": 8,
		"девятый": 9, "десятый": 10,
	}
	for word, num := range words {
		if strings.Contains(text, word) {
			return num
		}
	}

	return 0
}
