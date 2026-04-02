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

// ── Task ──────────────────────────────────────────────────────

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
	Owner       string
	Repo        string
	Branch      string
	Steps       chan Step
}

// ── Agent ─────────────────────────────────────────────────────

type Agent struct {
	cfg   Config
	gh    *GitHubClient
	owner string
	repo  string
}

func NewAgent(cfg Config, owner, repo string) *Agent {
	return &Agent{
		cfg:   cfg,
		gh:    NewGitHubClient(cfg.GitHubToken, owner, repo),
		owner: owner,
		repo:  repo,
	}
}

func (a *Agent) Run(task *Task) {
	defer func() {
		if r := recover(); r != nil {
			task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("panic: %v", r)}
			close(task.Steps)
		}
	}()
	defer close(task.Steps)

	// 1. Читаем контекст
	task.Steps <- Step{Type: StepThought, Content: "Читаю структуру репо..."}
	ctx := a.buildContext(task.Description)

	// 2. Создаём ветку (если не передана)
	if task.Branch == "" {
		task.Branch = makeBranch(task.Description, task.ID)
		if err := a.gh.CreateBranch(task.Branch); err != nil {
			task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("CreateBranch: %v", err)}
			return
		}
	}
	task.Steps <- Step{Type: StepAction, Content: fmt.Sprintf("git checkout -b %s", task.Branch)}

	// 3. ReAct loop
	messages := []msg{
		{Role: "system", Content: systemPrompt(a.owner, a.repo, ctx)},
		{Role: "user", Content: task.Description},
	}

	for i := 0; i < 20; i++ {
		resp, err := a.llm(messages)
		if err != nil {
			task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("LLM: %v", err)}
			return
		}
		messages = append(messages, msg{Role: "assistant", Content: resp})

		thought := extractThought(resp)
		if thought != "" {
			task.Steps <- Step{Type: StepThought, Content: thought}
		}

		actions := parseActions(resp)
		if len(actions) == 0 {
			// Нет action-тегов — агент, видимо, закончил без create_pr
			task.Steps <- Step{Type: StepDone, Content: resp}
			return
		}

		stop := false
		for _, act := range actions {
			result, prNum, prURL, done, err_ := a.execute(act, task.Branch, task.Steps)
			if err_ != "" {
				result = "error: " + err_
			}
			if prNum > 0 {
				task.Steps <- Step{Type: StepPR, PRNumber: prNum, PRURL: prURL}
				return
			}
			if done {
				task.Steps <- Step{Type: StepDone, Content: result}
				return
			}
			messages = append(messages, msg{Role: "user", Content: "Tool result:\n" + result})
			if stop {
				break
			}
		}
	}

	task.Steps <- Step{Type: StepError, Content: "Превышен лимит шагов (20)"}
}

// execute выполняет один tool call
func (a *Agent) execute(act action, branch string, steps chan Step) (result string, prNum int, prURL string, done bool, errMsg string) {
	steps <- Step{Type: StepAction, Content: fmt.Sprintf("%s(%s)", act.Tool, shortArgs(act.Args))}

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
		return fmt.Sprintf("wrote %s", act.Args["path"]), 0, "", false, ""

	case "list_files":
		files, err := a.gh.ListDir(act.Args["path"], "main")
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return strings.Join(files, "\n"), 0, "", false, ""

	case "search_code":
		// Ищем по файлам в репо (простой grep через GitHub search API)
		results, err := a.gh.SearchCode(act.Args["query"])
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return results, 0, "", false, ""

	case "spawn_subagent":
		// Запускаем суб-агента для подзадачи
		subTask := &Task{
			ID:          fmt.Sprintf("sub-%d", time.Now().UnixNano()),
			Description: act.Args["task"],
			Owner:       a.owner,
			Repo:        a.repo,
			Branch:      branch, // тот же branch
			Steps:       make(chan Step, 50),
		}
		subAgent := NewAgent(a.cfg, a.owner, a.repo)
		go subAgent.Run(subTask)

		// Собираем результат суб-агента
		var sb strings.Builder
		for step := range subTask.Steps {
			steps <- step // проксируем шаги наверх
			if step.Type == StepDone {
				sb.WriteString(step.Content)
			}
		}
		return "subagent result: " + sb.String(), 0, "", false, ""

	case "create_pr":
		num, url, err := a.gh.CreatePR(branch, act.Args["title"], act.Args["body"])
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return fmt.Sprintf("PR #%d: %s", num, url), num, url, false, ""

	case "done":
		return act.Args["summary"], 0, "", true, ""
	}

	return "", 0, "", false, fmt.Sprintf("unknown tool: %s", act.Tool)
}

