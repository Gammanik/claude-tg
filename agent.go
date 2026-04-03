package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type StepType string

const (
	StepThought StepType = "thought"
	StepAction  StepType = "action"
	StepPR      StepType = "pr"
	StepError   StepType = "error"
	StepDone    StepType = "done"
)

type Step struct {
	Type     StepType
	Content  string
	PRNumber int
	PRURL    string
}

type Task struct {
	ID          string
	Description string
	Owner, Repo string
	Branch      string
	Steps       chan Step
	StartedAt   time.Time
}

type Agent struct {
	cfg         Config
	gh          *GitHubClient
	owner, repo string
	progress    *ProgressTracker // может быть nil
	bot         *Bot             // для создания прогресс-трекеров суб-агентов
	threadID    int
}

func NewAgent(cfg Config, owner, repo string) *Agent {
	return &Agent{cfg: cfg, gh: NewGitHubClient(cfg.GitHubToken, owner, repo),
		owner: owner, repo: repo}
}

func (a *Agent) WithProgress(pt *ProgressTracker) *Agent {
	a.progress = pt
	return a
}

func (a *Agent) WithBot(bot *Bot, threadID int) *Agent {
	a.bot = bot
	a.threadID = threadID
	return a
}

func (a *Agent) Run(task *Task) {
	task.StartedAt = time.Now()
	defer func() {
		if r := recover(); r != nil {
			task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("panic: %v", r)}
			close(task.Steps)
		}
	}()
	defer close(task.Steps)

	a.think("Читаю структуру репо...", task)
	ctx := a.buildContext(task.Description)

	// Создаём ветку
	branch := makeBranch(task.Description, task.ID)
	if task.Branch != "" {
		branch = task.Branch
	}

	a.act("git checkout -b "+branch, task)
	if task.Branch == "" {
		if err := a.gh.CreateBranch(branch); err != nil {
			task.Steps <- Step{Type: StepError, Content: "CreateBranch: " + err.Error()}
			return
		}
	}
	task.Branch = branch
	if a.progress != nil {
		a.progress.SetBranch(branch)
	}

	// ReAct loop
	messages := []msg{
		{Role: "system", Content: systemPrompt(a.owner, a.repo, ctx)},
		{Role: "user", Content: task.Description},
	}

	for i := 0; i < 25; i++ {
		// Проверяем лимиты перед вызовом
		if a.bot != nil && a.bot.limits != nil {
			// Оцениваем ожидаемое использование (примерно 1000 токенов на запрос)
			estimatedTokens := 1000
			ok, warning := a.bot.limits.CheckLimit(estimatedTokens)
			if !ok {
				task.Steps <- Step{Type: StepError, Content: warning}
				if a.progress != nil {
					a.progress.Error(warning)
				}
				return
			}
			if warning != "" {
				// Показываем предупреждение но продолжаем
				task.Steps <- Step{Type: StepThought, Content: warning}
			}
		}

		resp, err := a.llm(messages)
		if err != nil {
			task.Steps <- Step{Type: StepError, Content: "LLM: " + err.Error()}
			return
		}
		messages = append(messages, msg{Role: "assistant", Content: resp})

		// Передаем usage в progress tracker и обновляем лимиты
		if a.progress != nil {
			a.progress.AddTokenUsage(lastUsage.Input, lastUsage.Output, lastUsage.CacheRead, lastUsage.CacheWrite)
		}
		if a.bot != nil && a.bot.limits != nil {
			totalTokens := lastUsage.Input + lastUsage.Output
			a.bot.limits.CheckLimit(totalTokens)
		}

		if t := extractThought(resp); t != "" {
			a.think(t, task)
		}

		actions := parseActions(resp)
		if len(actions) == 0 {
			task.Steps <- Step{Type: StepDone, Content: resp}
			if a.progress != nil {
				a.progress.Finish()
			}
			return
		}

		for _, act := range actions {
			result, prNum, prURL, done, errMsg := a.execute(act, branch, task)
			if errMsg != "" {
				result = "error: " + errMsg
				if a.progress != nil {
					a.progress.DoneStep(true)
				}
			} else if a.progress != nil {
				a.progress.DoneStep(false)
			}
			if prNum > 0 {
				task.Steps <- Step{Type: StepPR, PRNumber: prNum, PRURL: prURL}
				if a.progress != nil {
					a.progress.SetPR(prNum, prURL)
				}
				return
			}
			if done {
				task.Steps <- Step{Type: StepDone, Content: result}
				if a.progress != nil {
					a.progress.Finish()
				}
				return
			}
			messages = append(messages, msg{Role: "user", Content: "Tool result:\n" + result})
		}
	}
	task.Steps <- Step{Type: StepError, Content: "Превышен лимит шагов"}
}

