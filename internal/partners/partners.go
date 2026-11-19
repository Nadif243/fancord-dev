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
//	  - kind: "partner" (the default) is counted and shown in the recap; "group" is a team: naming it in a title (or typing it in /collab) means all of its members; "duo" is a team that is only ever named when the title says its name, and the recap writes it alone, without "bareng" (a two-person team such as YuuGuRe); "ignore" is known but never shown (her own handle, say), so it is not reported as an unknown handle either.
//	  - also_matches: other spellings, separated by "|".
//	  - members: for a group or duo only, the partners in it, separated by "|".
//
// A spelling matches only as a whole word, ignoring capitals: "dina" matches in "Main bareng Dina!" and in "@Dina", but not inside "@DinaMostarterra" (that one is matched by its own name) or in "dinamit".
//
// Two partners may share a spelling (a nickname that is both people's). On its own that spelling names nobody: the title does not say which one, so Match leaves both out and Ambiguities reports it, for a person to decide. If the title also names one of them another way ("@mishikaaya"), there is nothing to ask.
package partners

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
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
	kindDuo
)

type entry struct {
	name    string
	kind    kind
	tokens  []string // lower-case spellings, whole-word matched
	solo    []string // the spellings that name this entry by themselves: tokens, minus the ones a partner shares with another one
	members []string // groups and duos only: the display names of the partners in it
}

// isTeam says whether the entry stands for several partners.
func (e entry) isTeam() bool { return e.kind == kindGroup || e.kind == kindDuo }

// addTo puts the partners this entry stands for into set: all of its members for a team, the partner itself otherwise.
func (e entry) addTo(set map[string]bool) {
	if !e.isTeam() {
		set[e.name] = true
		return
	}
	for _, m := range e.members {
		set[m] = true
	}
}

