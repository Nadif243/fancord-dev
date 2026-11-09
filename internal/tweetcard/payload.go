// Package tweetcard turns one X post into a Discord message built from
// "Components V2": a ping line, then one card (a container with an accent
// bar) holding the author, the text, the pictures or video, the translation,
// the numbers and a small footnote. Classic embeds cannot do this: a bot
// cannot put a video inside an embed, and an embed always draws its picture
// between the text and the footer.
//
// The package only builds the message as plain data. Sending it is the
// Discord package's job.
package tweetcard

import "errors"

// ErrRejected means Discord answered 400 to a card: the message was not accepted as built. The relay then sends a plain link instead.
var ErrRejected = errors.New("discord refused the tweet card")

// Discord's numbers for these things.
const (
	flagComponentsV2 = 1 << 15

	typeSection   = 9
	typeText      = 10
	typeThumbnail = 11
	typeGallery   = 12
	typeSeparator = 14
	typeContainer = 17

	spacingSmall = 1
)

// Component is any one of the components above. Only the fields that belong to its Type are filled; the rest stay out of the JSON.
type Component struct {
	Type int `json:"type"`

	Content string `json:"content,omitempty"` // text display

	Components  []Component `json:"components,omitempty"` // container, section
	Accessory   *Component  `json:"accessory,omitempty"`  // section: its thumbnail
	AccentColor *int        `json:"accent_color,omitempty"`

	Media       *MediaRef     `json:"media,omitempty"` // thumbnail
	Description string        `json:"description,omitempty"`
	Items       []GalleryItem `json:"items,omitempty"` // gallery

	Divider *bool `json:"divider,omitempty"` // separator: a visible line
	Spacing int   `json:"spacing,omitempty"`
}

// MediaRef points at a picture or video by its address.
type MediaRef struct {
	URL string `json:"url"`
}

// GalleryItem is one picture or video of a gallery.
type GalleryItem struct {
	Media       MediaRef `json:"media"`
	Description string   `json:"description,omitempty"`
}

// AllowedMentions tells Discord who the message may ping. Parse is always sent (empty, not missing) so nothing in the text can ping anyone by accident.
type AllowedMentions struct {
	Parse []string `json:"parse"`
	Roles []string `json:"roles,omitempty"`
}

// Payload is the body of the "send message" call.
type Payload struct {
	Flags           int             `json:"flags"`
	Components      []Component     `json:"components"`
	AllowedMentions AllowedMentions `json:"allowed_mentions"`
}

// TextUnits counts the characters of all text displays together, the number Discord limits (UTF-16 units, the cautious way to count).
func (p Payload) TextUnits() int {
	var n int
	var walk func([]Component)
	walk = func(list []Component) {
		for _, c := range list {
			if c.Type == typeText {
				n += units(c.Content)
			}
			walk(c.Components)
			if c.Accessory != nil {
				walk([]Component{*c.Accessory})
			}
		}
	}
	walk(p.Components)
	return n
}

func text(s string) Component { return Component{Type: typeText, Content: s} }

func separator(line bool) Component {
	return Component{Type: typeSeparator, Divider: &line, Spacing: spacingSmall}
}
