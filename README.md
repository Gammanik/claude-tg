# 🤖 claude-tg

AI-powered Telegram bot для GitHub. Чат с кодом, автономные PR, voice support.

## Возможности

- 🤖 **Autonomous Agent**: читает код, делает изменения, создаёт PR, смотрит CI
- 💬 **AI Chat**: вопросы о коде, архитектуре, объяснения
- 🎤 **Voice**: голосовые сообщения (STT/TTS)
- 🔀 **PR Management**: список, мерж, закрытие PR
- 🔧 **Multi-repo**: работает со всеми твоими репозиториями
- ⚡ **Smart Routing**: Haiku для роутинга, Sonnet для чата, Opus для кодинга

## Setup

```bash
git clone https://github.com/Gammanik/claude-tg.git
cd claude-tg
go build

# Настрой .env
cp .env.example .env
# Добавь токены: TELEGRAM_BOT_TOKEN, GITHUB_TOKEN, ANTHROPIC_API_KEY

# Запусти
./claude-tg
```

**Обязательные переменные:**
- `TELEGRAM_BOT_TOKEN` - от @BotFather
- `TELEGRAM_CHAT_ID` - твой chat ID
- `GITHUB_TOKEN` - GitHub PAT (repo scope)
- `ANTHROPIC_API_KEY` - Claude API ключ

**Опциональные:**
- `DIRECT_COMMIT=true` - коммитить прямо в main (без PR)
- `GROQ_API_KEY` - бесплатный Whisper STT
- `OPENAI_API_KEY` - TTS для голосовых ответов

## Использование

Просто пиши боту естественным языком:

```
"покажи PR"                    → список открытых PR
"какие у меня репо"            → твои репозитории
"добавь logout кнопку"         → агент создаст PR
"что делает main.go?"          → объяснение кода
"прочитай config, исправь баг" → множественные действия
```

**Команды:**
- `/repo owner/name` - переключить репозиторий
- `/repos [username]` - список репозиториев
- `/prs` - показать PR
- `/status` - статус бота

**💡 Авто-переключение:** упомяни `owner/repo` в сообщении → бот автоматически переключится

## Архитектура

```
Bot → Router (Haiku) → {Chat (Sonnet) | Agent (Opus) | GitHub}
                         ↓
                    ReAct Loop → {read_file, write_file, create_pr}
```

**Файлы:**
- `bot.go` - Telegram + роутинг
- `agent.go` - ReAct loop (25 итераций макс)
- `llm.go` - Haiku/Sonnet/Opus
- `github.go` - GitHub API
- `voice.go` - STT/TTS

## Примеры

**Кодинг:**
```
User: добавь валидацию в форму

💭 Читаю форму...
⚡ read_file(form.tsx)
⚡ write_file(form.tsx)
⚡ create_pr
🚀 PR #42 создан
```

**Множественные действия:**
```
User: прочитай README и обнови версию на 2.0

💭 Читаю README...
⚡ read_file(README.md)
💭 Обновляю версию...
⚡ write_file(README.md)
✅ Готово (версия 2.0)
```

## License

MIT