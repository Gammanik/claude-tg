# PeerPack Dev Bot — Setup Guide

Телеграм-бот который делает PR-ы в PeerPack по твоим сообщениям с телефона.

## Быстрый старт (20 минут)

### 1. Создай Telegram бота

```
@BotFather → /newbot → "PeerPack Dev" → получи токен
```

### 2. GitHub токен

GitHub → Settings → Developer Settings → Personal Access Tokens → Tokens (classic)

Права: `repo` ✅, `workflow` ✅

### 3. DeepSeek API (дешевле всего)

https://platform.deepseek.com → API Keys → Create

~$5-8/месяц при 2ч/день

### 4. Supabase test project (для интеграционных тестов)

https://supabase.com → New Project → "peerpack-test"

Скопируй URL и service_role key из Settings → API

### 5. GitHub Secrets (для CI)

В репо PeerPack: Settings → Secrets → Actions:

```
SUPABASE_TEST_URL        = https://xxx.supabase.co
SUPABASE_TEST_ANON_KEY   = eyJxxx
SUPABASE_TEST_SERVICE_KEY = eyJxxx (service_role)
```

### 6. Установи тесты в репо PeerPack

```bash
# В директории PeerPack
npm install --save-dev @playwright/test vitest @supabase/supabase-js

# Скопируй файлы:
cp -r peerpack-tests/tests ./tests
cp peerpack-tests/playwright.config.ts .
cp peerpack-tests/.github/workflows/ci.yml .github/workflows/

# Добавь в package.json:
# "test:e2e": "playwright test",
# "test:integration": "vitest run tests/integration"
```

### 7. Задеплой оркестратор на Railway

```bash
# Установи Railway CLI
npm install -g @railway/cli

# В директории peerpack-orchestrator
railway login
railway init
railway up

# Добавь переменные окружения в Railway UI:
TELEGRAM_BOT_TOKEN=xxx
TELEGRAM_CHAT_ID=155741924
GITHUB_TOKEN=xxx
DEEPSEEK_API_KEY=xxx
```

### 8. Проверь

Напиши боту: `привет` — должен ответить help message.

Напиши задачу: `добавь кнопку поделиться на главном экране` — бот создаст PR.

## Структура файлов

```
peerpack-orchestrator/
├── cmd/bot/main.go          # точка входа
├── internal/
│   ├── agent/agent.go       # LLM ReAct loop
│   ├── github/client.go     # GitHub API
│   └── telegram/bot.go      # Telegram бот
├── Dockerfile
├── railway.json
└── .env.example

tests/ (копируешь в PeerPack репо)
├── e2e/app.spec.ts          # Playwright тесты
├── integration/supabase.test.ts  # Supabase тесты
└── setup/telegram-mock.ts  # Mock для TMA

.github/workflows/ci.yml    # CI: lint → build → e2e → auto-merge
```

## Как использовать

С телефона пишешь боту обычный текст:

```
добавь экран трекинга посылки
рефактори SearchCouriers на отдельные страницы  
добавь валидацию телефона в форму регистрации
исправь баг с датами в карточке курьера
```

Бот:
1. Читает CLAUDE.md и структуру репо
2. Создаёт ветку `feat/...`
3. Пишет код + тест
4. Открывает PR
5. Ждёт CI (lint + build + Playwright)
6. Если тесты прошли → автомерж
7. Если тесты упали → пробует починить сам

## Цена

| Компонент | Стоимость |
|-----------|-----------|
| DeepSeek V3 API (~2ч/день) | ~$5-8/мес |
| Railway (оркестратор) | $0 (free tier) |
| Supabase test project | $0 (free tier) |
| GitHub Actions | $0 (2000 мин/мес) |
| **Итого** | **~$8/мес** |
