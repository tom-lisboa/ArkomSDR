package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var listLine = regexp.MustCompile(`(?m)^\s*(?:[-*•]|[0-9]+[.)])\s`)

// SplitReply preserves words, links and structured lists. Small replies remain
// one message; longer prose becomes at most three independently delivered parts.
func SplitReply(text string) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}
	if utf8.RuneCountInString(text) <= 180 || listLine.MatchString(text) {
		return []string{text}
	}
	var units []string
	for _, p := range strings.Split(text, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			units = append(units, p)
		}
	}
	if len(units) == 1 {
		// Preserve multiline formatting when it isn't paragraph prose.
		if strings.Contains(text, "\n") {
			return []string{text}
		}
		units = nil
		var words []string
		length := 0
		for _, word := range strings.Fields(text) {
			words = append(words, word)
			length += utf8.RuneCountInString(word) + 1
			if length >= 180 && (strings.ContainsAny(word[len(word)-1:], ".!?") || length >= 300) {
				units = append(units, strings.Join(words, " "))
				words = nil
				length = 0
			}
		}
		if len(words) > 0 {
			units = append(units, strings.Join(words, " "))
		}
	}
	// Keep the final question with its context if it is very short.
	if len(units) > 1 && utf8.RuneCountInString(units[len(units)-1]) < 35 {
		n := len(units)
		units[n-2] += "\n\n" + units[n-1]
		units = units[:n-1]
	}
	if len(units) > 3 {
		units = append(units[:2], strings.Join(units[2:], "\n\n"))
	}
	return units
}

// A short reading pause; persisted as job due_at, never a worker sleep.
func ReplyPause(next string) time.Duration {
	return time.Duration(2+min(2, utf8.RuneCountInString(next)/120)) * time.Second
}
