package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TaskPlan представляет план выполнения задачи
type TaskPlan struct {
	Phases        []PlanPhase       `json:"phases"`
	EstimatedTime time.Duration     `json:"estimated_time_ms"`
	CriticalPath  []int             `json:"critical_path"`
	Metadata      map[string]string `json:"metadata"`
}

// PlanPhase представляет одну фазу плана
type PlanPhase struct {
	ID           int             `json:"id"`
	Name         string          `json:"name"`
	Tools        []PlannedAction `json:"tools"`
	Dependencies []int           `json:"dependencies"`
	Parallel     bool            `json:"parallel"`
}

// PlannedAction представляет запланированное действие
type PlannedAction struct {
	Tool        string            `json:"tool"`
	Args        map[string]string `json:"args"`
	EstimatedMs int               `json:"estimated_ms"`
	Reasoning   string            `json:"reasoning"`
}

// Planner создает и управляет планами задач
type Planner struct {
	cfg         Config
	owner, repo string
	bot         *Bot
}

// NewPlanner создает новый планировщик
func NewPlanner(cfg Config, owner, repo string, bot *Bot) *Planner {
	return &Planner{
		cfg:   cfg,
		owner: owner,
		repo:  repo,
		bot:   bot,
	}
}

// CreatePlan создает план выполнения задачи используя Extended Thinking
func (p *Planner) CreatePlan(task *Task, context string, onThinking func(string)) (*TaskPlan, error) {
	system := p.planningSystemPrompt(context)
	userPrompt := fmt.Sprintf(`Analyze this task deeply and create a detailed execution plan.

Task: %s

Use extended thinking to:
1. Break down the task into logical phases
2. Determine which actions can run in parallel
3. Identify dependencies between actions
4. Estimate execution time
5. Consider edge cases and error handling

Output ONLY valid JSON in this exact format:
{
  "phases": [
    {
      "id": 0,
      "name": "Read existing code",
      "tools": [
        {"tool": "read_file", "args": {"path": "..."}, "reasoning": "...", "estimated_ms": 3000}
      ],
      "dependencies": [],
      "parallel": true
    }
  ],
  "estimated_time_ms": 15000,
  "critical_path": [0, 2, 4],
  "metadata": {}
}`, task.Description)

	content, thinking, err := p.bot.streamClaudeWithThinking(system, userPrompt, onThinking, nil)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}

	// Извлекаем JSON из ответа (может быть обернут в markdown)
	jsonStr := extractJSON(content)
	if jsonStr == "" {
		return nil, fmt.Errorf("no valid JSON in response, thinking: %s", truncate(thinking, 200))
	}

	var plan TaskPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w\nContent: %s", err, truncate(jsonStr, 500))
	}

	// Валидация плана
	if len(plan.Phases) == 0 {
		return nil, fmt.Errorf("plan has no phases")
	}

	return &plan, nil
}

// Replan создает новый план при ошибке выполнения
func (p *Planner) Replan(task *Task, oldPlan *TaskPlan, errorMsg string) (*TaskPlan, error) {
	system := p.planningSystemPrompt("")
	userPrompt := fmt.Sprintf(`The previous plan failed with this error:

Error: %s

Previous plan:
%s

Task: %s

Create a NEW plan that fixes this issue. Output ONLY valid JSON in the same format.`,
		errorMsg, oldPlan.Format(), task.Description)

	content, _, err := p.bot.streamClaudeWithThinking(system, userPrompt, func(thinking string) {
		task.Steps <- Step{Type: StepPlanning, Content: "Replanning: " + thinking}
	}, nil)

	if err != nil {
		return nil, fmt.Errorf("replanning failed: %w", err)
	}

	jsonStr := extractJSON(content)
	if jsonStr == "" {
		return nil, fmt.Errorf("no valid JSON in replan response")
	}

	var plan TaskPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse replan JSON: %w", err)
	}

	if len(plan.Phases) == 0 {
		return nil, fmt.Errorf("replan has no phases")
	}

	return &plan, nil
}