// Book is the loaded list. A nil *Book is a valid, empty list: it matches nothing, so the recap simply has no collab line.
type Book struct {
	entries []entry
	known   map[string]bool     // every spelling of every entry, for Unknown
	shared  map[string][]string // a spelling that two or more partners have -> their display names, in file order
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

var knownColumns = []string{"name", "kind", "also_matches", "members"}

// Parse reads the CSV text. All problems are reported together, so the file can be fixed in one go.
func Parse(raw []byte) (*Book, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // Excel's "UTF-8 CSV" starts with a BOM

	r := csv.NewReader(bytes.NewReader(raw))
	r.Comma = delimiter(raw)
	r.FieldsPerRecord = -1 // a short row is reported with its line number
	r.TrimLeadingSpace = true

	col, err := readHeader(r)
	if err != nil {
		return nil, err
	}

	p := &parser{
		book:       &Book{known: map[string]bool{}},
		col:        col,
		seen:       map[string]int{},
		memberLine: map[string]int{},
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := r.FieldPos(0)
		if err != nil {
			p.problem("line %d: %v", line, err)
			continue
		}
		if blank(rec) {
			continue // Excel often leaves rows of empty commas at the bottom
		}
		p.row(rec, line)
	}
	p.checkMembers()

	if len(p.problems) > 0 {
		return nil, joinProblems(p.problems)
	}
	p.book.findShared()
	return p.book, nil
}

// readHeader reads the first line and says which column holds what.
func readHeader(r *csv.Reader) (map[string]int, error) {
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
		if !slices.Contains(knownColumns, h) {
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
	return col, nil
}

// parser is the state of one Parse: the book being built and the problems found so far.
type parser struct {
	book       *Book
	col        map[string]int
	problems   []string
	seen       map[string]int // lower-case name -> line, to catch a name that is listed twice
	memberLine map[string]int // team name -> line, for checkMembers
}

func (p *parser) problem(format string, args ...any) {
	p.problems = append(p.problems, fmt.Sprintf(format, args...))
}

// cell is the trimmed text of a column in a row ("" when the column is missing).
func (p *parser) cell(rec []string, name string) string {
	if i, ok := p.col[name]; ok && i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}

// row reads one line of the file into an entry. A row with a mistake adds a problem and, when the mistake is in the name or the kind, is skipped.
func (p *parser) row(rec []string, line int) {
	name := p.cell(rec, "name")
	if name == "" {
		p.problem("line %d: the name is empty", line)
		return
	}
	if strings.Contains(name, ",") {
		p.problem("line %d (%s): a name cannot contain a comma (commas separate names when typing in /collab)", line, name)
		return
	}
	e := entry{name: name}
	k, ok := parseKind(p.cell(rec, "kind"))
	if !ok {
		p.problem("line %d (%s): kind must be partner, group, duo or ignore, got %q", line, name, p.cell(rec, "kind"))
		return
	}
	e.kind = k
	if first, dup := p.seen[strings.ToLower(name)]; dup {
		p.problem("line %d: %s is already on line %d", line, name, first)
		return
	}
	p.seen[strings.ToLower(name)] = line

	members := p.cell(rec, "members")
	switch {
	case !e.isTeam() && members != "":
		p.problem("line %d (%s): only a group or a duo can have members", line, name)
	case e.isTeam():
		for _, m := range strings.Split(members, "|") {
			if m = strings.TrimSpace(m); m != "" {
				e.members = append(e.members, m)
			}
		}
		if len(e.members) == 0 {
			p.problem("line %d (%s): a group or duo needs members", line, name)
		}
		p.memberLine[name] = line
	}

	p.spellings(&e, p.cell(rec, "also_matches"), line)
	p.book.entries = append(p.book.entries, e)
}

// parseKind reads the "kind" column; empty means partner.
func parseKind(s string) (kind, bool) {
	switch strings.ToLower(s) {
	case "", "partner":
		return kindPartner, true
	case "ignore":
		return kindIgnore, true
	case "group":
		return kindGroup, true
	case "duo":
		return kindDuo, true
	}
	return 0, false
}

// spellings gives the entry the words that match it in a title: its own name (without a leading "@") and the also_matches column.
func (p *parser) spellings(e *entry, alsoMatches string, line int) {
	spellings := append([]string{strings.TrimPrefix(e.name, "@")}, strings.Split(alsoMatches, "|")...)
	for _, s := range spellings {
		t := normalise(s)
		if t == "" {
			continue // "a||b" or a trailing "|"
		}
		if strings.Contains(t, ",") {
			p.problem("line %d (%s): %q cannot contain a comma", line, e.name, s)
			continue
		}
		if utf8.RuneCountInString(t) < 2 || !hasLetterOrDigit(t) {
			p.problem("line %d (%s): %q is too short to match safely", line, e.name, s)
			continue
		}
		if !slices.Contains(e.tokens, t) {
			e.tokens = append(e.tokens, t)
			p.book.known[t] = true
		}
	}
}

// checkMembers runs after the last row, because a team may list partners that appear further down the file. It also replaces each member by its display name, however it was typed.
func (p *parser) checkMembers() {
	entries := p.book.entries
	byName := map[string]*entry{}
	for i := range entries {
		byName[strings.TrimPrefix(strings.ToLower(entries[i].name), "@")] = &entries[i]
	}
	for i := range entries {
		g := &entries[i]
		if !g.isTeam() {
			continue
		}
		for j, m := range g.members {
			member := byName[strings.TrimPrefix(strings.ToLower(m), "@")]
			switch {
			case member == nil:
				p.problem("line %d (%s): member %q is not in the file", p.memberLine[g.name], g.name, m)
			case member.kind != kindPartner:
				p.problem("line %d (%s): member %q must be a partner, not a group or an ignore row", p.memberLine[g.name], g.name, m)
			default:
				g.members[j] = member.name
			}
		}
	}
}

// findShared notes the spellings that belong to two or more partners, and gives every entry its solo spellings.
func (b *Book) findShared() {
	owners := map[string][]string{}
	for _, e := range b.entries {
		if e.kind != kindPartner {
			continue
		}
		for _, t := range e.tokens {
			owners[t] = append(owners[t], e.name)
		}
	}
	b.shared = map[string][]string{}
	for t, who := range owners {
		if len(who) > 1 {
			b.shared[t] = who
		}
	}
	for i := range b.entries {
		e := &b.entries[i]
		e.solo = nil
		for _, t := range e.tokens {
			if e.kind == kindPartner && b.shared[t] != nil {
				continue
			}
			e.solo = append(e.solo, t)
		}
	}
}

// delimiter picks the separator from the first line. Excel with Indonesian (and many European) regional settings saves CSV files with ";" instead of ",".
func delimiter(raw []byte) rune {
	first, _, _ := bytes.Cut(raw, []byte("\n"))
	if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		return ';'
	}
	return ','
}

// Match returns the display names of the partners named in the title, in the order of the file, each once. A group named in the title counts as all of its members; a person who was absent is taken out afterwards by hand. A spelling that two partners share names nobody by itself (see Ambiguities).
func (b *Book) Match(title string) []string {
	if b == nil {
		return nil
	}
	text := normalise(title)
	named := map[string]bool{}
	for _, e := range b.entries {
		if e.kind != kindIgnore && anyWord(text, e.solo) {
			e.addTo(named)
		}
	}
	return b.inFileOrder(named)
}

// Ambiguity is a spelling in a title that belongs to more than one partner, so the title does not say who was there.
type Ambiguity struct {
	Word       string   // the spelling, lower-case
	Candidates []string // the partners who have it, in file order
}

// Ambiguities lists the shared spellings in the title that nothing else in the title settles: if one of the candidates is also named another way (by her handle, say), the shared word is taken to mean that one and is not listed. Sorted by spelling.
func (b *Book) Ambiguities(title string) []Ambiguity {
	if b == nil || len(b.shared) == 0 {
		return nil
	}
	text := normalise(title)
	named := map[string]bool{}
	for _, n := range b.Match(title) {
		named[n] = true
	}
	var out []Ambiguity
	for _, w := range slices.Sorted(maps.Keys(b.shared)) {
		who := b.shared[w]
		if hasWord(text, w) && !slices.ContainsFunc(who, func(n string) bool { return named[n] }) {
			out = append(out, Ambiguity{Word: w, Candidates: slices.Clone(who)})
		}
	}
	return out
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
			if e.kind == kindIgnore || !slices.Contains(e.tokens, t) {
				continue
			}
			found = true
			e.addTo(named)
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
		return slices.Clone(names)
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

// SummaryFor is Summary for one stream, whose title is known: a duo is named when the title says its name and all of its members are in the list. Those members are then not listed on their own, and the duo comes back separately (in file order), so the recap can write it without "bareng".
func (b *Book) SummaryFor(title string, names []string) (with, duos []string) {
	with = b.Summary(names)
	if b == nil {
		return with, nil
	}
	present := map[string]bool{}
	for _, n := range names {
		present[n] = true
	}
	text := normalise(title)
	for _, e := range b.entries {
		if e.kind != kindDuo || !anyWord(text, e.tokens) || !allIn(e.members, present) {
			continue
		}
		duos = append(duos, e.name)
		with = slices.DeleteFunc(with, func(n string) bool { return slices.Contains(e.members, n) })
	}
	return with, duos
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
		if e.isTeam() {
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
	return slices.ContainsFunc(tokens, func(t string) bool { return strings.Contains(t, q) })
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

func joinProblems(p []string) error {
	return fmt.Errorf("%d problem(s):\n  - %s", len(p), strings.Join(p, "\n  - "))
}
