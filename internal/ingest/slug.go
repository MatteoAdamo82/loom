package ingest

import (
	"strings"
	"unicode"
)

// Slugify lower-cases text, replaces separator runs with '-', and keeps only
// ASCII letters, digits, and '-'. Unicode letters are transliterated by
// stripping combining marks after NFKD normalization is done on the caller
// side (we keep this dependency-free for MVP).
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case unicode.IsLetter(r):
			// strip diacritics very loosely (not perfect without unicode/norm
			// but fine for MVP entity names)
			mapped := foldLetter(r)
			if mapped != 0 {
				b.WriteRune(mapped)
				prevDash = false
			}
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

func foldLetter(r rune) rune {
	switch r {
	case 'à', 'á', 'â', 'ä', 'ã', 'å', 'ā':
		return 'a'
	case 'è', 'é', 'ê', 'ë', 'ē':
		return 'e'
	case 'ì', 'í', 'î', 'ï', 'ī':
		return 'i'
	case 'ò', 'ó', 'ô', 'ö', 'õ', 'ō', 'ø':
		return 'o'
	case 'ù', 'ú', 'û', 'ü', 'ū':
		return 'u'
	case 'ñ':
		return 'n'
	case 'ç':
		return 'c'
	case 'ß':
		return 's'
	}
	return 0
}
