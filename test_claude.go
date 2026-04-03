package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Println("❌ ANTHROPIC_API_KEY not set")
		os.Exit(1)
	}

	fmt.Printf("🔑 Testing API key: %s...%s\n", apiKey[:15], apiKey[len(apiKey)-10:])

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-4-5-20251101",
		"max_tokens": 100,
		"messages":   []map[string]string{{"role": "user", "content": "Say hi in 3 words"}},
	})

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	fmt.Println("📡 Sending request to Anthropic API...")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Request error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	fmt.Printf("\n📥 Response (status %d):\n", resp.StatusCode)

	var result map[string]any
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		fmt.Printf("Raw: %s\n", string(bodyBytes))
		os.Exit(1)
	}

	prettyJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(prettyJSON))

	if resp.StatusCode == 200 {
		if content, ok := result["content"].([]any); ok && len(content) > 0 {
			if textBlock, ok := content[0].(map[string]any); ok {
				if text, ok := textBlock["text"].(string); ok {
					fmt.Printf("\n✅ API works! Response: %s\n", text)
					os.Exit(0)
				}
			}
		}
	}

	if errObj, ok := result["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok {
			fmt.Printf("\n❌ API Error: %s\n", msg)
		}
	}

	fmt.Println("\n💡 Get a valid API key at: https://console.anthropic.com/settings/keys")
	os.Exit(1)
}
