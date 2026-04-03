# 🤖 claude-tg

AI-powered Telegram bot for GitHub repository management. Simple, focused, efficient.

## Features

### 🚀 Autonomous Coding Agent
- **Full Development Cycle**: Reads code → Makes changes → Creates PRs → Watches CI
- **ReAct Pattern**: Thought → Action → Observation loop (max 25 iterations)
- **Auto-approval Gates**: PR creation requires user confirmation

### 🧠 Model Hierarchy
Smart model selection for optimal cost/performance:
- **Haiku** (fast, cheap) - Intent routing, simple classification
- **Sonnet** (balanced) - Chat conversations, Q&A
- **Opus** (powerful) - Coding tasks, complex analysis

### 🔧 Available Tools

#### File Operations
- 📖 `read_file` - Read file contents
- ✏️ `write_file` - Write/modify files
- 📁 `list_files` - List directory contents

#### Code Analysis
- 🔍 `search_code` - Search code across repository

#### GitHub Integration
- 🚀 `create_pr` - Create pull requests (with approval)
- ✅ Auto-watch CI status
- 🔀 Inline PR merge/close buttons

### 💬 Telegram Features
- **Voice Support**: Whisper STT (Groq) + OpenAI TTS
- **Message History**: Search past conversations
- **Forum Topics**: Each repo gets dedicated thread

## Setup

1. **Clone & Install**
```bash
git clone https://github.com/Gammanik/claude-tg.git
cd claude-tg
go build
```

2. **Configure Environment**
```bash
cp .env.example .env
# Edit .env with your tokens
```

Required:
- `TELEGRAM_BOT_TOKEN` - from @BotFather
- `TELEGRAM_CHAT_ID` - your chat ID
- `GITHUB_TOKEN` - GitHub personal access token
- `ANTHROPIC_API_KEY` or `DEEPSEEK_API_KEY` - LLM provider

Optional:
- `GROQ_API_KEY` - Free STT (Whisper)
- `OPENAI_API_KEY` - TTS (voice responses)

3. **Run**
```bash
./claude-tg
```

## Usage

Just chat naturally:

```
"покажи PR"           → Lists open pull requests
"смержи PR #5"        → Merges PR with confirmation
"добавь логин кнопку" → Creates feature PR
"что делает main.go?" → Explains code
```

Voice messages work too! 🎤

## Commands

- `/repo owner/name` - Switch repository
- `/prs` - List open PRs
- `/status` - Show bot status
- `/help` - Show help

## Architecture

```
┌─────────────┐
│   Telegram  │
└──────┬──────┘
       │
┌──────▼──────┐       ┌─────────────┐
│     Bot     │◄──────┤  LLMClient  │  Haiku/Sonnet/Opus
└──────┬──────┘       └─────────────┘
       │
┌──────▼──────┐       ┌─────────────┐
│    Agent    │◄──────┤   GitHub    │  PRs, CI/CD
└─────────────┘       └─────────────┘
```

**Core Files:**
- `main.go` - Entry point
- `bot.go` - Telegram handling, routing
- `agent.go` - ReAct loop, tool execution
- `llm.go` - Model hierarchy, API calls
- `github.go` - GitHub API integration
- `voice.go` - STT/TTS
- `history.go` - Message search
- `topics.go` - Forum topic management

## Provider Comparison

| Feature | Anthropic | DeepSeek |
|---------|-----------|----------|
| Model Hierarchy | ✅ Haiku/Sonnet/Opus | ❌ Single model |
| Streaming | ✅ | ❌ |
| Prompt Caching | ✅ | ❌ |
| Cost | $$$ | $ |
| Quality | Best | Good |

**Recommendation**: Use Anthropic for production, DeepSeek for development.

## Examples

### Coding Task
```
User: добавь кнопку logout в хедер

💭 Читаю структуру репо...
💭 Ищу Header компонент...
⚡ read_file(src/Header.jsx)
✓ Found Header component
⚡ write_file(src/Header.jsx)
✓ Added logout button
⚡ create_pr
🚀 PR #42 created
✅ Готово за 45s
```

### PR Management
```
User: покажи мои PR

📋 Открытые PR для Gammanik/PeerPack:

1. #42 - Add logout button
   ✅ Merge #42  ❌ Close #42

2. #41 - Fix auth bug
   ✅ Merge #41  ❌ Close #41
```

## Development

Build:
```bash
go build -o claude-tg
```

Run with logging:
```bash
./claude-tg 2>&1 | tee bot.log
```

## License

MIT

## Credits

Built with:
- [Anthropic Claude](https://anthropic.com) - AI models
- [go-telegram-bot-api](https://github.com/go-telegram-bot-api/telegram-bot-api) - Telegram SDK
- [Groq](https://groq.com) - Free Whisper STT
