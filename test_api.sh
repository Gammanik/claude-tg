#!/bin/bash
API_KEY="${ANTHROPIC_API_KEY}"
if [ -z "$API_KEY" ]; then
  echo "Error: ANTHROPIC_API_KEY not set"
  exit 1
fi

curl -s https://api.anthropic.com/v1/messages \
  -H "x-api-key: $API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{
    "model": "claude-opus-4-5-20251101",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "Say hi in 3 words"}]
  }' | jq -r '.content[0].text // .error.message'