func (a *Agent) think(content string, task *Task) {
	task.Steps <- Step{Type: StepThought, Content: content}
}

func (a *Agent) act(label string, task *Task) {
	task.Steps <- Step{Type: StepAction, Content: label}
}

func (a *Agent) execute(act action, branch string, task *Task) (result string, prNum int, prURL string, done bool, errMsg string) {
	// Сообщаем трекеру о старте шага
	arg := shortArgs(act.Args)
	if a.progress != nil {
		a.progress.StartStep(act.Tool, arg)
	}
	task.Steps <- Step{Type: StepAction, Content: fmt.Sprintf("%s(%s)", act.Tool, truncateS(arg, 40))}

	switch act.Tool {
	case "read_file":
		content, err := a.gh.GetContent(act.Args["path"], "main")
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		if len(content) > 8000 {
			content = content[:8000] + "\n...[truncated]"
		}
		return content, 0, "", false, ""

	case "write_file":
		err := a.gh.WriteFile(branch, act.Args["path"], act.Args["content"], act.Args["message"])
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return "wrote " + act.Args["path"], 0, "", false, ""

	case "list_files":
		files, err := a.gh.ListDir(act.Args["path"], "main")
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return strings.Join(files, "\n"), 0, "", false, ""

	case "search_code":
		results, err := a.gh.SearchCode(act.Args["query"])
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return results, 0, "", false, ""

	case "search_history":
		if a.bot == nil || a.bot.history == nil {
			return "", 0, "", false, "История сообщений недоступна"
		}
		query := act.Args["query"]
		threadIDStr := act.Args["thread_id"]
		limit := 10

		var results []HistoryMessage
		if threadIDStr != "" {
			threadID, _ := strconv.Atoi(threadIDStr)
			results = a.bot.history.SearchInThread(threadID, query, limit)
		} else {
			results = a.bot.history.Search(query, limit)
		}

		return FormatSearchResults(results), 0, "", false, ""

	case "get_summary":
		if a.bot == nil || a.bot.history == nil {
			return "", 0, "", false, "История сообщений недоступна"
		}
		threadIDStr := act.Args["thread_id"]
		count := 20
		if countStr := act.Args["count"]; countStr != "" {
			if c, err := strconv.Atoi(countStr); err == nil && c > 0 {
				count = c
			}
		}

		var summary string
		if threadIDStr != "" {
			threadID, _ := strconv.Atoi(threadIDStr)
			summary = a.bot.history.GetThreadSummary(threadID, count)
		} else {
			// Саммари всех последних сообщений
			recent := a.bot.history.GetRecentMessages(count)
			summary = FormatSearchResults(recent)
		}

		return summary, 0, "", false, ""

	case "spawn_subagent":
		subTask := &Task{
			ID: task.ID + "-sub", Description: act.Args["task"],
			Owner: a.owner, Repo: a.repo, Branch: branch,
			Steps: make(chan Step, 50),
		}
		sub := NewAgent(a.cfg, a.owner, a.repo).WithBot(a.bot, a.threadID)
		if a.bot != nil {
			subPT := NewProgressTracker(a.bot, act.Args["task"], a.owner, a.repo, a.threadID)
			sub = sub.WithProgress(subPT)
		}
		go sub.Run(subTask)
		var sb strings.Builder
		for step := range subTask.Steps {
			task.Steps <- step
			if step.Type == StepDone {
				sb.WriteString(step.Content)
			}
		}
		return "subagent: " + sb.String(), 0, "", false, ""

	case "orchestrate":
		taskLines := strings.Split(strings.TrimSpace(act.Args["tasks"]), "\n")
		type orchResult struct {
			idx    int
			output string
			errMsg string
		}
		results := make(chan orchResult, len(taskLines))
		count := 0
		for i, t := range taskLines {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			count++
			go func(idx int, taskDesc string) {
				subTask := &Task{
					ID:          fmt.Sprintf("%s-orch-%d", task.ID, idx),
					Description: taskDesc,
					Owner:       a.owner, Repo: a.repo, Branch: branch,
					Steps: make(chan Step, 50),
				}
				sub := NewAgent(a.cfg, a.owner, a.repo).WithBot(a.bot, a.threadID)
				if a.bot != nil {
					subPT := NewProgressTracker(a.bot, taskDesc, a.owner, a.repo, a.threadID)
					sub = sub.WithProgress(subPT)
				}
				go sub.Run(subTask)
				var sb strings.Builder
				for step := range subTask.Steps {
					task.Steps <- step
					if step.Type == StepDone {
						sb.WriteString(step.Content)
					} else if step.Type == StepError {
						results <- orchResult{idx: idx, errMsg: step.Content}
						return
					}
				}
				results <- orchResult{idx: idx, output: sb.String()}
			}(i, t)
		}
		var parts []string
		for i := 0; i < count; i++ {
			r := <-results
			if r.errMsg != "" {
				parts = append(parts, fmt.Sprintf("agent %d error: %s", r.idx, r.errMsg))
			} else {
				parts = append(parts, fmt.Sprintf("agent %d: %s", r.idx, r.output))
			}
		}
		return strings.Join(parts, "\n"), 0, "", false, ""

	case "create_pr":
		if a.bot != nil {
			msg := fmt.Sprintf("🔀 Создать PR?\n*%s*", act.Args["title"])
			if !a.bot.requestApproval(task.ID, msg, a.threadID) {
				return "PR создание отменено пользователем", 0, "", false, ""
			}
		}
		num, url, err := a.gh.CreatePR(branch, act.Args["title"], act.Args["body"])
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return fmt.Sprintf("PR #%d: %s", num, url), num, url, false, ""

	case "done":
		return act.Args["summary"], 0, "", true, ""
	}

	return "", 0, "", false, "unknown tool: " + act.Tool
}

