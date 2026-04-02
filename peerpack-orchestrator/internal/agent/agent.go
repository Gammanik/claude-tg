package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gammanik/peerpack-bot/internal/github"
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
	Type    StepType
	Content string
	PRNumber int
	PRURL   string
}

type Task struct {
	ID     string
	Branch string
	Steps  chan Step
}

type Config struct {
	Provider     string
	DeepSeekKey  string
	AnthropicKey string
	GitHubClient *github.Client
}

type Agent struct {
	cfg   Config
	tasks map[string]*Task
}

func NewAgent(cfg Config) *Agent {
	return &Agent{cfg: cfg, tasks: make(map[string]*Task)}
}

// StartTask запускает агента для задачи — возвращает Task с каналом шагов
func (a *Agent) StartTask(description string) (*Task, error) {
	taskID := fmt.Sprintf("task-%d", time.Now().Unix())
	branch := slugify(description, taskID)

	task := &Task{
		ID:     taskID,
		Branch: branch,
		Steps:  make(chan Step, 50),
	}
	a.tasks[taskID] = task

	go a.run(task, description)
	return task, nil
}

// FixFailedTests — агент читает лог ошибок и пушит фикс в тот же branch
func (a *Agent) FixFailedTests(taskID, failLog string) {
	task, ok := a.tasks[taskID]
	if !ok {
		return
	}
	go a.runFix(task, failLog)
}

func (a *Agent) run(task *Task, description string) {
	defer close(task.Steps)

	// 1. Читаем контекст проекта
	task.Steps <- Step{Type: StepThought, Content: "Читаю CLAUDE.md и структуру проекта..."}
	ctx, err := a.buildContext(description)
	if err != nil {
		task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("не смог прочитать репо: %v", err)}
		return
	}

	// 2. Создаём ветку
	if err := a.cfg.GitHubClient.CreateBranch(task.Branch); err != nil {
		task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("не смог создать ветку: %v", err)}
		return
	}
	task.Steps <- Step{Type: StepAction, Content: fmt.Sprintf("git checkout -b %s", task.Branch)}

	// 3. ReAct loop
	messages := []llmMessage{
		{Role: "system", Content: systemPrompt(ctx)},
		{Role: "user", Content: description},
	}

	maxSteps := 15
	for i := 0; i < maxSteps; i++ {
		response, err := a.callLLM(messages)
		if err != nil {
			task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("LLM error: %v", err)}
			return
		}

		messages = append(messages, llmMessage{Role: "assistant", Content: response})

		// Парсим действия из ответа
		actions := parseActions(response)
		if len(actions) == 0 {
			// Нет действий — агент думает
			thought := extractThought(response)
			if thought != "" {
				task.Steps <- Step{Type: StepThought, Content: thought}
			}
		}

		allDone := false
		for _, action := range actions {
			result, done, prNum, prURL := a.executeAction(action, task.Branch, task.Steps)
			if prNum > 0 {
				task.Steps <- Step{Type: StepPR, PRNumber: prNum, PRURL: prURL}
				return // дальше bot.go будет ждать CI
			}
			if done {
				allDone = true
				break
			}
			// Добавляем результат в контекст
			messages = append(messages, llmMessage{
				Role:    "user",
				Content: fmt.Sprintf("Tool result:\n%s", result),
			})
		}

		if allDone {
			break
		}
	}
}

func (a *Agent) runFix(task *Task, failLog string) {
	messages := []llmMessage{
		{Role: "system", Content: "You are fixing failing tests. Analyze the error log and fix the code."},
		{Role: "user", Content: fmt.Sprintf("Tests failed with this log:\n```\n%s\n```\nFix the issue.", failLog)},
	}

	response, err := a.callLLM(messages)
	if err != nil {
		task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("LLM fix error: %v", err)}
		return
	}

	actions := parseActions(response)
	for _, action := range actions {
		a.executeAction(action, task.Branch, task.Steps)
	}

	task.Steps <- Step{Type: StepDone, Content: "Фикс запушен, жду CI..."}
}

// executeAction выполняет одно действие агента
func (a *Agent) executeAction(action toolCall, branch string, steps chan Step) (result string, done bool, prNum int, prURL string) {
	steps <- Step{Type: StepAction, Content: fmt.Sprintf("%s(%s)", action.Tool, summarizeArgs(action.Args))}

	switch action.Tool {
	case "read_file":
		content, err := a.cfg.GitHubClient.GetFileContent(action.Args["path"], "main")
		if err != nil {
			return fmt.Sprintf("error: %v", err), false, 0, ""
		}
		// Обрезаем большие файлы
		if len(content) > 6000 {
			content = content[:6000] + "\n... [truncated]"
		}
		return content, false, 0, ""

	case "write_file":
		path := action.Args["path"]
		content := action.Args["content"]
		msg := action.Args["message"]
		if msg == "" {
			msg = fmt.Sprintf("feat: update %s", path)
		}
		if err := a.cfg.GitHubClient.WriteFile(branch, path, content, msg); err != nil {
			return fmt.Sprintf("error writing %s: %v", path, err), false, 0, ""
		}
		return fmt.Sprintf("wrote %s successfully", path), false, 0, ""

	case "list_files":
		dirPath := action.Args["path"]
		files, err := a.cfg.GitHubClient.ListFiles(dirPath, "main")
		if err != nil {
			return fmt.Sprintf("error: %v", err), false, 0, ""
		}
		return strings.Join(files, "\n"), false, 0, ""

	case "create_pr":
		title := action.Args["title"]
		body := action.Args["body"]
		if title == "" {
			title = "feat: " + branch
		}
		num, url, err := a.cfg.GitHubClient.CreatePR(branch, title, body)
		if err != nil {
			return fmt.Sprintf("error creating PR: %v", err), false, 0, ""
		}
		return fmt.Sprintf("PR #%d created: %s", num, url), false, num, url

	case "done":
		return action.Args["summary"], true, 0, ""
	}

	return fmt.Sprintf("unknown tool: %s", action.Tool), false, 0, ""
}

