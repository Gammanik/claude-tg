package main

import (
	"fmt"
	"sync"
	"time"
)

// TokenStats - статистика использования токенов
type TokenStats struct {
	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	cacheRead    int // для Claude prompt caching
	cacheWrite   int
	apiCalls     int
	startTime    time.Time
}

func NewTokenStats() *TokenStats {
	return &TokenStats{
		startTime: time.Now(),
	}
}

// AddUsage - добавляет информацию об использовании из API ответа
func (ts *TokenStats) AddUsage(input, output, cacheRead, cacheWrite int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.inputTokens += input
	ts.outputTokens += output
	ts.cacheRead += cacheRead
	ts.cacheWrite += cacheWrite
	ts.apiCalls++
}

// GetStats - возвращает текущую статистику
func (ts *TokenStats) GetStats() (input, output, cacheRead, cacheWrite, calls int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.inputTokens, ts.outputTokens, ts.cacheRead, ts.cacheWrite, ts.apiCalls
}

// Format - форматирует статистику для отображения
func (ts *TokenStats) Format() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	total := ts.inputTokens + ts.outputTokens
	elapsed := time.Since(ts.startTime)

	result := fmt.Sprintf("🔢 Токены: %s in / %s out",
		formatNumber(ts.inputTokens),
		formatNumber(ts.outputTokens))

	if ts.cacheRead > 0 || ts.cacheWrite > 0 {
		result += fmt.Sprintf("\n💾 Кэш: %s read / %s write",
			formatNumber(ts.cacheRead),
			formatNumber(ts.cacheWrite))
	}

	// Оценка стоимости для Claude Sonnet 4
	// $3 per MTok input, $15 per MTok output
	// Кэш read: $0.30/MTok, кэш write: $3.75/MTok
	costInput := float64(ts.inputTokens) / 1_000_000 * 3.0
	costOutput := float64(ts.outputTokens) / 1_000_000 * 15.0
	costCacheRead := float64(ts.cacheRead) / 1_000_000 * 0.30
	costCacheWrite := float64(ts.cacheWrite) / 1_000_000 * 3.75
	totalCost := costInput + costOutput + costCacheRead + costCacheWrite

	result += fmt.Sprintf("\n💰 ~$%.4f (%s)", totalCost, fmtDuration(elapsed.Round(time.Second)))
	result += fmt.Sprintf("\n📊 %s токенов / %d вызовов", formatNumber(total), ts.apiCalls)

	return result
}

// formatNumber - форматирует число с разделителями тысяч
func formatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
}

// UsageLimits - лимиты API для предупреждений
type UsageLimits struct {
	// Claude API limits
	MaxTokensPerHour int // ~2 часа работы
	MaxTokensPerWeek int

	// Tracking
	hourlyUsage map[int64]int // timestamp hour -> tokens
	weeklyUsage map[int64]int // timestamp day -> tokens
	mu          sync.Mutex
}

func NewUsageLimits() *UsageLimits {
	return &UsageLimits{
		MaxTokensPerHour: 1_000_000,  // 1M токенов в час
		MaxTokensPerWeek: 10_000_000, // 10M токенов в неделю
		hourlyUsage:      make(map[int64]int),
		weeklyUsage:      make(map[int64]int),
	}
}

// CheckLimit - проверяет не превышен ли лимит
func (ul *UsageLimits) CheckLimit(tokens int) (bool, string) {
	ul.mu.Lock()
	defer ul.mu.Unlock()

	now := time.Now()
	hourKey := now.Unix() / 3600
	dayKey := now.Unix() / 86400

	// Добавляем текущее использование
	ul.hourlyUsage[hourKey] += tokens
	ul.weeklyUsage[dayKey] += tokens

	// Очищаем старые записи (старше недели)
	weekAgo := now.Add(-7*24*time.Hour).Unix() / 86400
	for k := range ul.weeklyUsage {
		if k < weekAgo {
			delete(ul.weeklyUsage, k)
		}
	}

	// Считаем использование за последний час
	hourTotal := 0
	hourAgo := now.Add(-time.Hour).Unix() / 3600
	for k, v := range ul.hourlyUsage {
		if k >= hourAgo {
			hourTotal += v
		}
	}

	// Считаем использование за последнюю неделю
	weekTotal := 0
	for _, v := range ul.weeklyUsage {
		weekTotal += v
	}

	// Проверяем лимиты
	if hourTotal > ul.MaxTokensPerHour {
		pct := float64(hourTotal) / float64(ul.MaxTokensPerHour) * 100
		return false, fmt.Sprintf("⚠️ Превышен часовой лимит! Использовано %.0f%% (%s / %s токенов)\nПодожди немного перед следующим запросом.",
			pct, formatNumber(hourTotal), formatNumber(ul.MaxTokensPerHour))
	}

	if weekTotal > ul.MaxTokensPerWeek {
		pct := float64(weekTotal) / float64(ul.MaxTokensPerWeek) * 100
		return false, fmt.Sprintf("⚠️ Превышен недельный лимит! Использовано %.0f%% (%s / %s токенов)",
			pct, formatNumber(weekTotal), formatNumber(ul.MaxTokensPerWeek))
	}

	// Предупреждения при приближении к лимитам
	hourPct := float64(hourTotal) / float64(ul.MaxTokensPerHour) * 100
	weekPct := float64(weekTotal) / float64(ul.MaxTokensPerWeek) * 100

	if hourPct > 80 || weekPct > 80 {
		return true, fmt.Sprintf("⚠️ Приближаемся к лимиту: час %.0f%%, неделя %.0f%%",
			hourPct, weekPct)
	}

	return true, ""
}
