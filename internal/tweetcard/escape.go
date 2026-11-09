package tweetcard

import (
	"regexp"
	"strings"
)

// A link inside the text. Links are left as they are (an underscore in a link must stay an underscore) except for the few characters that could still start formatting.
var link = regexp.MustCompile(`https?://\S+`)

var linkSafe = strings.NewReplacer(
	"|", "%7C", "`", "%60", "<", "%3C", ">", "%3E", `\`, "%5C",
)

// formatting are the characters Discord reads as formatting, a mention, a timestamp or a link when they stand in text. "#" is here for headers.
const formatting = "\\*_~|`<>[]@#"

// listStart matches what begins a list item at the start of a line: "- " or "1. ", after any spaces. Group 2 is the marker.
var listStart = regexp.MustCompile(`^( *)(-|\d{1,9}\.) `)

// escapeText makes text from X show exactly as written in a Discord message:
// no bold, no hidden spoilers, no mentions, no headers, no lists, no masked links. Line breaks are kept; links are left working.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = escapeLine(l)
	}
	return strings.Join(lines, "\n")
}

func escapeLine(l string) string {
	head := ""
	if m := listStart.FindStringSubmatchIndex(l); m != nil {
		marker := l[m[4]:m[5]]
		// "- " becomes "\- " and "1. " becomes "1\. ": shown as typed, not a list.
		marker = strings.TrimSuffix(marker, ".")
		if marker == "-" {
			head = l[:m[4]] + `\- `
		} else {
			head = l[:m[4]] + marker + `\. `
		}
		l = l[m[1]:]
	}
	var out strings.Builder
	last := 0
	for _, loc := range link.FindAllStringIndex(l, -1) {
		out.WriteString(escapeRun(l[last:loc[0]]))
		out.WriteString(linkSafe.Replace(l[loc[0]:loc[1]]))
		last = loc[1]
	}
	out.WriteString(escapeRun(l[last:]))
	return head + out.String()
}

func escapeRun(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(formatting, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