// Format форматирует план для вывода пользователю
func (plan *TaskPlan) Format() string {
	var sb strings.Builder
	sb.WriteString("📋 Execution Plan\n\n")

	for i, phase := range plan.Phases {
		sb.WriteString(fmt.Sprintf("Phase %d: %s\n", phase.ID, phase.Name))
		if len(phase.Dependencies) > 0 {
			sb.WriteString(fmt.Sprintf("  Dependencies: %v\n", phase.Dependencies))
		}
		if phase.Parallel {
			sb.WriteString("  ⚡ Parallel execution\n")
		}
		for _, tool := range phase.Tools {
			argStr := ""
			if len(tool.Args) > 0 {
				args := []string{}
				for k, v := range tool.Args {
					args = append(args, fmt.Sprintf("%s=%s", k, truncate(v, 30)))
				}
				argStr = strings.Join(args, ", ")
			}
			sb.WriteString(fmt.Sprintf("  • %s(%s) ~%dms\n", tool.Tool, argStr, tool.EstimatedMs))
			if tool.Reasoning != "" {
				sb.WriteString(fmt.Sprintf("    → %s\n", truncate(tool.Reasoning, 80)))
			}
		}
		if i < len(plan.Phases)-1 {
			sb.WriteString("\n")
		}
	}

	estTime := time.Duration(plan.EstimatedTime) * time.Millisecond
	sb.WriteString(fmt.Sprintf("\n⏱ Estimated time: %s\n", fmtDuration(estTime)))

	if len(plan.CriticalPath) > 0 {
		sb.WriteString(fmt.Sprintf("🎯 Critical path: %v\n", plan.CriticalPath))
	}

	return sb.String()
}

// ValidateActions проверяет соответствуют ли действия агента плану
func (plan *TaskPlan) ValidateActions(phaseID int, actions []action) bool {
	if phaseID >= len(plan.Phases) {
		return true // план уже выполнен, разрешаем любые действия
	}

	phase := plan.Phases[phaseID]
	plannedTools := make(map[string]bool)
	for _, tool := range phase.Tools {
		plannedTools[tool.Tool] = true
	}

	// Проверяем что все действия из ожидаемых инструментов
	for _, act := range actions {
		if !plannedTools[act.Tool] {
			// Разрешаем некоторые тулы всегда (done, create_pr)
			if act.Tool == "done" || act.Tool == "create_pr" {
				continue
			}
			return false
		}
	}

	return true
}

// GetPendingTools возвращает список инструментов, которые еще нужно выполнить
func (plan *TaskPlan) GetPendingTools() []string {
	var tools []string
	for _, phase := range plan.Phases {
		for _, tool := range phase.Tools {
			tools = append(tools, tool.Tool)
		}
	}
	return tools
}

func (p *Planner) planningSystemPrompt(context string) string {
	contextPart := ""
	if context != "" {
		contextPart = fmt.Sprintf("\n## Project Context\n%s\n", context)
	}

	return fmt.Sprintf(`You are an AI planning agent for %s/%s.
%s
Your task is to analyze a coding task and create a detailed execution plan.

## Available Tools
- read_file(path) - read file contents
- write_file(path, content, message) - write file with commit message
- list_files(path) - list directory contents
- search_code(query) - search code in repository
- create_pr(title, body) - create pull request
- done(summary) - mark task as complete

## Planning Rules
1. Break down complex tasks into phases
2. Identify which operations can run in parallel
3. Determine dependencies between phases
4. Estimate realistic execution time for each action
5. Consider error handling and edge cases

## Output Format
You MUST output valid JSON only. No markdown, no explanations, just JSON:
{
  "phases": [...],
  "estimated_time_ms": number,
  "critical_path": [phase_ids],
  "metadata": {}
}

Each phase must have:
- id: unique integer
- name: descriptive name
- tools: array of planned actions
- dependencies: array of phase IDs that must complete first
- parallel: boolean, true if tools can run in parallel

Each tool must have:
- tool: tool name
- args: object with tool arguments
- estimated_ms: estimated milliseconds
- reasoning: why this action is needed`, p.owner, p.repo, contextPart)
}

// extractJSON извлекает JSON из текста (убирает markdown обертки если есть)
func extractJSON(text string) string {
	// Убираем markdown code blocks
	text = strings.TrimSpace(text)

	// Ищем JSON объект
	start := strings.Index(text, "{")
	if start == -1 {
		return ""
	}

	// Находим парную закрывающую скобку
	depth := 0
	for i := start; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}

	return ""
}
