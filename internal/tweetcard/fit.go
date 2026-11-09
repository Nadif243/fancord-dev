package tweetcard

import (
	"strings"
	"unicode/utf8"
)

// units is the length of s the way Discord limits count it, in UTF-16 units: a character outside the basic plane (most emoji) counts as two.
func units(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// share splits budget among texts of the given lengths so that nobody gets more than they need, and the rest is divided evenly among those that need more than an even split. A short caption is never cut because a long translation sits next to it.
func share(lens []int, budget int) []int {
	out := make([]int, len(lens))
	var active []int
	for i, l := range lens {
		if l > 0 {
			active = append(active, i)
		}
	}
	left := budget
	for len(active) > 0 {
		fair := left / len(active)
		var over []int
		settled := false
		for _, i := range active {
			if lens[i] <= fair {
				out[i] = lens[i]
				left -= lens[i]
				settled = true
			} else {
				over = append(over, i)
			}
		}
		if !settled {
			for _, i := range over {
				out[i] = fair
			}
			return out
		}
		active = over
	}
	return out
}

// cut shortens already escaped text to at most max units, ending in "…". It prefers to cut at a space or a line break near the end, and never leaves a lone backslash that would escape the "…".
func cut(s string, max int) string {
	if units(s) <= max {
		return s
	}
	room := max - 1 // for the "…"
	if room <= 0 {
		return "…"
	}
	n, end := 0, 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > room {
			break
		}
		n += w
		end = i + utf8.RuneLen(r)
	}
	head := s[:end]
	if i := strings.LastIndexAny(head, " \n"); i > 0 && i >= len(head)*4/5 {
		head = head[:i]
	}
	// An odd number of trailing backslashes means an escape was cut in half.
	slashes := len(head) - len(strings.TrimRight(head, `\`))
	if slashes%2 == 1 {
		head = head[:len(head)-1]
	}
	return strings.TrimRight(head, " \n") + "…"
}

// limitLines keeps the first n lines of s. It reports whether lines were dropped.
func limitLines(s string, n int) (string, bool) {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s, false
	}
	return strings.Join(lines[:n], "\n"), true
}
