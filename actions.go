package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

type directAction struct {
	kind string
	arg  string
}

func detectDirectAction(text string) *directAction {
	lower := strings.ToLower(text)
	avatarKws := []string{"аватарк", "аву", "аватар", "фото бота", "avatar", "photo"}
	changeKws := []string{"поменяй", "смени", "измени", "поставь", "set", "change", "update", "ко мне"}
	if containsAny(lower, avatarKws...) && containsAny(lower, changeKws...) {
		return &directAction{kind: "set_avatar"}
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

// setBotAvatar — ставит аватарку боту
// Порядок попыток: DALL-E → dicebear → встроенная PNG
func (b *Bot) setBotAvatar() error {
	// 1. DALL-E если есть ключ
	if b.cfg.OpenAIKey != "" {
		if imgURL, err := b.generateAvatarDALLE(); err == nil {
			if err := b.uploadAvatarFromURL(imgURL); err == nil {
				return nil
			}
		}
	}

	// 2. Dicebear
	dicebearURL := "https://api.dicebear.com/9.x/bottts-neutral/png?seed=claude-tg&backgroundColor=1a1a2e&size=512"
	if err := b.uploadAvatarFromURL(dicebearURL); err == nil {
		return nil
	}

	// 3. Fallback — генерируем PNG прямо в Go (без внешних зависимостей)
	imgData := generateDefaultAvatar()
	return b.uploadAvatarBytes(imgData, "avatar.png", "image/png")
}

// generateDefaultAvatar — рисует простую аватарку в памяти
func generateDefaultAvatar() []byte {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Тёмно-синий фон
	bg := color.RGBA{R: 26, G: 26, B: 46, A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	// Фиолетовый круг по центру
	purple := color.RGBA{R: 124, G: 58, B: 237, A: 255}
	cx, cy, r := size/2, size/2, size/3
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.Set(x, y, purple)
			}
		}
	}

	// Светлая точка (глаз)
	white := color.RGBA{R: 220, G: 220, B: 255, A: 255}
	er := size / 10
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-cx, y-cy-size/12
			if dx*dx+dy*dy <= er*er {
				img.Set(x, y, white)
			}
		}
	}

	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func (b *Bot) generateAvatarDALLE() (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":   "dall-e-3",
		"prompt":  "Minimalist robot face avatar, dark blue background, purple glowing eye, circuit pattern, clean geometric design for a Telegram bot, square format",
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
		return "", fmt.Errorf("no image")
	}
	return result.Data[0].URL, nil
}

func (b *Bot) uploadAvatarFromURL(imgURL string) error {
	resp, err := http.Get(imgURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	imgData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(imgData) < 1000 {
		return fmt.Errorf("image too small (%d bytes)", len(imgData))
	}

	ct := resp.Header.Get("Content-Type")
	ext := "jpg"
	if strings.Contains(ct, "png") || strings.HasSuffix(imgURL, ".png") {
		ext = "png"
		ct = "image/png"
	} else {
		ct = "image/jpeg"
	}

	return b.uploadAvatarBytes(imgData, "avatar."+ext, ct)
}

func (b *Bot) uploadAvatarBytes(imgData []byte, filename, contentType string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Создаём part с правильным content-type
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="photo"; filename="%s"`, filename)}
	h["Content-Type"] = []string{contentType}
	fw, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	fw.Write(imgData)
	w.Close()

	// Пробуем setMyPhoto (Bot API 7.3+)
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/setMyPhoto", b.cfg.TelegramToken)
	req, _ := http.NewRequest("POST", apiURL, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if !result.OK {
		// setMyPhoto не поддерживается — говорим пользователю как поставить вручную
		if result.ErrorCode == 404 {
			return fmt.Errorf("setMyPhoto требует Bot API 7.3+.\n\nПоставь вручную:\n@BotFather → /setuserpic → @%s → загрузи фото", "claude_gammabot")
		}
		return fmt.Errorf("%s", result.Description)
	}
	return nil
}

func extractQuoted(text string) string {
	if i := strings.Index(text, `"`); i >= 0 {
		if j := strings.Index(text[i+1:], `"`); j >= 0 {
			return text[i+1 : i+1+j]
		}
	}
	return ""
}
