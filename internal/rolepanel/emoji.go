package rolepanel

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var customEmoji = regexp.MustCompile(`^<(a?):([A-Za-z0-9_]{2,32}):([0-9]{17,20})>$`)

// Emoji is an emoji as Discord wants it on a button: a plain emoji has only a Name (the emoji itself); a server's own emoji also has an ID.
type Emoji struct {
	Name     string
	ID       string
	Animated bool
}

// ParseEmoji reads an emoji as a person types it: the emoji itself ("🎨") or a server emoji as Discord writes it in a message ("<:name:123456789012345678>").
// ok is false for anything else, so a typo is caught when it is typed and not when Discord refuses the button.
func ParseEmoji(text string) (Emoji, bool) {
	text = strings.TrimSpace(text)
	if m := customEmoji.FindStringSubmatch(text); m != nil {
		return Emoji{Name: m[2], ID: m[3], Animated: m[1] == "a"}, true
	}
	if text == "" || utf8.RuneCountInString(text) > 16 {
		return Emoji{}, false
	}
	wide := false
	for _, r := range text {
		switch {
		case r == ' ' || r == '\t' || r == '\n':
			return Emoji{}, false
		case r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			return Emoji{}, false // a word, not an emoji
		case r >= 0x80:
			wide = true
		}
	}
	if !wide {
		return Emoji{}, false
	}
	return Emoji{Name: text}, true
}

const (
	zwj        = '\u200d' // joins the parts of one emoji: 🧑‍🍳 is 🧑 + ZWJ + 🍳
	vs16       = '\ufe0f' // asks for the emoji look of the symbol before it
	keycapMark = '\u20e3' // turns 1 into 1️⃣
)

// emojiRanges are the characters that are an emoji by themselves. Symbols that are an emoji only when U+FE0F follows (☪️ ❤️ ✔️) are caught by that selector instead, so plain decoration such as ★ ✦ ♡ stays in a name.
var emojiRanges = [...][2]rune{
	{0x231A, 0x231B}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0}, {0x23F3, 0x23F3},
	{0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F},
	{0x2693, 0x2693}, {0x26A1, 0x26A1}, {0x26AA, 0x26AB}, {0x26BD, 0x26BE},
	{0x26C4, 0x26C5}, {0x26CE, 0x26CE}, {0x26D4, 0x26D4}, {0x26EA, 0x26EA},
	{0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA}, {0x26FD, 0x26FD},
	{0x2705, 0x2705}, {0x270A, 0x270B}, {0x2728, 0x2728}, {0x274C, 0x274C},
	{0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50},
	{0x2B55, 0x2B55},
	{0x1F004, 0x1F004}, {0x1F0CF, 0x1F0CF}, {0x1F18E, 0x1F18E}, {0x1F191, 0x1F19A},
	{0x1F1E6, 0x1F1FF}, // flags are two of these
	{0x1F201, 0x1F201}, {0x1F21A, 0x1F21A}, {0x1F22F, 0x1F22F},
	{0x1F232, 0x1F236}, {0x1F238, 0x1F23A}, {0x1F250, 0x1F251},
	{0x1F300, 0x1FAFF}, // pictographs, faces, transport, food, skin tones, and the newer blocks
}

func isEmojiRune(r rune) bool {
	for _, rg := range emojiRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// DisplayName is a role's name as the panel shows it: without any emoji, because the panel puts the role's own emoji in front of it ("🎨 = Tukang Gambar").
// "🎨 Tukang Gambar" becomes "Tukang Gambar"; a name with no emoji comes back exactly as it was, and so does a name that is nothing but emoji.
func DisplayName(name string) string {
	rs := []rune(name)
	out := make([]rune, 0, len(rs))
	found := false
	for i := 0; i < len(rs); {
		if n := emojiLen(rs, i); n > 0 {
			found = true
			i += n
			continue
		}
		out = append(out, rs[i])
		i++
	}
	if !found {
		return name
	}
	clean := strings.Join(strings.Fields(string(out)), " ")
	if clean == "" {
		return name
	}
	return clean
}

// emojiLen is how many runes the emoji that starts at rs[i] takes, or 0 when none starts there. A ZWJ sequence (👨‍👩‍👧, 🧑‍🍳) counts as one emoji.
func emojiLen(rs []rune, i int) int {
	end := scanEmoji(rs, i)
	if end == i {
		return 0
	}
	for end+1 < len(rs) && rs[end] == zwj {
		next := scanEmoji(rs, end+1)
		if next == end+1 {
			break
		}
		end = next
	}
	return end - i
}

// scanEmoji reads one emoji with whatever sticks to it (variation selector, keycap mark, tag characters of a subdivision flag) and returns where it ends; it returns i when no emoji starts at rs[i].
func scanEmoji(rs []rune, i int) int {
	r := rs[i]
	j := i + 1
	switch {
	case r == '#' || r == '*' || r >= '0' && r <= '9':
		// A digit is an emoji only as a keycap: 1, optional U+FE0F, U+20E3.
		k := j
		if k < len(rs) && rs[k] == vs16 {
			k++
		}
		if k < len(rs) && rs[k] == keycapMark {
			return k + 1
		}
		return i
	case isEmojiRune(r):
	case r > 0x7f && j < len(rs) && rs[j] == vs16:
		// a text symbol made into an emoji by the selector after it
	default:
		return i
	}
	for j < len(rs) && (rs[j] == vs16 || rs[j] == keycapMark || rs[j] >= 0xE0020 && rs[j] <= 0xE007F) {
		j++
	}
	return j
}
