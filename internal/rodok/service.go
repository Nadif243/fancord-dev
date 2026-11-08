package rodok

import (
	"sync"
	"time"
)

// The limits, in one place so they are easy to change.
const (
	// Cooldown is the pause between two rodoks by the same person.
	Cooldown = 4 * time.Second
	// Limit is how many rodoks one person may do within Window ...
	Limit = 12
	// Window starts at the person's first rodok. Once it has passed, their tally starts again from zero.
	Window = 10 * time.Minute
	// Lockout is how long a person has to wait after reaching Limit.
	Lockout = time.Hour
)

// Outcome says what happened to a rodok.
type Outcome int

const (
	Done    Outcome = iota // counted
	Cooling                // too soon after the last one (Cooldown)
	Locked                 // reached Limit; waiting out Lockout
)

// Result is what the caller needs to answer with.
type Result struct {
	Outcome Outcome
	// Count is the new total for this pair (Done only).
	Count int
	// Wait is how long until the person may try again (Cooling and Locked).
	Wait time.Duration
}

// Counter is where the totals are kept. *File satisfies it.
type Counter interface {
	Add(actor, target string) (int, error)
}

// usage is what the limiter remembers about one person. It lives in memory only: a restart gives everybody a clean slate, which is harmless.
type usage struct {
	last        time.Time // their last counted rodok
	windowStart time.Time
	used        int
	lockedUntil time.Time
}

// Service applies the limits and counts the rodoks that pass them.
type Service struct {
	counts Counter
	now    func() time.Time

	mu    sync.Mutex
	users map[string]*usage
}

func New(counts Counter) *Service {
	return &Service{counts: counts, now: time.Now, users: map[string]*usage{}}
}

// Do tries one rodok from actor to target. A refused one is not counted and does not use up any of the person's allowance.
//
// If the count could not be saved, the result is still Done (the number is right for this run) and the error is returned for logging.
func (s *Service) Do(actor, target string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.forgetOld(now)

	u := s.users[actor]
	if u == nil {
		u = &usage{}
		s.users[actor] = u
	}

	if now.Before(u.lockedUntil) {
		return Result{Outcome: Locked, Wait: u.lockedUntil.Sub(now)}, nil
	}
	if !u.last.IsZero() && now.Sub(u.last) < Cooldown {
		return Result{Outcome: Cooling, Wait: Cooldown - now.Sub(u.last)}, nil
	}

	// A new window starts at the first rodok, and again once Window has passed. (A person's first time has a zero windowStart, which is long ago;
	// a lockout always outlasts Window.)
	if now.Sub(u.windowStart) >= Window {
		u.windowStart = now
		u.used = 0
	}
	u.used++
	u.last = now
	if u.used >= Limit {
		u.lockedUntil = now.Add(Lockout)
		u.used = 0
	}

	n, err := s.counts.Add(actor, target)
	return Result{Outcome: Done, Count: n}, err
}

// forgetOld drops people whose limits have all run out, so the map does not grow with everyone who ever used the command. It only bothers once the map is big.
func (s *Service) forgetOld(now time.Time) {
	if len(s.users) < 500 {
		return
	}
	for id, u := range s.users {
		if now.After(u.lockedUntil) && now.Sub(u.last) >= Window {
			delete(s.users, id)
		}
	}
}
