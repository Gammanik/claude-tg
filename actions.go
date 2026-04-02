package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
)

// directAction — вещи которые бот делает сам, без GitHub PR
type directAction struct {
	kind string // "set_avatar" | "set_name" | "set_description"
	arg  string
}

func detectDirectAction(text string) *directAction {
	lower := strings.ToLower(text)

	// Аватарка
	avatarKws := []string{"аватарк", "аву", "аватар", "фото бота", "avatar", "photo"}
	changeKws := []string{"поменяй", "смени", "измени", "поставь", "set", "change", "update", "ко мне"}
	isAvatar := containsAny(lower, avatarKws...)
	isChange := containsAny(lower, changeKws...)
	if isAvatar && isChange {
		return &directAction{kind: "set_avatar"}
	}

	// Имя бота
	if containsAny(lower, "имя бота", "название бота", "переименуй бота", "bot name") &&
		containsAny(lower, "поменяй", "смени", "измени", "set", "change") {
		return &directAction{kind: "set_name", arg: extractQuoted(text)}
	}

	return nil
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func extractQuoted(text string) string {
	if i := strings.Index(text, `"`); i >= 0 {
		if j := strings.Index(text[i+1:], `"`); j >= 0 {
			return text[i+1 : i+1+j]
		}
	}
	return ""
}

// setBotAvatar — генерирует аватарку через DALL-E или берёт дефолтную, ставит боту
func (b *Bot) setBotAvatar() error {
	// Если есть OpenAI — генерируем через DALL-E
	if b.cfg.OpenAIKey != "" {
		imgURL, err := b.generateAvatarDALLE()
		if err == nil {
			return b.uploadAvatarFromURL(imgURL)
		}
		log.Printf("DALL-E failed: %v, using default", err)
	}

	// Дефолт: красивая геометрическая аватарка (SVG → PNG через placeholder)
	defaultURL := "https://api.dicebear.com/9.x/bottts-neutral/png?seed=claude-tg&backgroundColor=1a1a2e&size=512"
	return b.uploadAvatarFromURL(defaultURL)
}

func (b *Bot) generateAvatarDALLE() (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":   "dall-e-3",
		"prompt":  "A sleek dark blue robot face with glowing purple circuit patterns, minimal design, clean background, suitable for a Telegram bot avatar",
		"n":       1,
		"size":    "1024x1024",
		"quality": "standard",
	})
	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+b.cfg.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Data) == 0 {
		return "", fmt.Errorf("no image generated")
	}
	return result.Data[0].URL, nil
}

func (b *Bot) uploadAvatarFromURL(imgURL string) error {
	// Скачиваем изображение
	resp, err := http.Get(imgURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	imgData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Определяем тип файла
	contentType := resp.Header.Get("Content-Type")
	ext := "jpg"
	if strings.Contains(contentType, "png") || strings.HasSuffix(imgURL, ".png") {
		ext = "png"
	}

	// Загружаем через setMyPhoto (Bot API 7.0+)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("photo", "avatar."+ext)
	fw.Write(imgData)
	w.Close()

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/setMyPhoto", b.cfg.TelegramToken)
	req, _ := http.NewRequest("POST", apiURL, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	apiResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer apiResp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.NewDecoder(apiResp.Body).Decode(&result)
	if !result.OK {
		return fmt.Errorf("Telegram API: %s", result.Description)
	}
	return nil
}
