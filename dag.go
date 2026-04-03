package main

import (
	"fmt"
	"strings"
	"sync"
)

// ActionNode - узел в графе зависимостей
type ActionNode struct {
	ID           int
	Action       action
	Dependencies []int // ID других action, от которых зависит этот
	EstimatedMs  int   // оценка времени выполнения в мс
}

// ActionDAG - направленный ациклический граф для параллельного выполнения
type ActionDAG struct {
	Nodes     []ActionNode
	completed map[int]bool
	results   map[int]string
	errors    map[int]string
	mu        sync.Mutex
}

// NewActionDAG - создает DAG из списка actions с анализом зависимостей
func NewActionDAG(actions []action) *ActionDAG {
	dag := &ActionDAG{
		Nodes:     make([]ActionNode, len(actions)),
		completed: make(map[int]bool),
		results:   make(map[int]string),
		errors:    make(map[int]string),
	}

	// Создаем узлы
	for i, act := range actions {
		dag.Nodes[i] = ActionNode{
			ID:           i,
			Action:       act,
			Dependencies: []int{},
			EstimatedMs:  estimateActionTime(act),
		}
	}

	// Анализируем зависимости
	dag.analyzeDependencies()

	return dag
}

// analyzeDependencies - определяет зависимости между actions
func (dag *ActionDAG) analyzeDependencies() {
	for i := range dag.Nodes {
		deps := dag.findDependencies(i)
		dag.Nodes[i].Dependencies = deps
	}
}

// findDependencies - находит все actions, от которых зависит текущий
func (dag *ActionDAG) findDependencies(nodeID int) []int {
	deps := []int{}

	// Проверяем каждый предыдущий action
	for j := 0; j < nodeID; j++ {
		if dag.hasDependency(nodeID, j) {
			deps = append(deps, j)
		}
	}

	return deps
}

// hasDependency - проверяет, зависит ли action i от action j
func (dag *ActionDAG) hasDependency(i, j int) bool {
	nodeI := &dag.Nodes[i]
	nodeJ := &dag.Nodes[j]

	// write_file зависит от read_file если читает тот же файл
	if nodeI.Action.Tool == "write_file" && nodeJ.Action.Tool == "read_file" {
		if nodeI.Action.Args["path"] == nodeJ.Action.Args["path"] {
			return true
		}
	}

	// write_file зависит от предыдущего write_file того же файла
	if nodeI.Action.Tool == "write_file" && nodeJ.Action.Tool == "write_file" {
		if nodeI.Action.Args["path"] == nodeJ.Action.Args["path"] {
			return true
		}
	}

	// spawn_subagent и orchestrate всегда зависят от всех предыдущих
	if nodeI.Action.Tool == "spawn_subagent" || nodeI.Action.Tool == "orchestrate" {
		return true
	}

	// create_pr зависит от всех write_file
	if nodeI.Action.Tool == "create_pr" && nodeJ.Action.Tool == "write_file" {
		return true
	}

	// done зависит от всех предыдущих
	if nodeI.Action.Tool == "done" {
		return true
	}

	// Если action использует результат предыдущего в аргументах
	if dag.usesResultInArgs(nodeI, nodeJ) {
		return true
	}

	return false
}

// usesResultInArgs - проверяет, использует ли action результат другого в аргументах
func (dag *ActionDAG) usesResultInArgs(nodeI, nodeJ *ActionNode) bool {
	// Простая эвристика: если в аргументах есть упоминание результата
	for _, arg := range nodeI.Action.Args {
		argLower := strings.ToLower(arg)
		// Проверяем упоминание пути из предыдущего read/write
		if path := nodeJ.Action.Args["path"]; path != "" {
			if strings.Contains(argLower, strings.ToLower(path)) {
				return true
			}
		}
		// Проверяем упоминание query из search
		if query := nodeJ.Action.Args["query"]; query != "" {
			if strings.Contains(argLower, strings.ToLower(query)) {
				return true
			}
		}
	}
	return false
}

