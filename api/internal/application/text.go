package application

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const zeroWidthJoiner = '‍'

// CleanText trims free text shown to other members (a team name, a rival, a
// place) and checks it has 1 to max characters, not bytes, so accents and ñ
// count as one. It rejects control characters (NUL makes Postgres fail) and
// invisible format characters (bidi overrides, zero-width spaces) that can
// disguise the text in the roster and in pushes. The zero-width joiner
// stays: compound emoji need it (a family emoji is man, ZWJ, woman, ZWJ,
// girl).
func CleanText(raw string, max int) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != zeroWidthJoiner) {
			return "", false
		}
	}
	return s, true
}

// CleanParagraph is CleanText for text that may span lines (a notice body):
// CRLF becomes LF, and line feeds are allowed; every other control or
// invisible character is rejected the same way.
func CleanParagraph(raw string, max int) (string, bool) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if s == "" || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != zeroWidthJoiner) {
			return "", false
		}
	}
	return s, true
}