func (a *Agent) buildContext(task string) string {
	var parts []string
	for _, f := range []string{"CLAUDE.md", "AGENT.md", "README.md"} {
		if c, err := a.gh.GetContent(f, "main"); err == nil {
			if len(c) > 3000 {
				c = c[:3000] + "..."
			}
			parts = append(parts, "## "+f+"\n"+c)
			break
		}
	}
	for _, dir := range []string{"src", ".", "lib", "app"} {
		if files, err := a.gh.ListDir(dir, "main"); err == nil && len(files) > 0 {
			parts = append(parts, fmt.Sprintf("## %s/\n%s", dir, strings.Join(files, "\n")))
			break
		}
	}
	for _, f := range []string{"package.json", "go.mod", "pyproject.toml"} {
		if c, err := a.gh.GetContent(f, "main"); err == nil {
			if len(c) > 800 {
				c = c[:800]
			}
			parts = append(parts, "## "+f+"\n"+c)
			break
		}
	}
	_ = task
	return strings.Join(parts, "\n\n")
}

// ── LLM ──────────────────────────────────────────────────────

type msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (a *Agent) llm(messages []msg) (string, error) {
	if a.cfg.LLMProvider == "claude" {
		return callClaude(a.cfg.AnthropicKey, messages)
	}
	return callDeepSeek(a.cfg.DeepSeekKey, messages)
}

func callDeepSeek(key string, messages []msg) (string, error) {
	if key == "" {
		return "", fmt.Errorf("❌ DEEPSEEK_API_KEY не найден\n\n👉 Добавь в Railway Variables или .env:\nDEEPSEEK_API_KEY=sk-...\n\nПолучить ключ: platform.deepseek.com")
	}
	body, _ := json.Marshal(map[string]any{
		"model": "deepseek-chat", "max_tokens": 8192, "temperature": 0.1,
		"messages": messages,
	})
	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return parseOpenAI(resp.Body)
}