// GetExecutableNodes - возвращает все actions, готовые к выполнению
func (dag *ActionDAG) GetExecutableNodes() []int {
	dag.mu.Lock()
	defer dag.mu.Unlock()

	executable := []int{}

	for i := range dag.Nodes {
		if dag.completed[i] {
			continue // уже выполнено
		}

		// Проверяем, что все зависимости выполнены
		ready := true
		for _, depID := range dag.Nodes[i].Dependencies {
			if !dag.completed[depID] {
				ready = false
				break
			}
			// Если есть ошибка в зависимости - пропускаем
			if dag.errors[depID] != "" {
				ready = false
				break
			}
		}

		if ready {
			executable = append(executable, i)
		}
	}

	return executable
}

// MarkCompleted - отмечает action как выполненный
func (dag *ActionDAG) MarkCompleted(nodeID int, result string, err string) {
	dag.mu.Lock()
	defer dag.mu.Unlock()

	dag.completed[nodeID] = true
	if err != "" {
		dag.errors[nodeID] = err
	} else {
		dag.results[nodeID] = result
	}
}

// GetResult - получает результат выполнения action
func (dag *ActionDAG) GetResult(nodeID int) string {
	dag.mu.Lock()
	defer dag.mu.Unlock()
	return dag.results[nodeID]
}

// IsCompleted - проверяет, выполнен ли action
func (dag *ActionDAG) IsCompleted(nodeID int) bool {
	dag.mu.Lock()
	defer dag.mu.Unlock()
	return dag.completed[nodeID]
}

// AllCompleted - проверяет, все ли actions выполнены
func (dag *ActionDAG) AllCompleted() bool {
	dag.mu.Lock()
	defer dag.mu.Unlock()
	return len(dag.completed) == len(dag.Nodes)
}

// GetEstimatedTime - возвращает общую оценку времени выполнения
func (dag *ActionDAG) GetEstimatedTime() int {
	// Используем критический путь (longest path) в DAG
	return dag.criticalPathLength()
}

// criticalPathLength - находит длину критического пути (наибольшее время)
func (dag *ActionDAG) criticalPathLength() int {
	memo := make(map[int]int)
	maxTime := 0

	for i := range dag.Nodes {
		time := dag.longestPath(i, memo)
		if time > maxTime {
			maxTime = time
		}
	}

	return maxTime
}

// longestPath - рекурсивно находит самый длинный путь от узла
func (dag *ActionDAG) longestPath(nodeID int, memo map[int]int) int {
	if cached, ok := memo[nodeID]; ok {
		return cached
	}

	node := &dag.Nodes[nodeID]
	maxDepTime := 0

	for _, depID := range node.Dependencies {
		depTime := dag.longestPath(depID, memo)
		if depTime > maxDepTime {
			maxDepTime = depTime
		}
	}

	result := maxDepTime + node.EstimatedMs
	memo[nodeID] = result
	return result
}

// PrintDAG - выводит граф в читаемом виде (для дебага)
func (dag *ActionDAG) PrintDAG() string {
	var sb strings.Builder
	sb.WriteString("Action DAG:\n")

	for i := range dag.Nodes {
		node := &dag.Nodes[i]
		sb.WriteString(fmt.Sprintf("  [%d] %s (est. %dms)\n", i, node.Action.Tool, node.EstimatedMs))

		if len(node.Dependencies) > 0 {
			sb.WriteString(fmt.Sprintf("      depends on: %v\n", node.Dependencies))
		}
	}

	criticalPath := dag.GetEstimatedTime()
	sb.WriteString(fmt.Sprintf("\nCritical path: %dms (~%ds)\n", criticalPath, criticalPath/1000))

	return sb.String()
}

// estimateActionTime - оценка времени выполнения тула в миллисекундах
func estimateActionTime(act action) int {
	estimates := map[string]int{
		"read_file":      2000,  // 2s
		"write_file":     4000,  // 4s
		"list_files":     1500,  // 1.5s
		"search_code":    3000,  // 3s
		"search_history": 500,   // 0.5s
		"get_summary":    1000,  // 1s
		"get_user_repos": 2000,  // 2s
		"set_avatar":     8000,  // 8s (DALL-E генерация)
		"manage_topics":  1000,  // 1s
		"spawn_subagent": 30000, // 30s
		"orchestrate":    20000, // 20s
		"create_pr":      3000,  // 3s
		"done":           100,   // 0.1s
	}

	if time, ok := estimates[act.Tool]; ok {
		return time
	}
	return 5000 // дефолт 5s
}
