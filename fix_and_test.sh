#!/bin/bash
# Скрипт для проверки и исправления API ключей

echo "🔧 Проверка API ключей..."
echo ""

source .env

# Anthropic Test
echo "1️⃣ Anthropic Claude (рекомендуется):"
echo "   Используемая модель: claude-opus-4-5-20251101"
echo ""

if [ -z "$ANTHROPIC_API_KEY" ]; then
  echo "   ❌ Ключ пустой!"
  echo "   👉 Создай новый: https://console.anthropic.com/settings/keys"
else
  echo "   Тестирую ключ: ${ANTHROPIC_API_KEY:0:20}..."

  anthro=$(curl -s https://api.anthropic.com/v1/messages \
    -H "x-api-key: $ANTHROPIC_API_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" \
    -d '{"model":"claude-opus-4-5-20251101","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}')

  if echo "$anthro" | grep -q '"content"'; then
    echo "   ✅ РАБОТАЕТ! Можно использовать"
    echo ""
    echo "   Ответ Claude:"
    echo "$anthro" | jq -r '.content[0].text' 2>/dev/null
    CLAUDE_OK=1
  elif echo "$anthro" | grep -q "authentication_error"; then
    echo "   ❌ КЛЮЧ НЕВАЛИДЕН!"
    echo ""
    echo "   🔧 КАК ИСПРАВИТЬ:"
    echo "   1. Открой: https://console.anthropic.com/settings/keys"
    echo "   2. Удали старый ключ (если есть)"
    echo "   3. Нажми 'Create Key'"
    echo "   4. Скопируй ВЕСЬ новый ключ"
    echo "   5. Замени в .env:"
    echo "      ANTHROPIC_API_KEY=\"новый-ключ\""
    echo "   6. Обнови Railway Variables"
    echo "   7. Перезапусти: ./build.sh"
  elif echo "$anthro" | grep -q "insufficient"; then
    echo "   ⚠️  Нет кредитов (нужно $5+)"
    echo "   Пополни: https://console.anthropic.com/settings/billing"
  else
    echo "   ❓ Неизвестная ошибка:"
    echo "$anthro" | jq .
  fi
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# DeepSeek Test
echo "2️⃣ DeepSeek (альтернатива):"
echo "   Используемая модель: deepseek-chat"
echo ""

if [ -z "$DEEPSEEK_API_KEY" ]; then
  echo "   ❌ Ключ пустой!"
  echo "   👉 Получить: https://platform.deepseek.com/api_keys"
else
  echo "   Тестирую ключ: ${DEEPSEEK_API_KEY:0:20}..."

  ds=$(curl -s https://api.deepseek.com/chat/completions \
    -H "Authorization: Bearer $DEEPSEEK_API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"max_tokens":10}')

  if echo "$ds" | grep -q '"content"'; then
    echo "   ✅ РАБОТАЕТ! Можно использовать"
    echo ""
    echo "   Ответ DeepSeek:"
    echo "$ds" | jq -r '.choices[0].message.content' 2>/dev/null
    DEEPSEEK_OK=1
  elif echo "$ds" | grep -q "Insufficient Balance"; then
    echo "   ⚠️  БАЛАНС ИСЧЕРПАН!"
    echo ""
    echo "   🔧 КАК ИСПРАВИТЬ:"
    echo "   1. Открой: https://platform.deepseek.com/account"
    echo "   2. Пополни баланс ($1+ минимум)"
    echo "   3. Обнови Railway Variables если нужно"
  elif echo "$ds" | grep -q "invalid"; then
    echo "   ❌ Ключ невалиден"
    echo "   Создай новый: https://platform.deepseek.com/api_keys"
  else
    echo "   ❓ Неизвестная ошибка:"
    echo "$ds" | jq .
  fi
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📊 ИТОГИ:"
echo ""

if [ -n "$CLAUDE_OK" ]; then
  echo "✅ Anthropic Claude работает → используй LLM_PROVIDER=\"claude\""
  echo "   (Рекомендуется - более мощная модель)"
elif [ -n "$DEEPSEEK_OK" ]; then
  echo "✅ DeepSeek работает → используй LLM_PROVIDER=\"deepseek\""
  echo "   (Работает, но менее мощная)"
else
  echo "❌ Ни один провайдер не работает!"
  echo ""
  echo "🎯 СЛЕДУЮЩИЕ ШАГИ:"
  echo "1. Исправь Anthropic ключ (см. инструкции выше) ← РЕКОМЕНДУЕТСЯ"
  echo "2. ИЛИ пополни DeepSeek баланс"
  echo "3. После исправления запусти: ./fix_and_test.sh"
  echo "4. Обнови Railway Variables"
  echo "5. Перезапусти бота"
fi
echo ""
