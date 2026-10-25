// Package broadcast turns what we know about a stream into messages in Discord.
package broadcast

import "time"

// State is where a stream is in its life. The order matters: a stream only ever moves forward (Upcoming -> Live -> Ended), never back.
type State int

// iota counts up from 0 inside a const block. Starting at iota + 1 keeps 0 free, so a Stream whose State was never set is obviously invalid.
const (
	Upcoming State = iota + 1 // scheduled, not started yet
	Live
	Ended
)

// Stream is what every source (YouTube, a tweet, /collab) boils down to.
type Stream struct {
	VideoID string
	Title   string
	State   State
	StartAt time.Time // scheduled start; the zero value means "not known"
}

// URL is the watch link. Discord shows a video preview for it by itself.
func (s Stream) URL() string {
	return "https://www.youtube.com/watch?v=" + s.VideoID
}
