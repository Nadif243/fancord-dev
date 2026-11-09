package relay

import (
	"context"
	"regexp"
	"strings"

	"github.com/Nadif243/fancord-bot/internal/msg"
	"github.com/Nadif243/fancord-bot/internal/translate"
	"github.com/Nadif243/fancord-bot/internal/tweetcard"
	"github.com/Nadif243/fancord-bot/internal/xapi"
)

// model decides what a card for this post shows. It reports false when X did not send enough to show the post faithfully (a reposted or quoted post that is missing, say); the relay then sends the plain link, which Discord and X can preview on their own.

// A repost is shown as the post that was reposted: its author, text, media and numbers. The line above the card says that she reposted it.
func (r *Relay) model(ctx context.Context, st State, p xapi.Post) (tweetcard.Model, bool) {
	m := tweetcard.Model{PostURL: r.postURL(p.ID), Posted: p.CreatedAt}
	if p.Kind == xapi.Repost {
		o := p.Original
		if !o.Known() {
			return m, false
		}
		m.Name, m.Handle, m.AvatarURL = o.Author.Name, o.Author.Username, o.Author.AvatarURL
		m.PostURL = "https://x.com/" + o.Author.Username + "/status/" + o.ID
		if !o.CreatedAt.IsZero() {
			m.Posted = o.CreatedAt
		}
		m.Caption = o.Caption
		m.Translation = r.translate(ctx, o.Caption)
		m.Media, m.MediaMissing = cardMedia(o.Media), o.MediaMissing
		m.Lead = msg.TweetLead("repost", o.Author.Username, false)
		return m, true
	}

	m.Name, m.Handle, m.AvatarURL = st.ProfileName, r.set.Username, st.ProfileAvatar
	m.Caption = p.Caption
	m.Translation = r.translate(ctx, p.Caption)
	m.Media, m.MediaMissing = cardMedia(p.Media), p.MediaMissing

	target := ""
	if q := p.Quoted; q != nil {
		if !q.Known() {
			return m, false
		}
		m.Quote = &tweetcard.Quote{
			Name:         q.Author.Name,
			Handle:       q.Author.Username,
			URL:          "https://x.com/" + q.Author.Username + "/status/" + q.ID,
			Text:         q.Caption,
			Translation:  r.translate(ctx, q.Caption),
			Media:        cardMedia(q.Media),
			MediaMissing: q.MediaMissing,
		}
		target = q.Author.Username
	}

	self := false
	if p.Kind == xapi.Reply {
		target = replyTarget(p)
		self = strings.EqualFold(target, r.set.Username)
		if p.RepliedTo != nil {
			m.ReplyTo = p.RepliedTo.ID
		}
	}
	m.Lead = msg.TweetLead(string(p.Kind), target, self)
	return m, true
}

// leadingMention is the handle a reply starts with: "@someone thanks!".
var leadingMention = regexp.MustCompile(`^@(\w{1,15})\b`)

// replyTarget is the handle a reply answers: taken from the answered post when X included it, otherwise from the @ the reply starts with ("" when neither).
func replyTarget(p xapi.Post) string {
	if p.RepliedTo.Known() {
		return p.RepliedTo.Author.Username
	}
	if m := leadingMention.FindStringSubmatch(strings.TrimSpace(p.Caption)); m != nil {
		return m[1]
	}
	return ""
}

// translate returns the translation of text when it is Japanese and a translator is set; otherwise, and on any failure, "". A post is never held back for a translation.
func (r *Relay) translate(ctx context.Context, text string) string {
	if r.set.Translator == nil || !translate.IsJapanese(text) {
		return ""
	}
	parent := r.parent
	if parent == nil {
		parent = ctx
	}
	tctx, cancel := context.WithTimeout(parent, translateTimeout)
	defer cancel()
	got, err := r.set.Translator.Translate(tctx, text)
	if err != nil {
		r.log.Warn("tweet relay: translation failed, posting without it", "err", err)
		return ""
	}
	if got = strings.TrimSpace(got); got == strings.TrimSpace(text) {
		return "" // nothing was translated
	}
	return got
}

// cardMedia picks what to show for each attachment: the photo; for a video or GIF the MP4 when it is small enough, otherwise its still picture.
func cardMedia(list []xapi.Media) []tweetcard.Media {
	var out []tweetcard.Media
	for _, md := range list {
		switch md.Type {
		case "video", "animated_gif":
			if u := md.VideoURL(maxVideoBytes); u != "" {
				out = append(out, tweetcard.Media{URL: u, Alt: md.Alt})
			} else if img := md.ImageURL(); img != "" {
				out = append(out, tweetcard.Media{URL: img, Alt: md.Alt, Still: true})
			}
		default:
			if img := md.ImageURL(); img != "" {
				out = append(out, tweetcard.Media{URL: img, Alt: md.Alt})
			}
		}
	}
	return out
}
