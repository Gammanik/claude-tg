package main

import (
	"fmt"
	"runtime"
	"time"
)

// Встраиваются при компиляции через -ldflags
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

// getVersion возвращает информацию о версии бота
func getVersion() string {
	return fmt.Sprintf(`🤖 *Claude-TG Bot*

📌 Commit: `+"`%s`"+`
🏷 Version: `+"`%s`"+`
🔨 Built: `+"`%s`"+`
⚙️ Go: `+"`%s`"+`
💻 OS: `+"`%s/%s`"+`

🕐 Время: `+"`%s`",
		GitCommit,
		Version,
		BuildTime,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
		time.Now().Format("2006-01-02 15:04:05"))
}
