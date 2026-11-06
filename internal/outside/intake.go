package outside

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/Nadif243/fancord-bot/internal/relay"
	"github.com/Nadif243/fancord-bot/internal/xapi"
)

// Limits on what one message or one person may send.
const (
	maxYouTube   = 5                // YouTube links taken from one message
	maxXPosts    = 3                // X posts read for one message
	rateWindow   = time.Minute      // a person may send rateMax messages in this time
	rateMax      = 3                //
	xLookupLimit = 10 * time.Second // one X lookup
)

// PostSource reads one X post. *xapi.Client satisfies it.
type PostSource interface {
	Post(ctx context.Context, id string) (xapi.Post, error)
}

// WithX turns on reading X posts that people send. cap is the number of billed reads allowed per month for this (0 turns it off).
func (s *Service) WithX(src PostSource, cap int) *Service {
	s.x, s.xCap = src, cap
	return s
}

// Outcome is what became of a submission.
type Outcome int

const (
	Queued      Outcome = iota // at least one new video is on its way to the reviewers
	Known                      // every video was already known
	NoLink                     // nothing usable in the text
	Slow                       // this person sent too many just now
	XNotHere                   // only X links, and this place does not read them
	XNoLink                    // the X post has no YouTube link
	XCapReached                // this month's X reads are used up
	XFailed                    // X could not be read right now
	XNotFound                  // the X post is gone or private
)

// Reply is the answer to a submission. New is the number of videos queued.
type Reply struct {
	Outcome Outcome
	New     int
}

var xPostLink = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.|mobile\.)?(?:x\.com|twitter\.com)/(?:i/(?:web/)?|[A-Za-z0-9_]{1,15}/)status(?:es)?/(\d{5,25})`)

// XPostIDs finds the X posts linked in a text, once each, in order.
func XPostIDs(text string) []string {
	var out []string
	for _, m := range xPostLink.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// Submit takes what a person typed: YouTube links go straight to the lookup, and (when allowX is set and X is on) X posts are read once for the YouTube links inside them. who identifies the person for the rate limit; name is shown to the reviewers.
func (s *Service) Submit(ctx context.Context, who, name, text string, allowX bool) Reply {
	yt := YouTubeIDs(text)
	xs := XPostIDs(text)
	if len(yt) == 0 && len(xs) == 0 {
		return Reply{Outcome: NoLink}
	}
	if len(yt) == 0 && (!allowX || s.x == nil) {
		return Reply{Outcome: XNotHere}
	}
	if !s.allow(who) {
		return Reply{Outcome: Slow}
	}

	queued := s.Enqueue("manual", name, yt[:min(len(yt), maxYouTube)]...)
	known := len(yt) > 0

	var capHit, failed, notFound, noLink bool
	if allowX && s.x != nil {
		for _, id := range xs[:min(len(xs), maxXPosts)] {
			ids, problem := s.readPost(ctx, id)
			switch problem {
			case problemNone:
				if len(ids) == 0 {
					noLink = true
					continue
				}
				known = true
				queued += s.Enqueue("xlink", id, ids...)
			case problemCap:
				capHit = true
			case problemFailed:
				failed = true
			case problemNotFound:
				notFound = true
			}
		}
	}

	switch {
	case queued > 0:
		return Reply{Outcome: Queued, New: queued}
	case capHit:
		return Reply{Outcome: XCapReached}
	case failed:
		return Reply{Outcome: XFailed}
	case notFound:
		return Reply{Outcome: XNotFound}
	case noLink && !known:
		return Reply{Outcome: XNoLink}
	}
	return Reply{Outcome: Known}
}

type problem int

const (
	problemNone problem = iota
	problemCap
	problemFailed
	problemNotFound
)

// readPost returns the YouTube videos linked in an X post. A post seen before costs nothing; a new one is one billed read, counted against the monthly cap. Failed and not-found lookups are not billed, so they are not counted.
func (s *Service) readPost(ctx context.Context, id string) ([]string, problem) {
	if look, ok := s.file.Tweet(id); ok {
		return look.VideoIDs, problemNone
	}
	month := s.now().UTC().Format("2006-01")
	if s.file.XReadsIn(month) >= s.xCap {
		return nil, problemCap
	}
	ctx, cancel := context.WithTimeout(ctx, xLookupLimit)
	defer cancel()
	post, err := s.x.Post(ctx, id)
	switch {
	case errors.Is(err, xapi.ErrNotFound):
		return nil, problemNotFound
	case err != nil:
		s.log.Warn("collab review: could not read an X post", "post", id, "err", err)
		return nil, problemFailed
	}
	if _, err := s.file.AddXRead(month); err != nil {
		s.log.Warn("collab review: could not save the X read count", "err", err)
	}
	var ids []string
	for _, raw := range post.URLs {
		if v, ok := relay.YouTubeID(raw); ok && !slices.Contains(ids, v) {
			ids = append(ids, v)
		}
	}
	if err := s.file.PutTweet(id, ids, s.now()); err != nil {
		s.log.Warn("collab review: could not save", "err", err)
	}
	return ids, problemNone
}

// allow applies the per-person limit: rateMax messages in rateWindow. Only accepted calls count.
func (s *Service) allow(who string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := slices.DeleteFunc(s.recent[who], func(t time.Time) bool { return now.Sub(t) >= rateWindow })
	if len(recent) >= rateMax {
		s.recent[who] = recent
		return false
	}
	s.recent[who] = append(recent, now)
	return true
}
