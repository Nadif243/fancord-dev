package tweetcard

import (
	"strings"
	"time"

	"github.com/Nadif243/fancord-bot/internal/msg"
)

const (
	// Discord allows 4000 characters of text in all the text displays of one message together.
	textLimit = 4000
	// A card shows at most this much of the long parts (caption, quote and the translations together), so a very long post does not fill the channel.
	softLimit = 2000
	// Room kept for what is only known after fitting ("teks dipotong"), and for line-start characters of the quote block.
	slack = 150
	// A quote is shown with at most this many lines.
	maxQuoteLines = 15
	// X allows four pictures in a post.
	maxMedia = 4
	// The accent bar: X's blue.
	accentColor = 0x1D9BF0
	// Alt text longer than this is not accepted for a gallery item.
	maxAlt = 1000
)

// Media is one picture or video to show.
type Media struct {
	URL string // a picture or an MP4
	Alt string // what the author wrote about it, if anything
	// Still is true when URL is a still picture standing in for a video that could not be put in the card.
	Still bool
}

// Quote is a post that the card's post quotes.
type Quote struct {
	Name, Handle string
	URL          string
	Text         string
	Translation  string
	Media        []Media
	MediaMissing bool
}

// Model is everything one card shows. The relay fills it; Build lays it out.
type Model struct {
	RoleID string // pinged in the line above the card; "" for no ping
	Lead   string // the line above the card (see msg.TweetLead)

	Name, Handle, AvatarURL string // who wrote the post that is shown
	PostURL                 string
	Posted                  time.Time

	Caption      string // plain text; escaping is done here
	Translation  string // of the caption, when it was Japanese
	Media        []Media
	MediaMissing bool

	Quote   *Quote
	ReplyTo string // ID of the post that was answered, for a link in the footnote
}

// Build lays the card out as a message.
func Build(m Model) Payload {
	lead := m.Lead
	if m.RoleID != "" {
		lead = "<@&" + m.RoleID + "> " + lead
	}
	header := msg.TweetHeader(m.Name, m.Handle, m.PostURL)
	t := fitTexts(m, lead, header)

	accent := accentColor
	top := []Component{{Type: typeContainer, AccentColor: &accent, Components: layout(m, header, t)}}
	if lead != "" { // a text display may not be empty
		top = append([]Component{text(lead)}, top...)
	}
	return Payload{Flags: flagComponentsV2, Components: top, AllowedMentions: allowed(m.RoleID)}
}

// texts are the long parts of a card, escaped and cut to fit.
type texts struct {
	caption, translation string // of the post
	quoteHead, quote     string // of the quoted post
	quoteTranslation     string
	cut                  bool // something was cut, so the footnote says so
}

// fitTexts escapes the long parts of the card and cuts them so that all the text of the message stays within Discord's limit.
func fitTexts(m Model, lead, header string) texts {
	// Escaping adds characters, so it is done before measuring.
	t := texts{caption: escapeText(m.Caption), translation: escapeText(m.Translation)}
	if m.Quote != nil {
		var c1, c2 bool
		t.quoteHead = msg.TweetQuoteHead(m.Quote.Name, m.Quote.Handle, m.Quote.URL)
		t.quote, c1 = limitLines(escapeText(m.Quote.Text), maxQuoteLines)
		t.quoteTranslation, c2 = limitLines(escapeText(m.Quote.Translation), maxQuoteLines)
		t.cut = c1 || c2
	}

	// The room for the long parts is what the limit leaves after the parts that are always whole, up to the soft limit.
	fixed := units(lead) + units(header) + units(t.quoteHead) + units(msg.TweetTranslationHead) +
		units(msg.TweetFootnote(m.Posted, m.PostURL, m.ReplyTo, true)) +
		units(msg.TweetVideoStill) + units(msg.TweetMediaMissing) + slack
	budget := min(max(textLimit-fixed, 0), softLimit)

	parts := []*string{&t.caption, &t.quote, &t.translation, &t.quoteTranslation}
	lens := make([]int, len(parts))
	for i, p := range parts {
		lens[i] = units(*p)
	}
	for i, allowed := range share(lens, budget) {
		if lens[i] > allowed {
			*parts[i] = cut(*parts[i], allowed)
			t.cut = true
		}
	}
	return t
}

// layout is the card, top to bottom.
func layout(m Model, header string, t texts) []Component {
	// The caption goes in the same section as the name, so it sits beside the picture right under the name. Outside the section it would start below the picture's whole height, leaving a blank gap under a one-line name.
	var card []Component
	head := []Component{text(header)}
	if t.caption != "" {
		head = append(head, text(t.caption))
	}
	if m.AvatarURL != "" {
		card = append(card, Component{
			Type:       typeSection,
			Components: head, // a section holds one to three text displays
			Accessory:  &Component{Type: typeThumbnail, Media: &MediaRef{URL: m.AvatarURL}, Description: truncate(m.Name, maxAlt)},
		})
	} else {
		card = append(card, head...)
	}
	card = append(card, mediaParts(m.Media, m.MediaMissing)...)

	if m.Quote != nil {
		card = append(card, text(quoteBlock(t.quoteHead, t.quote)))
		card = append(card, mediaParts(m.Quote.Media, m.Quote.MediaMissing)...)
	}

	if t.translation != "" || t.quoteTranslation != "" {
		body := msg.TweetTranslationHead
		if t.translation != "" {
			body += "\n" + t.translation
		}
		if t.quoteTranslation != "" {
			body += "\n" + quoteBlock("", t.quoteTranslation)
		}
		card = append(card, separator(false), text(body))
	}

	return append(card, separator(true), text(msg.TweetFootnote(m.Posted, m.PostURL, m.ReplyTo, t.cut)))
}

func allowed(roleID string) AllowedMentions {
	a := AllowedMentions{Parse: []string{}}
	if roleID != "" {
		a.Roles = []string{roleID}
	}
	return a
}

// quoteBlock writes lines as a Discord quote (a grey bar on the left). head, when given, is the first line.
func quoteBlock(head, body string) string {
	var lines []string
	if head != "" {
		lines = append(lines, head)
	}
	if body != "" {
		lines = append(lines, strings.Split(body, "\n")...)
	}
	for i, l := range lines {
		if l == "" {
			lines[i] = ">" // an empty quoted line keeps the bar unbroken
			continue
		}
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// mediaParts is the gallery for some media, plus a note when something could not be shown. Nothing at all when there is nothing to show.
func mediaParts(list []Media, missing bool) []Component {
	var out []Component
	var items []GalleryItem
	stills := false
	for _, md := range list {
		if md.URL == "" || len(items) == maxMedia {
			continue
		}
		items = append(items, GalleryItem{Media: MediaRef{URL: md.URL}, Description: truncate(md.Alt, maxAlt)})
		stills = stills || md.Still
	}
	if len(items) > 0 {
		out = append(out, Component{Type: typeGallery, Items: items})
	}
	if stills {
		out = append(out, text(msg.TweetVideoStill))
	}
	if missing {
		out = append(out, text(msg.TweetMediaMissing))
	}
	return out
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
