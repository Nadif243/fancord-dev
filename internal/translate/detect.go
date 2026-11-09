// Package translate decides whether a post needs translating and asks Gemini
// for the translation.
package translate

import (
	"regexp"
	"unicode"
)

// Text that is not "writing" in the post's own language: links, @handles and hashtags. A fan-art hashtag in Japanese must not make an Indonesian post look Japanese, so these are taken out before counting.
var noise = regexp.MustCompile(`https?://\S+|[@＠]\w+|[#＃][^\s#＃]+`)

const (
	// A post is Japanese when at least this share of its letters is kana or kanji...
	minShare = 0.3
	// ...and it has at least this many kana. Kanji alone could be Chinese, and one stray kana ("ー", "の") is not a sentence.
	minKana = 2
)

// IsJapanese reports whether the post is written in Japanese and so worth translating. A post in Indonesian or English with a Japanese greeting at the end stays below the share and is left alone.
func IsJapanese(text string) bool {
	var kana, han, letters int
	for _, r := range noise.ReplaceAllString(text, " ") {
		switch {
		case isKana(r):
			kana++
			letters++
		case unicode.Is(unicode.Han, r):
			han++
			letters++
		case unicode.IsLetter(r):
			letters++
		}
	}
	if kana < minKana || letters == 0 {
		return false
	}
	return float64(kana+han)/float64(letters) >= minShare
}

// isKana is hiragana and katakana (with the small extension block and the half-width forms). The long-vowel mark "ー" and the middle dot "・" are left out: they are not letters of a word on their own.
func isKana(r rune) bool {
	switch {
	case r >= 0x3041 && r <= 0x3096: // hiragana
		return true
	case r >= 0x30A1 && r <= 0x30FA: // katakana
		return true
	case r >= 0x31F0 && r <= 0x31FF: // katakana phonetic extensions
		return true
	case r >= 0xFF66 && r <= 0xFF9D: // half-width katakana
		return true
	}
	return false
}
