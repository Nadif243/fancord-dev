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

// Kind says what sort of video a Stream is. YouTube describes live streams and premieres in the same way, and an ordinary upload has no live state at all. Only real streams belong in the weekly recap.
type Kind int

// Like State, Kind starts at 1; the zero value (unset) is read as a stream.
const (
	KindStream   Kind = iota + 1 // a real live broadcast: what her Live tab lists
	KindPremiere                 // a finished video released with a countdown
	KindVideo                    // an ordinary upload: announced once, never edited
	KindShort                    // a Short: the same, told apart from a video
)

// Stream is what every source (YouTube, a tweet, /collab) boils down to.
type Stream struct {
	VideoID string
	Title   string
	State   State
	Kind    Kind      // 0 (unset) is treated as a stream
	StartAt time.Time // scheduled start; for a video or Short, when it was published; zero = not known
	EndAt   time.Time // when it ended; the zero value means "not over, or not known"

	// Channel is the name of the channel it happens on when that is not hers:
	// a collab on a partner's channel. Empty means her own channel.
	Channel string
}

// IsUpload says the stream is an ordinary upload (a video or a Short): it has no waiting room or live state, only a moment it was published.
func (s Stream) IsUpload() bool { return s.Kind == KindVideo || s.Kind == KindShort }

// URL is the watch link. Discord shows a video preview for it by itself.
func (s Stream) URL() string { return WatchURL(s.VideoID) }

// WatchURL is the watch link of a video. It is the one place that knows what such a link looks like.
func WatchURL(videoID string) string { return "https://www.youtube.com/watch?v=" + videoID }

// Scan is what one look at a channel found.
type Scan struct {
	Checked int      // how many videos were looked at
	Streams []Stream // the streams and premieres among them (and uploads, when asked for), newest first
	Notes   []string // anything that makes the result less certain, for humans
}
