package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

type Voice struct {
	cfg Config
}

func NewVoice(cfg Config) *Voice {
	return &Voice{cfg: cfg}
}

// Transcribe — STT через Groq Whisper (бесплатно, быстро)
// Fallback на OpenAI Whisper если нет Groq ключа
func (v *Voice) Transcribe(fileURL string) (string, error) {
	// Скачиваем OGG файл
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("download voice: %w", err)
	}
	defer resp.Body.Close()
	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if v.cfg.GroqKey != "" {
		return v.transcribeGroq(audioData)
	}
	if v.cfg.OpenAIKey != "" {
		return v.transcribeOpenAI(audioData)
	}
	return "", fmt.Errorf("no STT API key configured (set GROQ_API_KEY or OPENAI_API_KEY)")
}

// Groq Whisper — бесплатный тир, очень быстрый
func (v *Voice) transcribeGroq(audio []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Файл
	fw, _ := w.CreateFormFile("file", "voice.ogg")
	fw.Write(audio)

	// Параметры
	w.WriteField("model", "whisper-large-v3-turbo")
	w.WriteField("language", "ru") // авто-определение если убрать
	w.WriteField("response_format", "text")
	w.Close()

	req, _ := http.NewRequest("POST", "https://api.groq.com/openai/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer "+v.cfg.GroqKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Groq STT %d: %s", resp.StatusCode, string(body))
	}
	// response_format=text возвращает просто текст
	return string(bytes.TrimSpace(body)), nil
}

// OpenAI Whisper — платный но дёшево ($0.006/мин)
func (v *Voice) transcribeOpenAI(audio []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fw, _ := w.CreateFormFile("file", "voice.ogg")
	fw.Write(audio)
	w.WriteField("model", "whisper-1")
	w.WriteField("language", "ru")
	w.Close()

	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer "+v.cfg.OpenAIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Text  string                    `json:"text"`
		Error *struct{ Message string } `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Error != nil {
		return "", fmt.Errorf("OpenAI STT: %s", result.Error.Message)
	}
	return result.Text, nil
}

// Synthesize — TTS через OpenAI (возвращает OGG для отправки в Telegram)
// Голос "nova" — нейтральный, чёткий
func (v *Voice) Synthesize(text string) ([]byte, error) {
	if v.cfg.OpenAIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}

	// Укорачиваем для TTS — длинный текст не нужен
	if len(text) > 500 {
		text = text[:500] + "..."
	}

	body, _ := json.Marshal(map[string]any{
		"model":           "tts-1", // tts-1-hd для лучшего качества
		"input":           text,
		"voice":           "nova", // alloy, echo, fable, onyx, nova, shimmer
		"response_format": "opus", // OGG Opus — нативный формат Telegram
	})

	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/audio/speech", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+v.cfg.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TTS %d: %s", resp.StatusCode, string(b))
	}

	return io.ReadAll(resp.Body)
}
