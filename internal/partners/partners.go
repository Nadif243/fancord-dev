// Package partners knows Yuura's collab partners and spots them in stream titles, for the weekly recap.
//
// The list lives in a CSV file (partners.csv) that is easy to edit in Excel:
//
//		name,kind,also_matches
//		@SilviaValleria,partner,silvia|sylvia|kocak
//		@yuurayozakura,ignore,
//
//	  - name: what the recap shows. It is also matched in titles, with orwithou t a leading "@".
//	  - kind: "partner" (the default) is counted and shown in the recap;
//	    "ignore" is known but never shown (her own handle, say), so it is not reported as an unknown handle either.
//	  - also_matches: other spellings, separated by "|".
//
// A spelling matches only as a whole word, ignoring capitals: "dina" matches in "Main bareng Dina!" and in "@Dina", but not inside "@DinaMostarterra" (that one is matched by its own name) or in "dinamit".
package partners

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type kind int

const (
	kindPartner kind = iota + 1
	kindIgnore
)

type entry struct {
	name   string
	kind   kind
	tokens []string // lower-case spellings, whole-word matched
}

// Book is the loaded list. A nil *Book is a valid, empty list: it matches nothing, so the recap simply has no collab line.
type Book struct {
	entries []entry
	known   map[string]bool // every spelling of every entry, for Unknown
}

// Len is the number of partners that are counted in the recap.
func (b *Book) Len() int {
	if b == nil {
		return 0
	}
	n := 0
	for _, e := range b.entries {
		if e.kind == kindPartner {
			n++
		}
	}
	return n
}

// Load reads the file. The error wraps fs.ErrNotExist when there is no file, so the caller can treat that as "feature off".
func Load(path string) (*Book, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read partners file: %w", err)
	}
	b, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("partners file %s: %w", path, err)
	}
	return b, nil
}

var knownColumns = []string{"name", "kind", "also_matches"}

// Parse reads the CSV text. All problems are reported together, so the file can be fixed in one go.
func Parse(raw []byte) (*Book, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // Excel's "UTF-8 CSV" starts with a BOM

	r := csv.NewReader(bytes.NewReader(raw))
	r.Comma = delimiter(raw)
	r.FieldsPerRecord = -1 // a short row is reported below, with its line number
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("the file is empty; the first line must be: name,kind,also_matches")
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the first line: %w", err)
	}

	col := map[string]int{}
	var problems []string
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		if !contains(knownColumns, h) {
			problems = append(problems, fmt.Sprintf("unknown column %q (allowed: %s)", h, strings.Join(knownColumns, ", ")))
			continue
		}
		col[h] = i
	}
	if _, ok := col["name"]; !ok {
		problems = append(problems, `there is no "name" column`)
	}
	if len(problems) > 0 {
		return nil, joinProblems(problems)
	}
	cell := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	b := &Book{known: map[string]bool{}}
	seen := map[string]int{} // lower-case name -> line
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := r.FieldPos(0)
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		if blank(rec) {
			continue // Excel often leaves rows of empty commas at the bottom
		}

		name := cell(rec, "name")
		if name == "" {
			problems = append(problems, fmt.Sprintf("line %d: the name is empty", line))
			continue
		}
		e := entry{name: name}
		switch strings.ToLower(cell(rec, "kind")) {
		case "", "partner":
			e.kind = kindPartner
		case "ignore":
			e.kind = kindIgnore
		default:
			problems = append(problems, fmt.Sprintf("line %d (%s): kind must be partner or ignore, got %q", line, name, cell(rec, "kind")))
			continue
		}
		if first, dup := seen[strings.ToLower(name)]; dup {
			problems = append(problems, fmt.Sprintf("line %d: %s is already on line %d", line, name, first))
			continue
		}
		seen[strings.ToLower(name)] = line

		spellings := append([]string{strings.TrimPrefix(name, "@")}, strings.Split(cell(rec, "also_matches"), "|")...)
		for _, s := range spellings {
			t := normalise(s)
			if t == "" {
				continue // "a||b" or a trailing "|"
			}
			if utf8.RuneCountInString(t) < 2 || !hasLetterOrDigit(t) {
				problems = append(problems, fmt.Sprintf("line %d (%s): %q is too short to match safely", line, name, s))
				continue
			}
			if !contains(e.tokens, t) {
				e.tokens = append(e.tokens, t)
				b.known[t] = true
			}
		}
		b.entries = append(b.entries, e)
	}
	if len(problems) > 0 {
		return nil, joinProblems(problems)
	}
	return b, nil
}

// delimiter picks the separator from the first line. Excel with Indonesian (and many European) regional settings saves CSV files with ";" instead of ",".
func delimiter(raw []byte) rune {
	first, _, _ := bytes.Cut(raw, []byte("\n"))
	if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		return ';'
	}
	return ','
}

// Match returns the display names of the partners named in the title, in the order of the file, each once.
func (b *Book) Match(title string) []string {
	if b == nil {
		return nil
	}
	text := normalise(title)
	var out []string
	for _, e := range b.entries {
		if e.kind != kindPartner {
			continue
		}
		for _, t := range e.tokens {
			if hasWord(text, t) {
				out = append(out, e.name)
				break
			}
		}
	}
	return out
}

// atHandle finds "@something" that is not the tail of a longer word (so an e-mail address is not mistaken for a handle).
var atHandle = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_])@([A-Za-z0-9_][A-Za-z0-9_.-]{2,})`)

// Unknown returns the @handles in the title that are not on the list at all: likely collab partners the list is missing. Each is returned as written.
func (b *Book) Unknown(title string) []string {
	if b == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{} // handles are not case-sensitive on YouTube
	for _, m := range atHandle.FindAllStringSubmatch(title, -1) {
		h := strings.TrimRight(m[1], ".-")
		key := strings.ToLower(h)
		if utf8.RuneCountInString(h) < 3 || b.known[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, h)
	}
	return out
}

// normalise lower-cases and puts the text on one line with single spaces.
func normalise(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// hasWord reports whether tok occurs in text as a whole word.
func hasWord(text, tok string) bool {
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], tok)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(tok)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if !(start > 0 && isWordRune(before)) && !(end < len(text) && isWordRune(after)) {
			return true
		}
		_, n := utf8.DecodeRuneInString(text[start:])
		from = start + n
	}
	return false
}

// isWordRune says whether r is part of a word. Only Latin letters count: a name written in Latin letters right next to Japanese text ("Dinaとコラボ") is still a separate word, and Japanese has no spaces to tell words apart.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsDigit(r) || unicode.IsMark(r) || unicode.Is(unicode.Latin, r)
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func blank(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func joinProblems(p []string) error {
	return fmt.Errorf("%d problem(s):\n  - %s", len(p), strings.Join(p, "\n  - "))
}
