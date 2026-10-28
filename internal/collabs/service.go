package collabs

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/partners"
)

var (
	// ErrNoStream: the video is not in the history (never seen by the poller).
	ErrNoStream = errors.New("stream not found in the history")
	// ErrUnknownNames: some typed names are not on the partner list. Nothing was changed; Outcome.Unknown lists them.
	ErrUnknownNames = errors.New("some names are not on the partner list")
	// ErrNothingNamed: no name was typed.
	ErrNothingNamed = errors.New("no partner named")
)

// Streams is where streams are looked up. *history.File satisfies it.
type Streams interface {
	Get(videoID string) (history.Entry, bool)
	Recent(n int) []history.Entry
}

// Service combines the partner list, the by-hand tags and the stream history.
type Service struct {
	book    *partners.Book
	tags    *File
	streams Streams

	mu sync.Mutex // one change at a time, so "before" and "after" in Change are exact
}

func NewService(book *partners.Book, tags *File, streams Streams) *Service {
	return &Service{book: book, tags: tags, streams: streams}
}

// Partners is who was in the stream: named in the title or added by hand, minus those removed by hand. Titled-first, in the order of the partner file.
func (s *Service) Partners(videoID, title string) []string {
	out := s.book.Match(title)
	added, removed := s.tags.Get(videoID)
	for _, n := range added {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return slices.DeleteFunc(out, func(n string) bool { return slices.Contains(removed, n) })
}

// Unknown returns @handles in the title that the partner list does not know.
func (s *Service) Unknown(title string) []string { return s.book.Unknown(title) }

// StreamRef is a stream as a person sees it when tagging.
type StreamRef struct {
	ID       string
	Title    string
	Start    time.Time
	Live     bool
	Partners []string // who is tagged right now
}

func (s *Service) ref(e history.Entry) StreamRef {
	start := e.Start
	if start.IsZero() {
		start = e.End
	}
	return StreamRef{ID: e.VideoID, Title: e.Title, Start: start, Live: e.State == "live", Partners: s.Partners(e.VideoID, e.Title)}
}

// recentPool is how far back the search looks: well past a week of streams.
const recentPool = 100

// Recent lists up to n recent streams whose title contains query (capitals do not matter; empty means all), newest first.
func (s *Service) Recent(query string, n int) []StreamRef {
	query = strings.ToLower(strings.TrimSpace(query))
	var out []StreamRef
	for _, e := range s.streams.Recent(recentPool) {
		if len(out) == n {
			break
		}
		if query == "" || strings.Contains(strings.ToLower(e.Title), query) {
			out = append(out, s.ref(e))
		}
	}
	return out
}

// Outcome is what a tagging command did.
type Outcome struct {
	Stream  StreamRef // the stream, with its partners after the change
	Changed []string  // Add and Remove: the partners that were named
	Unknown []string  // typed names that were not recognised (with ErrUnknownNames)
	Added   []string  // Change: partners who are in the stream now but were not before
	Removed []string  // Change: partners who were in the stream before but are not now
}

// Suggest lists partners and teams matching what a person has typed so far, for autocomplete.
func (s *Service) Suggest(typed string, n int) []partners.Suggestion {
	return s.book.Suggest(typed, n)
}

// Change is the /collab command: it adds some people to a stream and takes some off, in one step. Both texts are checked first; if any name is not recognised nothing is changed. Someone named in both ends up removed.
// With both texts empty it changes nothing and just reports the stream.
func (s *Service) Change(videoID, add, remove string) (Outcome, error) {
	e, ok := s.streams.Get(videoID)
	if !ok {
		return Outcome{}, ErrNoStream
	}
	toAdd, unknownAdd := s.book.Resolve(add)
	toRemove, unknownRemove := s.book.Resolve(remove)
	if unknown := append(unknownAdd, unknownRemove...); len(unknown) > 0 {
		return Outcome{Stream: s.ref(e), Unknown: unknown}, ErrUnknownNames
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.Partners(e.VideoID, e.Title)
	var saveErr error
	if len(toAdd) > 0 {
		saveErr = errors.Join(saveErr, s.tags.Add(videoID, toAdd))
	}
	if len(toRemove) > 0 {
		saveErr = errors.Join(saveErr, s.tags.Remove(videoID, toRemove))
	}
	ref := s.ref(e)
	out := Outcome{
		Stream:  ref,
		Added:   without(ref.Partners, before),
		Removed: without(before, ref.Partners),
	}
	if saveErr != nil {
		// The change still holds until the bot restarts; the caller is told.
		return out, fmt.Errorf("save collab tags: %w", saveErr)
	}
	return out, nil
}

// without returns the names in a that are not in b, in a's order.
func without(a, b []string) []string {
	var out []string
	for _, n := range a {
		if !slices.Contains(b, n) {
			out = append(out, n)
		}
	}
	return out
}

// VideoID reads what a person gave as "the stream": a bare video ID, or a YouTube link in any of its usual shapes (watch?v=, youtu.be/, /live/, /shorts/, /embed/). Anything else is returned as typed, so the lookup in the history fails and the person is told the stream is unknown.
func VideoID(input string) string {
	in := strings.TrimSpace(input)
	u, err := url.Parse(in)
	if err != nil || u.Host == "" {
		return in // a bare video ID has no host, so it comes back as typed
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), "m.")
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case host == "youtu.be" && parts[0] != "":
		return parts[0]
	case host == "youtube.com" || host == "music.youtube.com":
		if v := u.Query().Get("v"); v != "" {
			return v
		}
		if len(parts) == 2 && (parts[0] == "live" || parts[0] == "shorts" || parts[0] == "embed") {
			return parts[1]
		}
	}
	return in
}

// Add says these people were in the stream. who is typed text such as "dina, kocak": a team means all its members. Either everything is applied or, if a name is not recognised, nothing is.
func (s *Service) Add(videoID, who string) (Outcome, error) {
	return s.apply(videoID, who, s.tags.Add)
}

// Remove says these people were not in the stream.
func (s *Service) Remove(videoID, who string) (Outcome, error) {
	return s.apply(videoID, who, s.tags.Remove)
}

func (s *Service) apply(videoID, who string, write func(string, []string) error) (Outcome, error) {
	e, ok := s.streams.Get(videoID)
	if !ok {
		return Outcome{}, ErrNoStream
	}
	names, unknown := s.book.Resolve(who)
	switch {
	case len(unknown) > 0:
		return Outcome{Stream: s.ref(e), Unknown: unknown}, ErrUnknownNames
	case len(names) == 0:
		return Outcome{Stream: s.ref(e)}, ErrNothingNamed
	}
	err := write(videoID, names)
	// Built after the write, so it shows the partners as they are now. (If only the saving failed, the change still holds until the bot restarts.)
	out := Outcome{Stream: s.ref(e), Changed: names}
	if err != nil {
		return out, fmt.Errorf("save collab tags: %w", err)
	}
	return out, nil
}
