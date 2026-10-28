// Package partners knows Yuura's collab partners and spots them in stream titles, for the weekly recap.
//
// The list lives in a CSV file (partners.csv) that is easy to edit in Excel:
//
//		name,kind,also_matches,members
//		@SilviaValleria,partner,silvia|via,
//		Squad Kocak,group,kocak|squad kocak,@SilviaValleria|@naplive7|@figaarfiansyah
//		@yuurayozakura,ignore,,
//
//	  - name: what the recap shows. It is also matched in titles, with or without a leading "@".
//	  - kind: "partner" (the default) is counted and shown in the recap; "group" is a team: naming it in a title (or typing it in /collab) means all of its members; "ignore" is known but never shown (her own handle, say), so it is not reported as an unknown handle either.
//	  - also_matches: other spellings, separated by "|".
//	  - members: for a group only, the partners in it, separated by "|".
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
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type kind int

const (
	kindPartner kind = iota + 1
	kindIgnore
	kindGroup
)

type entry struct {
	name    string
	kind    kind
	tokens  []string // lower-case spellings, whole-word matched
	members []string // groups only: the display names of the partners in it
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

// Groups returns the names of the groups, in file order.
func (b *Book) Groups() []string {
	if b == nil {
		return nil
	}
	var out []string
	for _, e := range b.entries {
		if e.kind == kindGroup {
			out = append(out, e.name)
		}
	}
	return out
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

var knownColumns = []string{"name", "kind", "also_matches", "members"}

// Parse reads the CSV text. All problems are reported together, so the file can be fixed in one go.
func Parse(raw []byte) (*Book, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // Excel's "UTF-8 CSV" starts with a BOM

	r := csv.NewReader(bytes.NewReader(raw))
	r.Comma = delimiter(raw)
	r.FieldsPerRecord = -1 // a short row is reported below, with its line number
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("the file is empty; the first line must be: name,kind,also_matches,members")
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
	seen := map[string]int{}       // lower-case name -> line
	memberLine := map[string]int{} // group name -> line, for the second pass
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
		if strings.Contains(name, ",") {
			problems = append(problems, fmt.Sprintf("line %d (%s): a name cannot contain a comma (commas separate names when typing in /collab)", line, name))
			continue
		}
		e := entry{name: name}
		switch strings.ToLower(cell(rec, "kind")) {
		case "", "partner":
			e.kind = kindPartner
		case "ignore":
			e.kind = kindIgnore
		case "group":
			e.kind = kindGroup
		default:
			problems = append(problems, fmt.Sprintf("line %d (%s): kind must be partner, group or ignore, got %q", line, name, cell(rec, "kind")))
			continue
		}
		if first, dup := seen[strings.ToLower(name)]; dup {
			problems = append(problems, fmt.Sprintf("line %d: %s is already on line %d", line, name, first))
			continue
		}
		seen[strings.ToLower(name)] = line

		members := cell(rec, "members")
		if e.kind != kindGroup && members != "" {
			problems = append(problems, fmt.Sprintf("line %d (%s): only a group can have members", line, name))
		}
		if e.kind == kindGroup {
			for _, m := range strings.Split(members, "|") {
				if m = strings.TrimSpace(m); m != "" {
					e.members = append(e.members, m)
				}
			}
			if len(e.members) == 0 {
				problems = append(problems, fmt.Sprintf("line %d (%s): a group needs members", line, name))
			}
			memberLine[name] = line
		}

		spellings := append([]string{strings.TrimPrefix(name, "@")}, strings.Split(cell(rec, "also_matches"), "|")...)
		for _, s := range spellings {
			t := normalise(s)
			if t == "" {
				continue // "a||b" or a trailing "|"
			}
			if strings.Contains(t, ",") {
				problems = append(problems, fmt.Sprintf("line %d (%s): %q cannot contain a comma", line, name, s))
				continue
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

	// Second pass: a group may list partners that appear further down the file.
	byName := map[string]*entry{}
	for i := range b.entries {
		byName[strings.TrimPrefix(strings.ToLower(b.entries[i].name), "@")] = &b.entries[i]
	}
	for i := range b.entries {
		g := &b.entries[i]
		if g.kind != kindGroup {
			continue
		}
		for j, m := range g.members {
			p := byName[strings.TrimPrefix(strings.ToLower(m), "@")]
			switch {
			case p == nil:
				problems = append(problems, fmt.Sprintf("line %d (%s): member %q is not in the file", memberLine[g.name], g.name, m))
			case p.kind != kindPartner:
				problems = append(problems, fmt.Sprintf("line %d (%s): member %q must be a partner, not a group or an ignore row", memberLine[g.name], g.name, m))
			default:
				g.members[j] = p.name // the display name, however it was typed
			}
		}
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

// Match returns the display names of the partners named in the title, in the order of the file, each once. A group named in the title counts as all of its members; a person who was absent is taken out afterwards by hand.
func (b *Book) Match(title string) []string {
	if b == nil {
		return nil
	}
	text := normalise(title)
	named := map[string]bool{}
	for _, e := range b.entries {
		if e.kind == kindIgnore || !anyWord(text, e.tokens) {
			continue
		}
		if e.kind == kindGroup {
			for _, m := range e.members {
				named[m] = true
			}
		} else {
			named[e.name] = true
		}
	}
	return b.inFileOrder(named)
}

// Resolve reads names typed by a person, such as "dina, kocak": partners (by name, handle or nickname) and groups, separated by commas. A group stands for all its members. It returns the partners in file order, and the terms it did not recognise.
func (b *Book) Resolve(text string) (names, unknown []string) {
	if b == nil {
		return nil, nil
	}
	named := map[string]bool{}
	for _, term := range strings.Split(text, ",") {
		t := normalise(strings.TrimPrefix(strings.TrimSpace(term), "@"))
		if t == "" {
			continue
		}
		found := false
		for _, e := range b.entries {
			if e.kind == kindIgnore || !contains(e.tokens, t) {
				continue
			}
			found = true
			if e.kind == kindGroup {
				for _, m := range e.members {
					named[m] = true
				}
			} else {
				named[e.name] = true
			}
		}
		if !found {
			unknown = append(unknown, strings.TrimSpace(term))
		}
	}
	return b.inFileOrder(named), unknown
}

// inFileOrder lists the partners in the set, in the order of the file.
func (b *Book) inFileOrder(set map[string]bool) []string {
	var out []string
	for _, e := range b.entries {
		if e.kind == kindPartner && set[e.name] {
			out = append(out, e.name)
		}
	}
	return out
}

// Summary shortens a list of partners for reading: a team whose members are all in the list is named once, and the others keep their own names ("Squad Kocak, @DinaMostarterra"). A bigger team is named before a smaller one, and a team whose members are already covered by another is left out. Teams come first, then the remaining people in the order they were given.
func (b *Book) Summary(names []string) []string {
	if b == nil {
		return append([]string(nil), names...)
	}
	present := map[string]bool{}
	for _, n := range names {
		present[n] = true
	}
	var teams []entry
	for _, e := range b.entries {
		if e.kind == kindGroup && allIn(e.members, present) {
			teams = append(teams, e)
		}
	}
	sort.SliceStable(teams, func(i, j int) bool { return len(teams[i].members) > len(teams[j].members) })

	covered := map[string]bool{}
	var out []string
	for _, g := range teams {
		if allIn(g.members, covered) {
			continue // everyone in it is already named through a bigger team
		}
		out = append(out, g.name)
		for _, m := range g.members {
			covered[m] = true
		}
	}
	for _, n := range names {
		if !covered[n] {
			out = append(out, n)
		}
	}
	return out
}

// allIn reports whether every name is in the set (an empty list is not "all").
func allIn(names []string, set map[string]bool) bool {
	for _, n := range names {
		if !set[n] {
			return false
		}
	}
	return len(names) > 0
}

// Suggestion is a partner or a group that can be typed in /collab.
type Suggestion struct {
	Name    string
	Members int // 0 for a partner; the number of members for a group
}

// Suggest lists the partners and groups whose name or spellings contain the typed text (capitals and a leading "@" do not matter; empty means all), for autocomplete. Groups come first, then partners in the order of the file.
func (b *Book) Suggest(typed string, n int) []Suggestion {
	if b == nil || n <= 0 {
		return nil
	}
	q := normalise(strings.TrimPrefix(strings.TrimSpace(typed), "@"))
	var groups, people []Suggestion
	for _, e := range b.entries {
		if e.kind == kindIgnore || !(q == "" || anySubstring(e.tokens, q)) {
			continue
		}
		if e.kind == kindGroup {
			groups = append(groups, Suggestion{Name: e.name, Members: len(e.members)})
		} else {
			people = append(people, Suggestion{Name: e.name})
		}
	}
	out := append(groups, people...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func anySubstring(tokens []string, q string) bool {
	for _, t := range tokens {
		if strings.Contains(t, q) {
			return true
		}
	}
	return false
}

// Has reports whether name is the display name of a partner on the list.
func (b *Book) Has(name string) bool {
	if b == nil {
		return false
	}
	for _, e := range b.entries {
		if e.kind == kindPartner && e.name == name {
			return true
		}
	}
	return false
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

func anyWord(text string, tokens []string) bool {
	for _, t := range tokens {
		if hasWord(text, t) {
			return true
		}
	}
	return false
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
