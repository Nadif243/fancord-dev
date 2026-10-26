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

// Kind says what sort of video a Stream is. YouTube gives live streams and premieres the same shape of data, but only streams belong in the stream announcements and the weekly recap.
type Kind int

const (
	KindStream   Kind = iota + 1 // a real live broadcast: what her Live tab lists
	KindPremiere                 // a finished video released with a countdown
)

// Stream is what every source (YouTube, a tweet, /collab) boils down to.
type Stream struct {
	VideoID string
	Title   string
	State   State
	Kind    Kind      // 0 (unset) is treated as a stream
	StartAt time.Time // scheduled start; the zero value means "not known"
}

// URL is the watch link. Discord shows a video preview for it by itself.
func (s Stream) URL() string {
	return "https://www.youtube.com/watch?v=" + s.VideoID
}

// Scan is what one look at a channel found.
type Scan struct {
	Checked int      // how many videos were looked at
	Streams []Stream // the streams and premieres among them, newest first
	Notes   []string // anything that makes the result less certain, for humans
}
