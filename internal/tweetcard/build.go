package tweetcard

import (
	"fmt"
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

// Stats are the numbers shown under a card.
type Stats struct {
	Replies, Reposts, Likes, Views int
	HasViews                       bool
}

func (s Stats) any() bool {
	return s.Replies > 0 || s.Reposts > 0 || s.Likes > 0 || s.Views > 0
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
	Stats   Stats
	ReplyTo string // ID of the post that was answered, for a link in the footnote
}

// Build lays the card out as a message.
func Build(m Model) Payload {
	lead := m.Lead
	if m.RoleID != "" {
		lead = "<@&" + m.RoleID + "> " + lead
	}
	header := msg.TweetHeader(m.Name, m.Handle, m.PostURL)
	stats := formatStats(m.Stats)

	// The parts that can be long, escaped (the escaping adds characters, so it is done before measuring).
	var (
		caption = escapeText(m.Caption)
		capTr   = escapeText(m.Translation)
		qText   string
		qTr     string
		qHead   string
		cutLine bool
	)
	if m.Quote != nil {
		qHead = msg.TweetQuoteHead(m.Quote.Name, m.Quote.Handle, m.Quote.URL)
		var c1, c2 bool
		qText, c1 = limitLines(escapeText(m.Quote.Text), maxQuoteLines)
		qTr, c2 = limitLines(escapeText(m.Quote.Translation), maxQuoteLines)
		cutLine = c1 || c2
	}

	// The room for the long parts is what the limit leaves after the parts that are always whole, up to the soft limit.
	fixed := units(lead) + units(header) + units(qHead) + units(stats) + units(msg.TweetTranslationHead) +
		units(msg.TweetFootnote(m.Posted, m.PostURL, m.ReplyTo, true)) +
		units(msg.TweetVideoStill) + units(msg.TweetMediaMissing) + slack
	budget := textLimit - fixed
	if budget > softLimit {
		budget = softLimit
	}
	if budget < 0 {
		budget = 0
	}
	parts := []*string{&caption, &qText, &capTr, &qTr}
	lens := make([]int, len(parts))
	for i, p := range parts {
		lens[i] = units(*p)
	}
	for i, allowed := range share(lens, budget) {
		if lens[i] > allowed {
			*parts[i] = cut(*parts[i], allowed)
			cutLine = true
		}
	}

	// ---- the card, top to bottom
	var card []Component
	if m.AvatarURL != "" {
		card = append(card, Component{
			Type:       typeSection,
			Components: []Component{text(header)},
			Accessory:  &Component{Type: typeThumbnail, Media: &MediaRef{URL: m.AvatarURL}, Description: truncate(m.Name, maxAlt)},
		})
	} else {
		card = append(card, text(header))
	}
	if caption != "" {
		card = append(card, text(caption))
	}
	card = append(card, mediaParts(m.Media, m.MediaMissing)...)

	if m.Quote != nil {
		card = append(card, text(quoteBlock(qHead, qText)))
		card = append(card, mediaParts(m.Quote.Media, m.Quote.MediaMissing)...)
	}

	if capTr != "" || qTr != "" {
		body := msg.TweetTranslationHead
		if capTr != "" {
			body += "\n" + capTr
		}
		if qTr != "" {
			body += "\n" + quoteBlock("", qTr)
		}
		card = append(card, separator(false), text(body))
	}

	card = append(card, separator(true))
	if stats != "" {
		card = append(card, text(stats))
	}
	card = append(card, text(msg.TweetFootnote(m.Posted, m.PostURL, m.ReplyTo, cutLine)))

	accent := accentColor
	top := []Component{{Type: typeContainer, AccentColor: &accent, Components: card}}
	if lead != "" { // a text display may not be empty
		top = append([]Component{text(lead)}, top...)
	}
	return Payload{Flags: flagComponentsV2, Components: top, AllowedMentions: allowed(m.RoleID)}
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

// formatStats is the line of numbers under a card, or "" when every number is zero (a post read a moment after it was made has none yet).
func formatStats(s Stats) string {
	if !s.any() {
		return ""
	}
	parts := []string{
		"💬 " + Abbrev(s.Replies),
		"🔁 " + Abbrev(s.Reposts),
		"❤️ " + Abbrev(s.Likes),
	}
	if s.HasViews {
		parts = append(parts, "👁️ "+Abbrev(s.Views))
	}
	return strings.Join(parts, "   ")
}

// Abbrev writes a count short: 999, 1.2K, 123.3K, 10K, 1.5M. It rounds down, so a count never looks bigger than it is.
func Abbrev(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return short(n, 1000, "K")
	default:
		return short(n, 1_000_000, "M")
	}
}

func short(n, unit int, suffix string) string {
	tenths := n / (unit / 10) // 2338 / 100 = 23
	if tenths%10 == 0 {
		return fmt.Sprintf("%d%s", tenths/10, suffix)
	}
	return fmt.Sprintf("%d.%d%s", tenths/10, tenths%10, suffix)
}
