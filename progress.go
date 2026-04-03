package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ProgressTracker — живое сообщение с прогрессом задачи
// Обновляется каждую секунду пока агент работает
type ProgressTracker struct {
	mu sync.Mutex

	// Telegram
	bot      *Bot
	msgID    int
	threadID int

	// Задача
	taskDesc string
	owner    string
	repo     string
	branch   string
	started  time.Time

	// Шаги
	done    []progressStep
	current *progressStep
	pending []string // ожидаемые шаги (предсказание)

	// Результат
	prNum int
	prURL string
	done_ bool

	stopCh chan struct{}

	// Статистика токенов
	tokens *TokenStats
}

type progressStep struct {
	icon     string
	label    string
	duration time.Duration
	err      bool
}

var stepDurations = map[string]time.Duration{
	"read_file":      3 * time.Second,
	"list_files":     2 * time.Second,
	"write_file":     5 * time.Second,
	"search_code":    4 * time.Second,
	"spawn_subagent": 30 * time.Second,
	"create_pr":      3 * time.Second,
}

func NewProgressTracker(bot *Bot, taskDesc, owner, repo string, threadID int) *ProgressTracker {
	pt := &ProgressTracker{
		bot:      bot,
		threadID: threadID,
		taskDesc: taskDesc,
		owner:    owner,
		repo:     repo,
		started:  time.Now(),
		stopCh:   make(chan struct{}),
		tokens:   NewTokenStats(),
	}

	// Отправляем начальное сообщение
	pt.msgID = bot.tg(pt.render(), threadID)

	// Фоновое обновление каждую секунду
	go pt.loop()

	return pt
}

func (pt *ProgressTracker) loop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pt.refresh()
		case <-pt.stopCh:
			return
		}
	}
}

func (pt *ProgressTracker) refresh() {
	pt.mu.Lock()
	text := pt.render()
	msgID := pt.msgID
	pt.mu.Unlock()
	pt.bot.edit(msgID, text)
}

func (pt *ProgressTracker) render() string {
	elapsed := time.Since(pt.started).Round(time.Second)
	var sb strings.Builder

	// Заголовок
	sb.WriteString(fmt.Sprintf("⚙️ *%s*\n", truncate(pt.taskDesc, 60)))
	sb.WriteString(fmt.Sprintf("`%s/%s`", pt.owner, pt.repo))
	if pt.branch != "" {
		sb.WriteString(fmt.Sprintf(" → `%s`", truncate(pt.branch, 35)))
	}
	sb.WriteString(fmt.Sprintf("\n⏱ %s", fmtDuration(elapsed)))

	// Оценка оставшегося времени
	if est := pt.estimateRemaining(); est > 0 && !pt.done_ {
		sb.WriteString(fmt.Sprintf(" • ест. ещё ~%s", fmtDuration(est)))
	}
	sb.WriteString("\n")

	// Общий прогресс-бар
	totalSteps := len(pt.done) + len(pt.pending)
	if pt.current != nil {
		totalSteps++
	}
	if totalSteps > 0 {
		progress := float64(len(pt.done)) / float64(totalSteps)
		sb.WriteString(renderProgressBar(progress, 10))
		sb.WriteString(fmt.Sprintf(" (%d/%d)\n", len(pt.done), totalSteps))
	}

	sb.WriteString("\n")

	// Завершённые шаги
	for _, step := range pt.done {
		statusIcon := "✅"
		if step.err {
			statusIcon = "❌"
		}
		toolIcon := step.icon
		if toolIcon == "" {
			toolIcon = "⚙️"
		}
		sb.WriteString(fmt.Sprintf("%s %s `%s` _%s_\n", statusIcon, toolIcon, step.label, fmtDuration(step.duration)))
	}

	// Текущий шаг (анимированный)
	if pt.current != nil && !pt.done_ {
		spinner := spinnerFrame(elapsed)
		running := time.Since(pt.started.Add(-elapsed)).Round(time.Second)
		_ = running
		stepElapsed := time.Since(pt.started) - totalDone(pt.done)
		toolIcon := pt.current.icon
		if toolIcon == "" {
			toolIcon = "⚙️"
		}
		sb.WriteString(fmt.Sprintf("%s %s `%s` _(%s)_\n", spinner, toolIcon, pt.current.label,
			fmtDuration(stepElapsed.Round(time.Second))))
	}

	// Ожидаемые шаги
	for _, p := range pt.pending {
		sb.WriteString(fmt.Sprintf("⏳ `%s`\n", p))
	}

	// PR результат
	if pt.prURL != "" {
		sb.WriteString(fmt.Sprintf("\n🚀 [PR #%d](%s)", pt.prNum, pt.prURL))
	}

	// Статистика токенов
	if pt.tokens != nil {
		_, _, _, _, calls := pt.tokens.GetStats()
		if calls > 0 {
			sb.WriteString("\n\n")
			sb.WriteString(pt.tokens.Format())
		}
	}

	if pt.done_ {
		sb.WriteString(fmt.Sprintf("\n\n✅ *Готово за %s*", fmtDuration(elapsed)))
	}

	return sb.String()
}

