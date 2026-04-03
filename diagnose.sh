#!/bin/bash
source .env

echo "🔍 Диагностика API ключей..."
echo ""

# Проверка Anthropic
echo "1️⃣ Anthropic Claude:"
if [ -z "$ANTHROPIC_API_KEY" ]; then
  echo "   ❌ Ключ не установлен"
else
  echo "   📝 Ключ: ${ANTHROPIC_API_KEY:0:20}..."
  anthro_test=$(curl -s https://api.anthropic.com/v1/messages \
    -H "x-api-key: $ANTHROPIC_API_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" \
    -d '{"model":"claude-haiku-4-5-20251001","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}')
  
  if echo "$anthro_test" | grep -q '"content"'; then
    echo "   ✅ Работает!"
  elif echo "$anthro_test" | grep -q "invalid_api_key"; then
    echo "   ❌ Неправильный ключ"
  elif echo "$anthro_test" | grep -q "insufficient_credits"; then
    echo "   ⚠️  Нет кредитов (добавь $5+)"
  else
    echo "   ❓ Ошибка: $(echo $anthro_test | jq -r '.error.message' 2>/dev/null || echo 'unknown')"
  fi
fi
echo ""

# Проверка DeepSeek
echo "2️⃣ DeepSeek:"
if [ -z "$DEEPSEEK_API_KEY" ]; then
  echo "   ❌ Ключ не установлен"
else
  echo "   📝 Ключ: ${DEEPSEEK_API_KEY:0:20}..."
  ds_test=$(curl -s https://api.deepseek.com/chat/completions \
    -H "Authorization: Bearer $DEEPSEEK_API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"max_tokens":10}')
  
  if echo "$ds_test" | grep -q '"content"'; then
    echo "   ✅ Работает!"
  elif echo "$ds_test" | grep -q "Insufficient Balance"; then
    echo "   ⚠️  Нет баланса (добавь $1+)"
  elif echo "$ds_test" | grep -q "invalid"; then
    echo "   ❌ Неправильный ключ"
  else
    echo "   ❓ Ошибка: $(echo $ds_test | jq -r '.error.message' 2>/dev/null || echo 'unknown')"
  fi
fi
echo ""

# Рекомендации
echo "📋 Рекомендации:"
echo ""
if echo "$anthro_test" | grep -q '"content"'; then
  echo "✅ Используй Claude: LLM_PROVIDER=\"claude\""
elif echo "$ds_test" | grep -q '"content"'; then
  echo "✅ Используй DeepSeek: LLM_PROVIDER=\"deepseek\""
else
  echo "⚠️  Ни один ключ не работает!"
  echo ""
  echo "Варианты:"
  echo "1. Anthropic: добавь кредиты ($5) → console.anthropic.com/settings/billing"
  echo "2. DeepSeek: добавь кредиты ($1) → platform.deepseek.com/account"
  echo "3. Или используй Ollama локально (бесплатно)"
fi
