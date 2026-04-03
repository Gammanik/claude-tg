package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// getVersion возвращает информацию о версии бота
func getVersion() string {
	var sb strings.Builder

	sb.WriteString("🤖 *Claude-TG Bot*\n\n")

	// Git commit hash
	if hash := getGitHash(); hash != "" {
		sb.WriteString(fmt.Sprintf("📌 Commit: `%s`\n", hash))
	}

	// Git branch
	if branch := getGitBranch(); branch != "" {
		sb.WriteString(fmt.Sprintf("🌿 Branch: `%s`\n", branch))
	}

	// Build info
	sb.WriteString(fmt.Sprintf("⚙️ Go: `%s`\n", runtime.Version()))
	sb.WriteString(fmt.Sprintf("💻 OS: `%s/%s`\n", runtime.GOOS, runtime.GOARCH))

	// Uptime (если нужно)
	sb.WriteString(fmt.Sprintf("\n🕐 Время: `%s`", time.Now().Format("2006-01-02 15:04:05")))

	return sb.String()
}

// getGitHash возвращает текущий git commit hash (короткий)
func getGitHash() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// getGitBranch возвращает текущую git ветку
func getGitBranch() string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
