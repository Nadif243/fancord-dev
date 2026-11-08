package rodok

import "sort"

// Role is which side of a rodok a leaderboard is about.
type Role int

const (
	Doers   Role = iota // pelaku: who rodoks the most
	Targets             // korban: who gets rodok-ed the most
)

// Entry is one person on a leaderboard.
type Entry struct {
	ID    string // Discord user ID
	Total int    // how many times they rodok-ed (Doers) or were rodok-ed (Targets)
	// People is how many different people that was with: whom they rodok-ed, or who rodok-ed them.
	People int
}

// Board is what a leaderboard needs. *File and *Service satisfy it.
type Board interface {
	// Top is the first n people of the leaderboard, best first.
	Top(role Role, n int) []Entry
	// Rank is a person's place (1 is first) and their entry. ok is false for somebody who has no rodok on that side.
	Rank(role Role, id string) (rank int, e Entry, ok bool)
	// Total is how many rodoks there have been in all.
	Total() int
}

// ranked adds up the counts for one side and sorts them: most first; the one with more different people first on a tie; the lower ID on a further tie, so the order never changes between two looks. The caller holds the lock.
func (f *File) ranked(role Role) []Entry {
	type tally struct {
		total  int
		people int
	}
	sums := map[string]*tally{}
	for actor, row := range f.data.Counts {
		for target, n := range row {
			if n <= 0 {
				continue
			}
			id := actor
			if role == Targets {
				id = target
			}
			t := sums[id]
			if t == nil {
				t = &tally{}
				sums[id] = t
			}
			t.total += n
			t.people++
		}
	}
	out := make([]Entry, 0, len(sums))
	for id, t := range sums {
		out = append(out, Entry{ID: id, Total: t.total, People: t.people})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		if a.People != b.People {
			return a.People > b.People
		}
		return a.ID < b.ID
	})
	return out
}

func (f *File) Top(role Role, n int) []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.ranked(role)
	if n < 0 {
		n = 0
	}
	if n < len(all) {
		all = all[:n]
	}
	return all
}

func (f *File) Rank(role Role, id string) (int, Entry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, e := range f.ranked(role) {
		if e.ID == id {
			return i + 1, e, true
		}
	}
	return 0, Entry{}, false
}

func (f *File) Total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, row := range f.data.Counts {
		for _, n := range row {
			if n > 0 {
				total += n
			}
		}
	}
	return total
}

// The Service answers the same questions by asking the counts it was given, when those can answer them (the real file can).

func (s *Service) Top(role Role, n int) []Entry {
	if b, ok := s.counts.(Board); ok {
		return b.Top(role, n)
	}
	return nil
}

func (s *Service) Rank(role Role, id string) (int, Entry, bool) {
	if b, ok := s.counts.(Board); ok {
		return b.Rank(role, id)
	}
	return 0, Entry{}, false
}

func (s *Service) Total() int {
	if b, ok := s.counts.(Board); ok {
		return b.Total()
	}
	return 0
}
