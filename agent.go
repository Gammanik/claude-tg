package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type StepType string

const (
	StepThought StepType = "thought"
	StepAction  StepType = "action"
	StepResult  StepType = "result"
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
	llm         *LLMClient
	gh          *GitHubClient
	owner, repo string
	bot         *Bot
	threadID    int
}

func NewAgent(cfg Config, llm *LLMClient, owner, repo string) *Agent {
	return &Agent{
		cfg:   cfg,
		llm:   llm,
		gh:    NewGitHubClient(cfg.GitHubToken, owner, repo),
		owner: owner,
		repo:  repo,
	}
}

func (a *Agent) WithBot(bot *Bot, threadID int) *Agent {
	a.bot = bot
	a.threadID = threadID
	return a
}

func (a *Agent) Run(task *Task) {
	defer close(task.Steps)
	defer func() {
		if r := recover(); r != nil {
			task.Steps <- Step{Type: StepError, Content: fmt.Sprintf("panic: %v", r)}
		}
	}()

	// Создаём ветку
	branch := makeBranch(task.Description, task.ID)
	task.Steps <- Step{Type: StepThought, Content: "Создаю ветку " + branch}

	if err := a.gh.CreateBranch(branch); err != nil {
		task.Steps <- Step{Type: StepError, Content: "CreateBranch: " + err.Error()}
		return
	}
	task.Branch = branch

	// Читаем контекст репо
	task.Steps <- Step{Type: StepThought, Content: "Читаю структуру репо..."}
	ctx := a.buildContext()

	// ReAct loop с Opus (для coding задач)
	messages := []msg{
		{Role: "system", Content: systemPrompt(a.owner, a.repo, ctx)},
		{Role: "user", Content: task.Description},
	}

	for i := 0; i < 25; i++ {
		resp, err := a.llm.Call(TierOpus, messages[0].Content, messages[len(messages)-1].Content)
		if err != nil {
			task.Steps <- Step{Type: StepError, Content: "LLM: " + err.Error()}
			return
		}
		messages = append(messages, msg{Role: "assistant", Content: resp})

		// Извлекаем мысль
		if t := extractThought(resp); t != "" {
			task.Steps <- Step{Type: StepThought, Content: t}
		}

		// Парсим actions
		actions := parseActions(resp)
		if len(actions) == 0 {
			task.Steps <- Step{Type: StepDone, Content: resp}
			return
		}

		// Выполняем actions
		results, prNum, prURL, done := a.executeActions(actions, branch, task)

		if prNum > 0 {
			task.Steps <- Step{Type: StepPR, PRNumber: prNum, PRURL: prURL}
			return
		}
		if done {
			task.Steps <- Step{Type: StepDone, Content: results}
			return
		}

		// Добавляем результаты в контекст
		messages = append(messages, msg{Role: "user", Content: "Tool results:\n" + results})
	}

	task.Steps <- Step{Type: StepError, Content: "Превышен лимит шагов (25)"}
}

func (a *Agent) executeActions(actions []action, branch string, task *Task) (results string, prNum int, prURL string, done bool) {
	var sb strings.Builder

	for _, act := range actions {
		task.Steps <- Step{Type: StepAction, Content: fmt.Sprintf("%s(%s)", act.Tool, shortArgs(act.Args))}

		result, pNum, pURL, isDone, errMsg := a.execute(act, branch)

		if pNum > 0 {
			return result, pNum, pURL, false
		}
		if isDone {
			return result, 0, "", true
		}
		if errMsg != "" {
			sb.WriteString(fmt.Sprintf("[%s]: ERROR: %s\n", act.Tool, errMsg))
			task.Steps <- Step{Type: StepResult, Content: "ERROR: " + errMsg}
		} else {
			sb.WriteString(fmt.Sprintf("[%s]: %s\n", act.Tool, truncate(result, 200)))
			task.Steps <- Step{Type: StepResult, Content: truncate(result, 300)}
		}
	}

	return sb.String(), 0, "", false
}

func (a *Agent) execute(act action, branch string) (result string, prNum int, prURL string, done bool, errMsg string) {
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
		content := act.Args["content"]
		message := act.Args["message"]
		if message == "" {
			message = "update " + act.Args["path"]
		}
		err := a.gh.WriteFile(branch, act.Args["path"], content, message)
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

	case "create_pr":
		title := act.Args["title"]
		body := act.Args["body"]

		// Запрашиваем подтверждение через бота
		if a.bot != nil {
			msg := fmt.Sprintf("🔀 Создать PR?\n*%s*", title)
			if !a.bot.requestApproval(fmt.Sprintf("pr-%d", time.Now().Unix()), msg, a.threadID) {
				return "PR создание отменено пользователем", 0, "", false, ""
			}
		}

		num, url, err := a.gh.CreatePR(branch, title, body)
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		return fmt.Sprintf("PR #%d: %s", num, url), num, url, false, ""

	case "done":
		return act.Args["summary"], 0, "", true, ""
	}

	return "", 0, "", false, "unknown tool: " + act.Tool
}

func (a *Agent) buildContext() string {
	var parts []string

	// Ищем документацию
	for _, f := range []string{"README.md", "CLAUDE.md", "AGENT.md"} {
		if c, err := a.gh.GetContent(f, "main"); err == nil {
			if len(c) > 3000 {
				c = c[:3000] + "..."
			}
			parts = append(parts, "## "+f+"\n"+c)
			break
		}
	}

	// Листинг директории
	for _, dir := range []string{".", "src", "lib", "app"} {
		if files, err := a.gh.ListDir(dir, "main"); err == nil && len(files) > 0 {
			parts = append(parts, fmt.Sprintf("## %s/\n%s", dir, strings.Join(files, "\n")))
			break
		}
	}

	// Манифест
	for _, f := range []string{"package.json", "go.mod", "pyproject.toml", "Cargo.toml"} {
		if c, err := a.gh.GetContent(f, "main"); err == nil {
			if len(c) > 800 {
				c = c[:800]
			}
			parts = append(parts, "## "+f+"\n"+c)
			break
		}
	}

	return strings.Join(parts, "\n\n")
}

// ── Parsing ───────────────────────────────────────────────────

type msg struct {
	Role    string
	Content string
}

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
	for _, k := range []string{"path", "title", "query"} {
		if v := args[k]; v != "" {
			return truncate(v, 40)
		}
	}
	return ""
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
tool: "create_pr"
title: "feat: add tracking screen"
body: "## Changes\n- TrackingScreen.jsx\n- useTracking.ts"
</action>

<action>
tool: "done"
summary: "Analysis complete"
</action>

## Rules
1. Read files before writing - understand existing patterns
2. Follow existing code style
3. After all files written → create_pr
4. Write "Thought:" before each action to explain your reasoning
5. Keep changes focused - don't over-engineer

Example:
Thought: I need to understand the current auth implementation
<action>
tool: "read_file"
path: "src/auth/AuthService.ts"
</action>

Thought: Now I'll add the new logout feature
<action>
tool: "write_file"
path: "src/auth/AuthService.ts"
content: "..."
message: "feat: add logout functionality"
</action>`, owner, repo, ctx)
}
