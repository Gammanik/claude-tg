package main

import (
	"fmt"
	"log"
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
	Commits     []string // SHA коммитов
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

	// Определяем ветку для работы
	var branch string
	if a.cfg.DirectCommit {
		// Прямой коммит в main
		branch = "main"
		task.Steps <- Step{Type: StepThought, Content: "Работаю с main (прямой коммит)"}
	} else {
		// Создаём feature-ветку
		branch = makeBranch(task.Description, task.ID)
		task.Steps <- Step{Type: StepThought, Content: "Создаю ветку " + branch}

		if err := a.gh.CreateBranch(branch); err != nil {
			task.Steps <- Step{Type: StepError, Content: "CreateBranch: " + err.Error()}
			return
		}
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
		log.Printf("[Agent] === Iteration %d/%d ===", i+1, 25)
		resp, err := a.llm.Call(TierOpus, messages[0].Content, messages[len(messages)-1].Content)
		if err != nil {
			log.Printf("[Agent] LLM error: %v", err)
			task.Steps <- Step{Type: StepError, Content: "LLM: " + err.Error()}
			return
		}
		log.Printf("[Agent] LLM response length: %d chars", len(resp))

		// Логируем первые 1000 символов ответа для отладки
		if len(resp) > 1000 {
			log.Printf("[Agent] Response preview:\n%s\n...[truncated]", resp[:1000])
		} else {
			log.Printf("[Agent] Full response:\n%s", resp)
		}

		messages = append(messages, msg{Role: "assistant", Content: resp})

		// Извлекаем мысль
		if t := extractThought(resp); t != "" {
			task.Steps <- Step{Type: StepThought, Content: t}
		}

		// Парсим actions
		actions := parseActions(resp)
		log.Printf("[Agent] Parsed %d actions", len(actions))

		// Если не нашли actions, попробуем более мягкий парсинг
		if len(actions) == 0 {
			actions = parseActionsRelaxed(resp)
			log.Printf("[Agent] Relaxed parsing found %d actions", len(actions))
		}

		if len(actions) == 0 {
			// Логируем ответ для отладки
			log.Printf("[Agent] LLM response (first 500 chars): %s", truncate(resp, 500))
			log.Printf("[Agent] No actions found, marking as done")
			task.Steps <- Step{Type: StepDone, Content: resp}
			return
		}

		// Выполняем actions
		log.Printf("[Agent] Executing %d actions...", len(actions))
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

		result, pNum, pURL, isDone, errMsg := a.execute(act, branch, task)

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

func (a *Agent) execute(act action, branch string, task *Task) (result string, prNum int, prURL string, done bool, errMsg string) {
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
		commitSHA, err := a.gh.WriteFile(branch, act.Args["path"], content, message)
		if err != nil {
			return "", 0, "", false, err.Error()
		}
		// Сохраняем commit SHA
		task.Commits = append(task.Commits, commitSHA)
		commitURL := fmt.Sprintf("https://github.com/%s/%s/commit/%s", task.Owner, task.Repo, commitSHA)
		log.Printf("Commit created: %s", commitURL)
		return fmt.Sprintf("wrote %s (commit: %s)", act.Args["path"], commitSHA[:7]), 0, "", false, ""

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

		// Если включен прямой коммит - возвращаем ссылки на коммиты
		if a.cfg.DirectCommit {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("✅ Изменения закоммичены в main\n\n%s\n\n", title))
			if len(task.Commits) > 0 {
				sb.WriteString("🔗 Коммиты:\n")
				for _, sha := range task.Commits {
					commitURL := fmt.Sprintf("https://github.com/%s/%s/commit/%s", task.Owner, task.Repo, sha)
					sb.WriteString(fmt.Sprintf("• %s\n", commitURL))
				}
			}
			return sb.String(), 0, "", true, ""
		}

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

// parseActionsRelaxed - более мягкий парсинг для случаев, когда строгий формат не сработал
func parseActionsRelaxed(response string) []action {
	var actions []action

	// Пробуем найти блоки между <action> и </action> с более гибкими правилами
	relaxedActionRe := regexp.MustCompile(`(?is)<action[^>]*>(.*?)</action>`)
	for _, block := range relaxedActionRe.FindAllStringSubmatch(response, -1) {
		body := block[1]

		// Ищем tool: "name" или tool:"name" или tool: name
		relaxedToolRe := regexp.MustCompile(`(?i)tool\s*:\s*["']?(\w+)["']?`)
		tm := relaxedToolRe.FindStringSubmatch(body)
		if len(tm) < 2 {
			continue
		}

		act := action{Tool: tm[1], Args: make(map[string]string)}

		// Ищем аргументы с более гибкими правилами
		// Поддерживаем: arg: "value", arg:"value", arg: value (без кавычек)
		relaxedArgRe := regexp.MustCompile(`(?m)(\w+)\s*:\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|([^\n]+))`)
		for _, am := range relaxedArgRe.FindAllStringSubmatch(body, -1) {
			argName := am[1]
			if argName == "tool" {
				continue
			}

			var val string
			if am[2] != "" {
				val = am[2] // в двойных кавычках
			} else if am[3] != "" {
				val = am[3] // в одинарных кавычках
			} else if am[4] != "" {
				val = strings.TrimSpace(am[4]) // без кавычек
			}

			val = strings.ReplaceAll(val, `\n`, "\n")
			val = strings.ReplaceAll(val, `\"`, `"`)
			val = strings.ReplaceAll(val, `\'`, `'`)
			act.Args[argName] = val
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

## IMPORTANT: You MUST use tools to complete tasks

You are an autonomous agent that MUST interact with the repository using tools.
NEVER just describe what to do - ALWAYS use the tools to actually do it.
If no tools are needed, use the "done" tool to finish.

## Available Tools

read_file(path) - read file contents
write_file(path, content, message) - write/update file (commits directly to branch)
list_files(path) - list directory contents
search_code(query) - search code in repo
create_pr(title, body) - create pull request OR finish task (if direct commit mode)
done(summary) - complete task without PR

## Tool Usage Format (STRICT)

You MUST wrap each tool call in <action></action> tags with EXACTLY this format:

<action>
tool: "tool_name"
arg1: "value1"
arg2: "value2"
</action>

Examples:

<action>
tool: "read_file"
path: "src/main.go"
</action>

<action>
tool: "write_file"
path: "src/config.go"
content: "package main\n\nfunc Config() {}"
message: "feat: add config"
</action>

<action>
tool: "create_pr"
title: "feat: add feature X"
body: "Added feature X to main.go"
</action>

## Workflow Rules

1. ALWAYS write "Thought:" before actions to explain reasoning
2. ALWAYS use tools - never just describe what to do
3. Read files before editing to understand existing code
4. Multiple write_file calls create multiple commits (commits happen immediately)
5. After ALL changes are done, call create_pr OR done to finish
6. Keep code style consistent with existing patterns
7. If you don't see tool results, your format was wrong - check the format above

## Complete Example

User: Add a new config function to main.go

Thought: First, I need to read the current main.go to understand the structure
<action>
tool: "read_file"
path: "main.go"
</action>

[Tool returns file contents]

Thought: Now I'll add the config function, keeping the existing style
<action>
tool: "write_file"
path: "main.go"
content: "[full updated file content here]"
message: "feat: add config function"
</action>

[Tool confirms write]

Thought: All changes done, finishing the task
<action>
tool: "create_pr"
title: "feat: add config function"
body: "Added config function to main.go"
</action>`, owner, repo, ctx)
}