// API для агента

func (pt *ProgressTracker) SetBranch(branch string) {
	pt.mu.Lock()
	pt.branch = branch
	pt.mu.Unlock()
}

func (pt *ProgressTracker) StartStep(tool, arg string) {
	pt.mu.Lock()
	// Завершаем предыдущий шаг если был
	if pt.current != nil {
		pt.current.duration = time.Since(pt.started) - totalDone(pt.done)
		pt.done = append(pt.done, *pt.current)
	}
	label := tool
	if arg != "" {
		label += "(" + truncate(arg, 30) + ")"
	}
	icon := getToolIcon(tool)
	pt.current = &progressStep{icon: icon, label: label}

	// Убираем из pending если есть
	for i, p := range pt.pending {
		if strings.Contains(p, tool) {
			pt.pending = append(pt.pending[:i], pt.pending[i+1:]...)
			break
		}
	}
	pt.mu.Unlock()
}

func (pt *ProgressTracker) DoneStep(err bool) {
	pt.mu.Lock()
	if pt.current != nil {
		elapsed := time.Since(pt.started) - totalDone(pt.done)
		pt.current.duration = elapsed.Round(time.Second)
		pt.current.err = err
		pt.done = append(pt.done, *pt.current)
		pt.current = nil
	}
	pt.mu.Unlock()
}

func (pt *ProgressTracker) SetPR(num int, url string) {
	pt.mu.Lock()
	pt.prNum = num
	pt.prURL = url
	pt.mu.Unlock()
}

func (pt *ProgressTracker) Finish() {
	pt.mu.Lock()
	if pt.current != nil {
		pt.DoneStep(false)
	}
	pt.done_ = true
	pt.pending = nil
	pt.mu.Unlock()
	pt.refresh()
	close(pt.stopCh)
}

func (pt *ProgressTracker) Error(msg string) {
	pt.mu.Lock()
	if pt.current != nil {
		pt.current.err = true
		pt.done = append(pt.done, *pt.current)
		pt.current = nil
	}
	pt.done_ = true
	pt.pending = nil
	pt.mu.Unlock()
	pt.refresh()
	close(pt.stopCh)
	pt.bot.tg("❌ "+msg, pt.threadID)
}

func (pt *ProgressTracker) AddTokenUsage(input, output, cacheRead, cacheWrite int) {
	if pt.tokens != nil {
		pt.tokens.AddUsage(input, output, cacheRead, cacheWrite)
	}
}

func (pt *ProgressTracker) estimateRemaining() time.Duration {
	var est time.Duration
	for _, p := range pt.pending {
		for tool, d := range stepDurations {
			if strings.Contains(p, tool) {
				est += d
				break
			}
		}
		if est == 0 {
			est += 5 * time.Second // дефолт
		}
	}
	if pt.current != nil {
		if d, ok := stepDurations[strings.Split(pt.current.label, "(")[0]]; ok {
			stepElapsed := time.Since(pt.started) - totalDone(pt.done)
			if d > stepElapsed {
				est += d - stepElapsed
			}
		}
	}
	return est
}

// ── Helpers ──────────────────────────────────────────────────

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

func spinnerFrame(elapsed time.Duration) string {
	frames := []string{"🔄", "⚙️", "🔧", "⚙️"}
	return frames[int(elapsed.Seconds())%len(frames)]
}

func totalDone(steps []progressStep) time.Duration {
	var total time.Duration
	for _, s := range steps {
		total += s.duration
	}
	return total
}

// renderProgressBar — визуальный прогресс-бар с эмоджи
// progress: 0.0 - 1.0 (например, 0.7 = 70%)
// width: количество блоков (обычно 10)
func renderProgressBar(progress float64, width int) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	filled := int(progress * float64(width))
	empty := width - filled

	bar := strings.Repeat("▓", filled) + strings.Repeat("░", empty)
	percentage := int(progress * 100)
	return fmt.Sprintf("%s %d%%", bar, percentage)
}

// getToolIcon — иконки для разных типов тулов
func getToolIcon(tool string) string {
	icons := map[string]string{
		"read_file":      "📖",
		"write_file":     "✏️",
		"list_files":     "📁",
		"search_code":    "🔍",
		"search_history": "🔎",
		"get_summary":    "📋",
		"spawn_subagent": "🤖",
		"orchestrate":    "🎯",
		"create_pr":      "🚀",
		"done":           "✅",
	}
	if icon, ok := icons[tool]; ok {
		return icon
	}
	return "⚙️" // дефолтная иконка
}