// buildContext читает ключевые файлы из репо для контекста агента
func (a *Agent) buildContext(task string) (string, error) {
	gh := a.cfg.GitHubClient

	claudeMD, _ := gh.GetFileContent("CLAUDE.md", "main")
	packageJSON, _ := gh.GetFileContent("package.json", "main")
	srcFiles, _ := gh.ListFiles("src", "main")

	return fmt.Sprintf(`## CLAUDE.md (project rules)
%s

## package.json
%s

## src/ structure
%s

## Current task
%s`, claudeMD, packageJSON, strings.Join(srcFiles, "\n"), task), nil
}

// --- LLM ---

type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (a *Agent) callLLM(messages []llmMessage) (string, error) {
	switch a.cfg.Provider {
	case "claude":
		return a.callClaude(messages)
	default:
		return a.callDeepSeek(messages)
	}
}

func (a *Agent) callDeepSeek(messages []llmMessage) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       "deepseek-chat",
		"messages":    messages,
		"max_tokens":  4096,
		"temperature": 0.1,
	})

	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a.cfg.DeepSeekKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return extractOpenAIContent(resp.Body)
}

func (a *Agent) callClaude(messages []llmMessage) (string, error) {
	// Отделяем system от остальных
	var system string
	var rest []llmMessage
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			rest = append(rest, m)
		}
	}

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 4096,
		"system":     system,
		"messages":   rest,
	})

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", a.cfg.AnthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return extractAnthropicContent(resp.Body)
}

func extractOpenAIContent(r io.Reader) (string, error) {
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
		return "", fmt.Errorf("API error: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty response")
	}
	return result.Choices[0].Message.Content, nil
}

func extractAnthropicContent(r io.Reader) (string, error) {
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
		return "", fmt.Errorf("API error: %s", result.Error.Message)
	}
	for _, c := range result.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text in response")
}

// --- Parsing ReAct format ---

type toolCall struct {
	Tool string
	Args map[string]string
}

var actionRe = regexp.MustCompile(`(?s)<action>\s*tool:\s*(\w+)\s*(.*?)</action>`)
var argRe = regexp.MustCompile(`(\w+):\s*"((?:[^"\\]|\\.)*)"`)

func parseActions(response string) []toolCall {
	var calls []toolCall
	matches := actionRe.FindAllStringSubmatch(response, -1)
	for _, m := range matches {
		call := toolCall{Tool: m[1], Args: make(map[string]string)}
		for _, am := range argRe.FindAllStringSubmatch(m[2], -1) {
			call.Args[am[1]] = am[2]
		}
		calls = append(calls, call)
	}
	return calls
}

func extractThought(response string) string {
	lines := strings.Split(response, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Thought:") || strings.HasPrefix(l, "💭") {
			return strings.TrimPrefix(strings.TrimPrefix(l, "Thought:"), "💭")
		}
	}
	if len(response) > 200 {
		return response[:200] + "..."
	}
	return response
}

func slugify(desc, fallback string) string {
	re := regexp.MustCompile(`[^a-zA-Zа-яА-Я0-9\s]`)
	clean := re.ReplaceAllString(desc, "")
	words := strings.Fields(clean)
	if len(words) > 4 {
		words = words[:4]
	}
	slug := strings.ToLower(strings.Join(words, "-"))
	// транслитерация базовая
	replacer := strings.NewReplacer(
		"а","a","б","b","в","v","г","g","д","d","е","e","ё","yo",
		"ж","zh","з","z","и","i","й","y","к","k","л","l","м","m",
		"н","n","о","o","п","p","р","r","с","s","т","t","у","u",
		"ф","f","х","h","ц","ts","ч","ch","ш","sh","щ","sch",
		"ъ","","ы","y","ь","","э","e","ю","yu","я","ya",
	)
	slug = replacer.Replace(slug)
	if slug == "" {
		return "feat/" + fallback
	}
	return "feat/" + slug
}

func summarizeArgs(args map[string]string) string {
	path := args["path"]
	if path != "" {
		return path
	}
	title := args["title"]
	if title != "" {
		return title
	}
	return "..."
}

func systemPrompt(ctx string) string {
	return fmt.Sprintf(`You are an AI coding agent for PeerPack — a Telegram Mini App (React + Vite + Supabase).

## Project Context
%s

## Your Tools
Use XML tags to call tools:

<action>
tool: read_file
path: "src/components/SomeFile.jsx"
</action>

<action>
tool: write_file
path: "src/components/NewFile.jsx"
content: "import React from 'react';\n..."
message: "feat: add new component"
</action>

<action>
tool: list_files
path: "src/components"
</action>

<action>
tool: create_pr
title: "feat: add tracking screen"
body: "## Changes\n- Added TrackingScreen component\n- Added useTracking hook\n- Added Playwright test"
</action>

<action>
tool: done
summary: "Completed the task"
</action>

## Rules
1. ALWAYS read relevant files before writing anything
2. Follow existing code patterns — check similar files first
3. Every new screen/component MUST have a Playwright test in tests/e2e/
4. Every Supabase hook MUST have an integration test in tests/integration/
5. Keep Telegram Mini App patterns (use var(--tg-theme-*) CSS vars)
6. Use snake_case for API properties (as per CLAUDE.md)
7. After writing all files, call create_pr to open a PR
8. PR body should list all changed/added files

## Thought format
Before each action, write:
Thought: [what you're planning to do and why]`, ctx)
}

var _ = log.Printf // suppress unused import