func callClaude(key string, messages []msg) (string, error) {
	if key == "" {
		return "", fmt.Errorf("❌ ANTHROPIC_API_KEY не найден\n\n👉 Добавь в Railway Variables или .env:\nANTHROPIC_API_KEY=sk-ant-...\n\nПолучить ключ: console.anthropic.com")
	}
	var system string
	var rest []msg
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			rest = append(rest, m)
		}
	}
	// system как массив с cache_control — кэширует большой system prompt между шагами ReAct цикла
	systemBlocks := []map[string]any{
		{"type": "text", "text": system, "cache_control": map[string]string{"type": "ephemeral"}},
	}
	body, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-20250514", "max_tokens": 8192,
		"system": systemBlocks, "messages": rest,
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return parseAnthropic(resp.Body)
}

func parseOpenAI(r io.Reader) (string, error) {
	var result struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("API: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty")
	}
	return result.Choices[0].Message.Content, nil
}

// lastUsage - глобальная переменная для хранения последнего usage (временное решение)
var lastUsage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
}

func parseAnthropic(r io.Reader) (string, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheCreationTokens  int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("API: %s", result.Error.Message)
	}

	// Сохраняем usage для передачи в progress tracker
	if result.Usage != nil {
		lastUsage.Input = result.Usage.InputTokens
		lastUsage.Output = result.Usage.OutputTokens
		lastUsage.CacheRead = result.Usage.CacheReadInputTokens
		lastUsage.CacheWrite = result.Usage.CacheCreationTokens
	}

	for _, c := range result.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text")
}

// ── Parsing ───────────────────────────────────────────────────

type action struct {
	Tool string
	Args map[string]string
}

var actionRe = regexp.MustCompile(`(?s)<action>(.*?)</action>`)
var toolRe = regexp.MustCompile(`tool:\s*"?(\w+)"?`)
var argRe = regexp.MustCompile(`(?s)(\w+):\s*"((?:[^"\\]|\\.)*)"`)

func parseActions(response string) []action {
	var actions []action
	for _, block := range actionRe.FindAllStringSubmatch(response, -1) {
		body := block[1]
		tm := toolRe.FindStringSubmatch(body)
		if len(tm) < 2 {
			continue
		}
		act := action{Tool: tm[1], Args: make(map[string]string)}
		for _, am := range argRe.FindAllStringSubmatch(body, -1) {
			if am[1] != "tool" {
				val := strings.ReplaceAll(am[2], `\n`, "\n")
				val = strings.ReplaceAll(val, `\"`, `"`)
				act.Args[am[1]] = val
			}
		}
		actions = append(actions, act)
	}
	return actions
}

func extractThought(r string) string {
	for _, line := range strings.Split(r, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Thought:") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Thought:"))
		}
	}
	return ""
}

func makeBranch(desc, id string) string {
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug := re.ReplaceAllString(strings.ToLower(desc), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = id
	}
	return "feat/" + slug
}

func shortArgs(args map[string]string) string {
	for _, k := range []string{"path", "title", "task", "query"} {
		if v := args[k]; v != "" {
			return truncateS(v, 40)
		}
	}
	return ""
}

func truncateS(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func systemPrompt(owner, repo, ctx string) string {
	return fmt.Sprintf(`You are an AI coding agent for %s/%s.

## Project Context
%s

## Tools

<action>
tool: "read_file"
path: "src/components/Example.jsx"
</action>

<action>
tool: "write_file"
path: "src/components/New.jsx"
content: "import React from 'react';\nexport default function New() { return <div/>; }"
message: "feat: add New component"
</action>

<action>
tool: "list_files"
path: "src"
</action>

<action>
tool: "search_code"
query: "useUserData"
</action>

<action>
tool: "search_history"
query: "bug fix"
thread_id: "123"
</action>

<action>
tool: "get_summary"
thread_id: "123"
count: "20"
</action>

<action>
tool: "spawn_subagent"
task: "Write Playwright test for TrackingScreen"
</action>

<action>
tool: "orchestrate"
tasks: "Write unit tests for AuthService\nWrite integration test for login flow\nUpdate API docs"
</action>

<action>
tool: "create_pr"
title: "feat: add tracking screen"
body: "## Changes\n- TrackingScreen.jsx\n- useTracking.ts\n- e2e test"
</action>

<action>
tool: "done"
summary: "Analysis complete"
</action>

## Rules
1. Read files before writing — check existing patterns
2. Every new component → Playwright test in tests/e2e/
3. Every Supabase hook → integration test in tests/integration/
4. Follow existing code style
5. After all files written → create_pr
6. Write Thought: before each action`, owner, repo, ctx)
}