// buildContext читает ключевые файлы из репо
func (a *Agent) buildContext(task string) string {
	var parts []string

	// CLAUDE.md или README.md
	for _, f := range []string{"CLAUDE.md", "AGENT.md", "README.md"} {
		if content, err := a.gh.GetContent(f, "main"); err == nil {
			if len(content) > 3000 {
				content = content[:3000] + "...[truncated]"
			}
			parts = append(parts, fmt.Sprintf("## %s\n%s", f, content))
			break
		}
	}

	// Структура src/
	for _, dir := range []string{"src", ".", "lib", "app"} {
		if files, err := a.gh.ListDir(dir, "main"); err == nil && len(files) > 0 {
			parts = append(parts, fmt.Sprintf("## %s/ structure\n%s", dir, strings.Join(files, "\n")))
			break
		}
	}

	// package.json или go.mod
	for _, f := range []string{"package.json", "go.mod", "pyproject.toml", "Cargo.toml"} {
		if content, err := a.gh.GetContent(f, "main"); err == nil {
			if len(content) > 1000 {
				content = content[:1000]
			}
			parts = append(parts, fmt.Sprintf("## %s\n%s", f, content))
			break
		}
	}

	return strings.Join(parts, "\n\n")
}

// ── LLM ───────────────────────────────────────────────────────

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
	body, _ := json.Marshal(map[string]any{
		"model":       "deepseek-chat",
		"messages":    messages,
		"max_tokens":  8192,
		"temperature": 0.1,
	})
	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return parseOpenAI(resp.Body)
}

func callClaude(key string, messages []msg) (string, error) {
	var system string
	var rest []msg
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			rest = append(rest, m)
		}
	}
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 8192,
		"system":     system,
		"messages":   rest,
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
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
		return "", fmt.Errorf("empty response")
	}
	return result.Choices[0].Message.Content, nil
}

func parseAnthropic(r io.Reader) (string, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("API: %s", result.Error.Message)
	}
	for _, c := range result.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text block")
}

// ── ReAct parsing ─────────────────────────────────────────────

type action struct {
	Tool string
	Args map[string]string
}

var actionBlockRe = regexp.MustCompile(`(?s)<action>(.*?)</action>`)
var toolRe = regexp.MustCompile(`tool:\s*(\w+)`)
var argRe = regexp.MustCompile(`(?s)(\w+):\s*"((?:[^"\\]|\\.)*)"`)

func parseActions(response string) []action {
	var actions []action
	for _, block := range actionBlockRe.FindAllStringSubmatch(response, -1) {
		body := block[1]
		toolMatch := toolRe.FindStringSubmatch(body)
		if len(toolMatch) < 2 {
			continue
		}
		act := action{Tool: toolMatch[1], Args: make(map[string]string)}
		for _, am := range argRe.FindAllStringSubmatch(body, -1) {
			if am[1] != "tool" {
				// Обрабатываем escape sequences
				val := strings.ReplaceAll(am[2], `\n`, "\n")
				val = strings.ReplaceAll(val, `\"`, `"`)
				act.Args[am[1]] = val
			}
		}
		actions = append(actions, act)
	}
	return actions
}

func extractThought(response string) string {
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Thought:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Thought:"))
		}
	}
	return ""
}

// ── Utils ─────────────────────────────────────────────────────

func makeBranch(desc, taskID string) string {
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug := re.ReplaceAllString(strings.ToLower(desc), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = taskID
	}
	return "feat/" + slug
}

func shortArgs(args map[string]string) string {
	if p := args["path"]; p != "" {
		return p
	}
	if t := args["title"]; t != "" {
		return t
	}
	if t := args["task"]; t != "" {
		return truncateS(t, 40)
	}
	return "..."
}

func truncateS(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ── System Prompt ─────────────────────────────────────────────

func systemPrompt(owner, repo, ctx string) string {
	return fmt.Sprintf(`You are an AI coding agent for the GitHub repo %s/%s.

## Project Context
%s

## Tools — use XML <action> tags

Read a file:
<action>
tool: "read_file"
path: "src/components/Example.jsx"
</action>

Write/update a file:
<action>
tool: "write_file"
path: "src/components/NewThing.jsx"
content: "import React from 'react';\n\nexport default function NewThing() {\n  return <div>Hello</div>;\n}"
message: "feat: add NewThing component"
</action>

List directory:
<action>
tool: "list_files"
path: "src/components"
</action>

Search code:
<action>
tool: "search_code"
query: "useUserData"
</action>

Spawn a sub-agent for a subtask (runs in same branch, results come back):
<action>
tool: "spawn_subagent"
task: "Write a Playwright test for the TrackingScreen component"
</action>

Open a PR (call this when all files are written):
<action>
tool: "create_pr"
title: "feat: add tracking screen"
body: "## Changes\n- Added TrackingScreen.jsx\n- Added useTracking.ts\n- Added playwright test"
</action>

Signal completion without PR:
<action>
tool: "done"
summary: "Analysis complete: the bug is in line 42 of api.js"
</action>

## Rules
1. ALWAYS read files before writing — check existing patterns
2. Every new UI component needs a Playwright test in tests/e2e/
3. Every Supabase hook needs an integration test in tests/integration/
4. Follow existing code style exactly
5. After writing all files → call create_pr
6. PR body lists every file changed
7. For complex tasks: use spawn_subagent to parallelize work
8. Write Thought: before each action block`, owner, repo, ctx)
}
