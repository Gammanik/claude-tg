#!/bin/bash
# Тест Anthropic API ключа

source .env

echo "🧪 Тестирую Anthropic API ключ..."
echo ""

response=$(curl -s https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{
    "model": "claude-haiku-4-5-20251001",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "Say hi"}]
  }')

if echo "$response" | grep -q '"content"'; then
  echo "✅ API ключ работает!"
  echo ""
  echo "Ответ Claude:"
  echo "$response" | jq -r '.content[0].text' 2>/dev/null || echo "$response"
elif echo "$response" | grep -q "invalid_api_key"; then
  echo "❌ Неправильный API ключ"
  echo ""
  echo "Проверь ключ на: https://console.anthropic.com/settings/keys"
  echo "Создай новый ключ если нужно"
elif echo "$response" | grep -q "permission"; then
  echo "❌ Нет доступа к API"
  echo ""
  echo "Возможно нужно:"
  echo "1. Добавить способ оплаты: https://console.anthropic.com/settings/billing"
  echo "2. Или подождать активации аккаунта"
else
  echo "❓ Неожиданный ответ:"
  echo "$response"
fi
