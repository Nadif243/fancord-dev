package broadcast

// MapMemory is a Memory that lives only in RAM, so it forgets everything when the bot stops. It is for things that must not be kept, such as the fake streams of /devstream.
//
// It has no lock of its own because an Announcer calls it one Update at a time.
// Do not share one MapMemory between two announcers.
type MapMemory struct {
	m map[string]Announced
}

func NewMapMemory() *MapMemory {
	return &MapMemory{m: map[string]Announced{}}
}

func (r *MapMemory) Get(videoID string) (Announced, bool) {
	a, ok := r.m[videoID]
	return a, ok
}

func (r *MapMemory) Put(videoID string, a Announced) error {
	r.m[videoID] = a
	return nil
}
