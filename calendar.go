package main

import (
	"fmt"
	"net/url"
	"time"
)

// CalendarEvent — событие которое бот добавляет после завершения задачи
type CalendarEvent struct {
	Title       string
	Description string
	StartTime   time.Time
	Duration    time.Duration
	PRUrl       string
}

// GoogleCalendarLink — генерирует ссылку для добавления события одним кликом
// Не требует OAuth, работает сразу
func GoogleCalendarLink(ev CalendarEvent) string {
	start := ev.StartTime.UTC().Format("20060102T150405Z")
	end := ev.StartTime.Add(ev.Duration).UTC().Format("20060102T150405Z")

	desc := ev.Description
	if ev.PRUrl != "" {
		desc += "\n\nPR: " + ev.PRUrl
	}

	params := url.Values{
		"action":  {"TEMPLATE"},
		"text":    {ev.Title},
		"dates":   {start + "/" + end},
		"details": {desc},
		"sf":      {"true"},
		"output":  {"xml"},
	}
	return "https://calendar.google.com/calendar/render?" + params.Encode()
}

// ICSContent — генерирует .ics файл (открывается в любом календаре)
func ICSContent(ev CalendarEvent) []byte {
	start := ev.StartTime.UTC().Format("20060102T150405Z")
	end := ev.StartTime.Add(ev.Duration).UTC().Format("20060102T150405Z")
	now := time.Now().UTC().Format("20060102T150405Z")

	desc := ev.Description
	if ev.PRUrl != "" {
		desc += "\\nPR: " + ev.PRUrl
	}

	content := fmt.Sprintf(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//claude-tg//bot//EN
BEGIN:VEVENT
DTSTART:%s
DTEND:%s
DTSTAMP:%s
SUMMARY:%s
DESCRIPTION:%s
URL:%s
END:VEVENT
END:VCALENDAR`, start, end, now, ev.Title, desc, ev.PRUrl)

	return []byte(content)
}

// buildTaskEvent — создаёт событие для завершённой задачи
func buildTaskEvent(taskDesc, owner, repo, prURL string, taskDuration time.Duration) CalendarEvent {
	title := fmt.Sprintf("✅ %s [%s/%s]", truncate(taskDesc, 50), owner, repo)
	desc := fmt.Sprintf("Задача выполнена ботом claude-tg\nРепо: %s/%s\nВремя: %s",
		owner, repo, fmtDuration(taskDuration))

	return CalendarEvent{
		Title:       title,
		Description: desc,
		StartTime:   time.Now(),
		Duration:    30 * time.Minute, // событие на 30 мин как заметка
		PRUrl:       prURL,
	}
}
