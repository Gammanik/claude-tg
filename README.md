# 🤖 claude-tg

AI-powered Telegram coding assistant that autonomously handles development tasks with live progress tracking.

## Features

### 🚀 Autonomous Coding Agent
- **Full Development Cycle**: Reads code → Makes changes → Creates PRs → Watches CI/CD
- **Auto-fixing**: Automatically fixes failing tests
- **Parallel Execution**: Independent tools run concurrently (DAG-based dependency resolution)
- **Smart Planning**: LLM estimates execution time for each tool

### 📊 Live Progress Tracking
- Real-time progress bars with completion percentage
- Token usage statistics (input/output/cache)
- Cost estimation for API calls
- Time tracking for each operation
- Visual tool execution feedback

### 🔧 Available Tools

#### File Operations
- 📖 `read_file` - Read file contents
- ✏️ `write_file` - Write/modify files with auto-commit
- 📁 `list_files` - List directory contents

#### Code Analysis
- 🔍 `search_code` - Search code across repository
- 🔎 `search_history` - Search conversation history
- 📋 `get_summary` - Get conversation summary

#### GitHub Integration
- 🚀 `create_pr` - Create pull requests (with approval gate)
- 📂 `get_user_repos` - List user's active repositories

#### Task Management
- 🤖 `spawn_subagent` - Create subtask with dedicated agent
- 🎯 `orchestrate` - Run multiple tasks in parallel
- 📌 `manage_topics` - Create/delete Telegram topics
- 🎨 `set_avatar` - Update bot avatar

### 🧠 AI Features
- **Multi-provider LLM**: Claude Sonnet 4 or DeepSeek
- **Prompt Caching**: Reduces costs with Anthropic's prompt caching
- **Streaming Responses**: Real-time output with 400ms updates
- **ReAct Pattern**: Thought → Action → Observation loop (max 25 iterations)

### 💬 Telegram Integration
- **Forum Topics**: Each repo gets dedicated topic/thread
- **Voice Support**: Whisper STT (Groq/OpenAI) + OpenAI TTS
- **Interactive Approvals**: Inline buttons for PR creation/merging
- **Reminders**: NLP-based timer system
- **Calendar Integration**: Auto-generate Google Calendar events

### 📈 Usage Limits & Monitoring
- **Hourly Limit**: 1M tokens/hour
- **Weekly Limit**: 10M tokens/week
- **Warnings**: At 80% usage
- **Auto-blocking**: Prevents API overuse

## Installation

### Prerequisites
- Go 1.22+
- Telegram Bot Token (via @BotFather)
- GitHub Personal Access Token
- Anthropic API Key (or DeepSeek API Key)

### Environment Variables

```bash
# Required
TELEGRAM_BOT_TOKEN=your_telegram_token
TELEGRAM_CHAT_ID=your_chat_id
GITHUB_TOKEN=your_github_token
ANTHROPIC_API_KEY=sk-ant-...

# Repository defaults
DEFAULT_OWNER=your_username
DEFAULT_REPO=your_repo

# Optional
LLM_PROVIDER=claude  # or "deepseek"
DEEPSEEK_API_KEY=sk-...

# Voice (optional)
OPENAI_API_KEY=sk-...  # For Whisper STT + TTS + DALL-E avatars
GROQ_API_KEY=gsk-...   # Free Whisper STT alternative
```

### Build & Run

```bash
# Clone
git clone https://github.com/Gammanik/peerpack-bot
cd peerpack-bot

# Build
go build -o claude-tg

# Run
./claude-tg
```

### Docker

```dockerfile
# Use the included Dockerfile
docker build -t claude-tg .
docker run -d --env-file .env claude-tg
```

## Usage

### Commands

```
/help        - Show help
/status      - Show active tasks & reminders
/prs         - List open PRs
/tasks       - List running tasks
/reminders   - List active reminders
/repo owner/name - Switch repository context
/cancel ID   - Cancel running task
```

### Natural Language

**Tasks** (triggers autonomous agent):
```
добавь темную тему
fix bug in authentication
create API endpoint for users
refactor login component
```

**Questions** (streaming chat):
```
как работает аутентификация?
what does this function do?
explain the database schema
```

**Reminders**:
```
напомни через 2 часа проверить PR
remind me tomorrow at 9am about meeting
```

**Management**:
```
поменяй аватарку
над какими проектами я работаю?
создай топики для активных репо
```

## Example Workflow

### Coding Task with Parallel Execution

```
User: добавь темную тему в приложение

Bot:
💡 Execution plan:
Action DAG:
  [0] read_file (est. 2000ms)
  [1] read_file (est. 2000ms)
  [2] search_code (est. 3000ms)
  [3] write_file (est. 4000ms)
      depends on: [0]
  [4] write_file (est. 4000ms)
      depends on: [1]
  [5] create_pr (est. 3000ms)
      depends on: [3, 4]

Critical path: 9000ms (~9s)

⚙️ *Добавить темную тему*
`owner/repo` → `feat/add-dark-theme`
⏱ 8s • ест. ещё ~3s
▓▓▓▓▓▓▓▓░░ 80% (4/5)

✅ 📖 `read_file(theme.css)` 2s || 📖 `read_file(colors.js)` 2s
✅ 🔍 `search_code(theme)` 3s
✅ ✏️ `write_file(dark.css)` 4s
🔄 🚀 `create_pr` (2s)

🔢 Токены: 12.4K in / 2.1K out
💾 Кэш: 9.8K read / 1.5K write
💰 ~$0.0624 (8s)
📊 14.5K токенов / 3 вызовов
⚡ 3 тулов выполнено параллельно

🚀 PR #42 created

✅ Задача завершена за 9s

📝 *Добавить темную тему*
🔗 PR #42

💬 Добавлен DarkMode компонент. Параллельно обработаны
стили и конфиг цветов. Время выполнения сокращено с 15s до 9s.

📅 Добавить в календарь
```

### Management Task

```
User: мои репо по которым ведется работа

Bot:
💡 Получаю список активных репозиториев...
📂 get_user_repos
→ Gammanik

✓ Активные репозитории Gammanik (5):

1. Gammanik/peerpack-bot (private, Go)
   Обновлен: 03.04.2026
   AI coding assistant for Telegram

2. Gammanik/peerpack (private, TypeScript)
   Обновлен: 02.04.2026
   P2P package manager

3. Gammanik/skyfarm (public, Rust)
   Обновлен: 28.03.2026
   Distributed cloud farming

4. Gammanik/claude-tg (private, Go)
   Обновлен: 03.04.2026
   Telegram bot with Claude AI

5. Gammanik/react-widgets (public, JavaScript)
   Обновлен: 15.03.2026
   Reusable React components

⚙️ *Список активных проектов*
⏱ 2s
▓▓▓▓▓▓▓▓▓▓ 100% (1/1)

✅ 📂 `get_user_repos(Gammanik)` 2s

🔢 Токены: 1.2K in / 450 out
💰 ~$0.0104 (2s)

✅ Готово за 2s
```

## Architecture

### Agent ReAct Loop

```
1. LLM generates Thought + Actions
2. Parse actions and build dependency graph (DAG)
3. Execute independent actions in parallel
4. Collect results
5. Feed results back to LLM
6. Repeat until done (max 25 iterations)
```

### Parallel Execution

The agent analyzes action dependencies:
- **No dependency**: Run in parallel
- **Sequential dependency**: Wait for previous result
- **DAG topological sort**: Optimal execution order

Example:
```python
# Parallel (no dependencies)
read_file(A) ┐
read_file(B) ├→ [execute simultaneously]
read_file(C) ┘

# Sequential (B needs A's output)
read_file(A) → write_file(B, content_from_A)
```

### Caching

- **Prompt Caching** (Anthropic): System prompt cached between iterations
- **Message History**: Last 1000 messages in-memory
- **Topic State**: Telegram forum topics persist across restarts

## File Structure

```
peerpack-bot/
├── main.go         # Entry point
├── config.go       # Configuration
├── bot.go          # Telegram bot & message routing
├── agent.go        # AI agent & ReAct loop
├── stream.go       # LLM streaming
├── progress.go     # Live progress tracking
├── tokens.go       # Token statistics & limits
├── history.go      # Message history & search
├── github.go       # GitHub API client
├── topics.go       # Telegram topic management
├── thread.go       # Raw Telegram API (threads/buttons)
├── voice.go        # Speech-to-text & text-to-speech
├── actions.go      # Direct action handlers
├── calendar.go     # Google Calendar integration
└── Dockerfile      # Container build
```

## Cost Estimation

**Claude Sonnet 4** (typical task):
- Input: 15K tokens × $3/MTok = $0.045
- Output: 3K tokens × $15/MTok = $0.045
- Cache read: 12K × $0.30/MTok = $0.0036
- **Total**: ~$0.09 per task

**DeepSeek** (budget option):
- Much cheaper alternative
- Slightly lower quality

## Development

### Adding a New Tool

1. Add tool case in `agent.go:execute()`
2. Add tool icon in `progress.go:getToolIcon()`
3. Add tool example in `agent.go:systemPrompt()`
4. Implement tool logic

Example:
```go
case "my_tool":
    result := doSomething(act.Args["param"])
    return result, 0, "", false, ""
```

### Testing

```bash
# Build
go build -o claude-tg

# Run with verbose logging
LOG_LEVEL=debug ./claude-tg
```

## Limitations

- Max 25 ReAct iterations per task
- File size limit: 8000 chars per read
- Search results: First 20 files
- History: Last 1000 messages
- Hourly API limit: 1M tokens

## Security

- **Approval Gates**: PR creation/merging requires user confirmation
- **No Force Push**: Git safety protocol prevents destructive operations
- **API Key Validation**: Checks for missing keys before execution
- **Rate Limiting**: Prevents API abuse

## Roadmap

- [ ] Multi-file diff editing
- [ ] Image generation for UI mockups
- [ ] Database query execution
- [ ] Deployment automation
- [ ] Code review comments
- [ ] Issue auto-creation from errors

## Contributing

1. Fork the repository
2. Create feature branch (`git checkout -b feature/amazing`)
3. Commit changes (`git commit -m 'Add amazing feature'`)
4. Push to branch (`git push origin feature/amazing`)
5. Open Pull Request

## License

MIT License - see LICENSE file

## Credits

Built with:
- [Claude Sonnet 4](https://www.anthropic.com/claude) - AI reasoning
- [go-telegram-bot-api](https://github.com/go-telegram-bot-api/telegram-bot-api) - Telegram integration
- [GitHub API](https://docs.github.com/en/rest) - Repository management
- [Groq](https://groq.com/) - Free Whisper STT

---

**Made with ❤️ by [@Gammanik](https://github.com/Gammanik)**

**Powered by Claude Sonnet 4.5**
