// Package xapi reads a user's posts from the X (Twitter) API v2.
//
// X bills reads per post returned, so every call asks only for what is new (since_id) and the caller counts what came back.
package xapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.x.com/2"

// Kind says what sort of post this is.
type Kind string

const (
	Original Kind = "post"
	Reply    Kind = "reply"
	Quote    Kind = "quote"
	Repost   Kind = "repost"
)

// Post is one post, reduced to what the bot needs.
type Post struct {
	ID        string
	Text      string
	CreatedAt time.Time
	Kind      Kind
	// URLs are the links in the post, fully written out (not t.co). For a repost or a quote they also hold the links of the original post, because the repost's own text is cut short.
	URLs []string

	// What follows is for showing the post as a card. Everything here comes in the same answer as the post and is not billed on top of it.

	// Caption is the post's own text made readable: links written out, the links to its pictures and to a quoted post removed, "&amp;" turned back into "&". A repost has none (its own text is only "RT @name: ...").
	Caption string
	Media   []Media
	// MediaMissing is true when the post has media that X did not describe, so a card should say there is more to see on X.
	MediaMissing bool

	// The posts this one is built on. Each is nil when the post is not that kind. A Ref with an empty Author.Username is a post X did not include (deleted, or from a private account): it is only known by its ID.
	Original  *Ref // the post that was reposted
	Quoted    *Ref // the post that was quoted
	RepliedTo *Ref // the post that was answered
}

// Ref is a post that another post reposts, quotes or answers.
type Ref struct {
	ID           string
	Author       User
	Caption      string
	CreatedAt    time.Time
	Media        []Media
	MediaMissing bool
}

// Known reports whether X described the post (and its author), not just named it.
func (r *Ref) Known() bool { return r != nil && r.Author.Username != "" }

// User is an account, as much as a card needs.
type User struct {
	ID        string
	Name      string // the display name
	Username  string // the handle, without the @
	AvatarURL string // a large version of the profile picture
}

// Media is a picture, a video or an animated GIF attached to a post.
type Media struct {
	Type       string // "photo", "video" or "animated_gif"
	URL        string // the picture itself (photos only)
	Preview    string // a still from the video or GIF
	Alt        string // the description the author wrote, if any
	Width      int
	Height     int
	DurationMS int
	Variants   []Variant
}

// Variant is one downloadable version of a video.
type Variant struct {
	URL         string
	ContentType string
	BitRate     int // bits per second; 0 for a playlist
}

// ImageURL is the picture to show for this media: the photo, or the still of a video. Photos are asked for in the large size.
func (m Media) ImageURL() string {
	u := m.URL
	if m.Type != "photo" {
		u = m.Preview
	}
	if u != "" && m.Type == "photo" && !strings.Contains(u, "?") {
		u += "?name=large"
	}
	return u
}

// VideoURL is the best MP4 of a video or GIF whose size is estimated to be at most maxBytes (bit rate times duration), or "" when none qualifies. When the duration is not known the estimate is skipped and the second-best quality is taken, a safe middle.
func (m Media) VideoURL(maxBytes int64) string {
	if m.Type != "video" && m.Type != "animated_gif" {
		return ""
	}
	var mp4 []Variant
	for _, v := range m.Variants {
		if v.ContentType == "video/mp4" && v.URL != "" {
			mp4 = append(mp4, v)
		}
	}
	sort.Slice(mp4, func(i, j int) bool { return mp4[i].BitRate > mp4[j].BitRate }) // best first
	if len(mp4) == 0 {
		return ""
	}
	if m.DurationMS <= 0 {
		if len(mp4) > 1 {
			return mp4[1].URL
		}
		return mp4[0].URL
	}
	for _, v := range mp4 {
		size := int64(v.BitRate) / 8 * int64(m.DurationMS) / 1000
		if size <= maxBytes {
			return v.URL
		}
	}
	return ""
}

// Page is what one call returned.
type Page struct {
	Posts []Post // oldest first
	// Billed is how many posts X charged for: the posts returned. (The originals that come along with a repost or a quote were not charged in the first real day of use, and neither were checks that found nothing.)
	Billed int
}

// Client talks to the API with one app-only bearer token. It is not safe to share between goroutines (it rewords its requests as X complains), so each part of the bot that reads X has its own.
type Client struct {
	token   string
	baseURL string // a field so tests can point it at a fake server
	http    *http.Client
	// X's documentation calls the parameter "post.fields" in places and the API has long used "tweet.fields". We start with the long-standing one and switch once if X says it does not know it.
	fieldsParam string
	// noNestedMedia is set when X refused the expansion that brings the media of reposted and quoted posts (see nestedMediaExpansion); from then on it is left out.
	noNestedMedia bool
}

// nestedMediaExpansion asks X to describe the media of the posts a post builds on. Without it, only the media of the post itself is described, and a repost's picture or video is reported as missing.
const nestedMediaExpansion = "referenced_tweets.id.attachments.media_keys"

func NewClient(bearerToken string) *Client {
	return &Client{
		token:       bearerToken,
		baseURL:     defaultBaseURL,
		http:        &http.Client{Timeout: 15 * time.Second},
		fieldsParam: "tweet.fields",
	}
}

// User looks a handle up (without the @) and returns the account with its display name and picture. It costs one user read, so the caller should keep the answer.
func (c *Client) User(ctx context.Context, username string) (User, error) {
	q := url.Values{}
	q.Set("user.fields", "name,username,profile_image_url")
	var out struct {
		Data *rawUser `json:"data"`
	}
	if err := c.get(ctx, "users/by/username/"+url.PathEscape(username), q, &out); err != nil {
		return User{}, err
	}
	if out.Data == nil || out.Data.ID == "" {
		return User{}, fmt.Errorf("x user lookup: no account named @%s", username)
	}
	return out.Data.user(), nil
}

// Posts returns the user's posts newer than sinceID, oldest first. With an empty sinceID it returns the 5 newest (the smallest page X allows), which is enough to learn where "now" is.
func (c *Client) Posts(ctx context.Context, userID, sinceID string) (Page, error) {
	return retrying(c, func() (Page, error) { return c.posts(ctx, userID, sinceID) })
}

// ErrNotFound means X answered, but has no such post (deleted, private, or a wrong ID). Such a lookup is not billed.
var ErrNotFound = errors.New("x post not found")

// Post looks one post up by its ID. A found post is one billed read; the original of a quote or repost comes along for free and its links are included, like in Posts.
func (c *Client) Post(ctx context.Context, id string) (Post, error) {
	return retrying(c, func() (Post, error) { return c.post(ctx, id) })
}

// retrying runs call, and once more when X answered 400 about something this client can reword (see adjust).
func retrying[T any](c *Client, call func() (T, error)) (T, error) {
	v, err := call()
	var se *statusError
	if errors.As(err, &se) && se.status == http.StatusBadRequest && c.adjust(se.message) {
		return call()
	}
	return v, err
}

// query holds what is asked of every post call: the post's own fields, and the extra objects (the posts it builds on, their authors, the media) that come back in the same answer.
func (c *Client) query() url.Values {
	q := url.Values{}
	q.Set(c.fieldsParam, "created_at,entities,referenced_tweets,author_id,attachments,note_tweet")
	expansions := "referenced_tweets.id,referenced_tweets.id.author_id,attachments.media_keys"
	if !c.noNestedMedia {
		expansions += "," + nestedMediaExpansion
	}
	q.Set("expansions", expansions)
	q.Set("media.fields", "type,url,preview_image_url,variants,alt_text,width,height,duration_ms")
	q.Set("user.fields", "name,username,profile_image_url")
	return q
}

func (c *Client) post(ctx context.Context, id string) (Post, error) {
	q := c.query()
	var raw struct {
		Data     *rawPost `json:"data"`
		Includes struct {
			Tweets []rawPost `json:"tweets"`
			Posts  []rawPost `json:"posts"`
		} `json:"includes"`
	}
	if err := c.get(ctx, "tweets/"+url.PathEscape(id), q, &raw); err != nil {
		return Post{}, err
	}
	if raw.Data == nil || raw.Data.ID == "" {
		return Post{}, ErrNotFound
	}
	r := response{Data: []rawPost{*raw.Data}}
	r.Includes.Tweets, r.Includes.Posts = raw.Includes.Tweets, raw.Includes.Posts
	return r.page().Posts[0], nil
}

// adjust rewords the request after a 400 that names something X does not know, and reports whether it changed anything (so the call is worth repeating once).
func (c *Client) adjust(message string) bool {
	if !c.noNestedMedia && strings.Contains(message, nestedMediaExpansion) {
		c.noNestedMedia = true
		return true
	}
	return c.swapFieldsParam(message)
}

// swapFieldsParam switches between "tweet.fields" and "post.fields" when X's error names the one in use. It reports whether it switched.
func (c *Client) swapFieldsParam(message string) bool {
	if !strings.Contains(message, c.fieldsParam) {
		return false
	}
	if c.fieldsParam == "tweet.fields" {
		c.fieldsParam = "post.fields"
	} else {
		c.fieldsParam = "tweet.fields"
	}
	return true
}

func (c *Client) posts(ctx context.Context, userID, sinceID string) (Page, error) {
	q := c.query()
	if sinceID == "" {
		q.Set("max_results", "5")
	} else {
		q.Set("max_results", "100")
		q.Set("since_id", sinceID)
	}
	var raw response
	if err := c.get(ctx, "users/"+url.PathEscape(userID)+"/tweets", q, &raw); err != nil {
		return Page{}, err
	}
	return raw.page(), nil
}

// ---- the answer's shape -----------------------------------------------------

type rawURL struct {
	URL      string `json:"url"` // the t.co link as it stands in the text
	Expanded string `json:"expanded_url"`
	Unwound  string `json:"unwound_url"`
	MediaKey string `json:"media_key"` // set when the link stands for attached media
}

type rawPost struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	AuthorID  string `json:"author_id"`
	Entities  struct {
		URLs []rawURL `json:"urls"`
	} `json:"entities"`
	// A long post (over 280 characters) has its full text here; Text is cut.
	Note *struct {
		Text     string `json:"text"`
		Entities struct {
			URLs []rawURL `json:"urls"`
		} `json:"entities"`
	} `json:"note_tweet"`
	Attachments struct {
		MediaKeys []string `json:"media_keys"`
	} `json:"attachments"`
	// X's documentation has used both names for this list.
	RefTweets []rawRef `json:"referenced_tweets"`
	RefPosts  []rawRef `json:"referenced_posts"`
}

type rawRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type rawUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Avatar   string `json:"profile_image_url"`
}

func (u rawUser) user() User {
	// X sends a 48-pixel picture; the same file exists in 400 pixels.
	return User{ID: u.ID, Name: u.Name, Username: u.Username, AvatarURL: strings.Replace(u.Avatar, "_normal.", "_400x400.", 1)}
}

type rawMedia struct {
	Key        string `json:"media_key"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	Preview    string `json:"preview_image_url"`
	Alt        string `json:"alt_text"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	DurationMS int    `json:"duration_ms"`
	Variants   []struct {
		BitRate     int    `json:"bit_rate"`
		ContentType string `json:"content_type"`
		URL         string `json:"url"`
	} `json:"variants"`
}

type response struct {
	Data     []rawPost `json:"data"`
	Includes struct {
		Tweets []rawPost  `json:"tweets"`
		Posts  []rawPost  `json:"posts"`
		Users  []rawUser  `json:"users"`
		Media  []rawMedia `json:"media"`
	} `json:"includes"`
}

func (r response) page() Page {
	l := r.lookup()
	var out []Post
	for _, p := range r.Data {
		out = append(out, l.post(p))
	}
	sort.Slice(out, func(i, j int) bool { return idLess(out[i].ID, out[j].ID) })
	return Page{Posts: out, Billed: len(r.Data)}
}

// lookup is the extra objects that came with an answer, by ID: the posts a post
// builds on, their authors and the media.
type lookup struct {
	originals map[string]rawPost
	users     map[string]rawUser
	media     map[string]rawMedia
}

func (r response) lookup() lookup {
	l := lookup{originals: map[string]rawPost{}, users: map[string]rawUser{}, media: map[string]rawMedia{}}
	for _, p := range slices.Concat(r.Includes.Tweets, r.Includes.Posts) {
		l.originals[p.ID] = p
	}
	for _, u := range r.Includes.Users {
		l.users[u.ID] = u
	}
	for _, m := range r.Includes.Media {
		l.media[m.Key] = m
	}
	return l
}

// post turns one post of the answer into a Post.
func (l lookup) post(p rawPost) Post {
	refs := slices.Concat(p.RefTweets, p.RefPosts)
	post := Post{ID: p.ID, Text: p.Text, CreatedAt: parseTime(p.CreatedAt), Kind: kindOf(refs, p.Text), URLs: p.urls()}

	if post.Kind != Repost {
		post.Caption = p.caption(l.shownQuote(refs))
		post.Media, post.MediaMissing = p.media(l.media)
	}

	for _, ref := range refs {
		switch ref.Type {
		case "retweeted", "quoted":
			if orig, ok := l.originals[ref.ID]; ok {
				post.URLs = append(post.URLs, orig.urls()...)
			}
		}
		var slot **Ref
		switch ref.Type {
		case "retweeted":
			slot = &post.Original
		case "quoted":
			slot = &post.Quoted
		case "replied_to":
			slot = &post.RepliedTo
		default:
			continue
		}
		if *slot == nil { // the first reference of a kind wins
			*slot = l.ref(ref.ID)
		}
	}
	return post
}

// shownQuote is the ID of the quoted post when the answer holds it, so the card can show it and its link is dropped from the caption; "" otherwise, and then the link stays (the reader would lose it).
func (l lookup) shownQuote(refs []rawRef) string {
	id := ""
	for _, ref := range refs {
		if _, ok := l.originals[ref.ID]; ok && ref.Type == "quoted" {
			id = ref.ID
		}
	}
	return id
}

// ref describes a referenced post from the extra objects of the answer. A post X did not include comes back with only its ID.
func (l lookup) ref(id string) *Ref {
	o, ok := l.originals[id]
	if !ok {
		return &Ref{ID: id}
	}
	ref := &Ref{ID: id, Author: l.users[o.AuthorID].user(), Caption: o.caption(""), CreatedAt: parseTime(o.CreatedAt)}
	ref.Media, ref.MediaMissing = o.media(l.media)
	return ref
}

// parseTime reads X's time; a missing or unreadable one is the zero time.
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// urls lists the links of the post, written out in full.
func (p rawPost) urls() []string {
	var out []string
	for _, u := range p.Entities.URLs {
		switch {
		case u.Unwound != "":
			out = append(out, u.Unwound)
		case u.Expanded != "":
			out = append(out, u.Expanded)
		}
	}
	return out
}

var (
	// x.com/name/status/123/photo/1 is the link X puts in the text for a picture.
	mediaLink = regexp.MustCompile(`^https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/[^/]+/status/\d+/(?:photo|video)/\d+/?$`)
	statusID  = regexp.MustCompile(`^https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/[^/]+/status/(\d+)`)
	threeNL   = regexp.MustCompile(`\n{3,}`)
)

// caption is the post's text made readable. The full text of a long post is used when X sent it. Each t.co link becomes the page it points at, except links that only stand for attached media and the link to the quoted post (dropQuote is its ID, "" when there is none to drop), which are removed.
func (p rawPost) caption(dropQuote string) string {
	text, urls := p.Text, p.Entities.URLs
	if p.Note != nil && p.Note.Text != "" {
		text, urls = p.Note.Text, p.Note.Entities.URLs
	}
	for _, u := range urls {
		if u.URL == "" {
			continue
		}
		target := u.Expanded
		if target == "" {
			target = u.Unwound
		}
		switch {
		case u.MediaKey != "" || mediaLink.MatchString(target):
			target = ""
		case dropQuote != "":
			if m := statusID.FindStringSubmatch(target); m != nil && m[1] == dropQuote {
				target = ""
			}
		}
		text = strings.ReplaceAll(text, u.URL, target)
	}
	text = html.UnescapeString(text) // the API sends &amp; &lt; &gt;
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(threeNL.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// media returns the post's attached media in order. missing is true when the post names media that the answer does not describe.
func (p rawPost) media(all map[string]rawMedia) (out []Media, missing bool) {
	for _, key := range p.Attachments.MediaKeys {
		m, ok := all[key]
		if !ok {
			missing = true
			continue
		}
		item := Media{Type: m.Type, URL: m.URL, Preview: m.Preview, Alt: m.Alt, Width: m.Width, Height: m.Height, DurationMS: m.DurationMS}
		for _, v := range m.Variants {
			item.Variants = append(item.Variants, Variant{URL: v.URL, ContentType: v.ContentType, BitRate: v.BitRate})
		}
		out = append(out, item)
	}
	return out, missing
}

// kindOf decides the kind from the post's references. A repost wins over a reply, and a reply over a quote. The "RT @" text is the fallback when the references are missing.
func kindOf(refs []rawRef, text string) Kind {
	has := func(t string) bool {
		return slices.ContainsFunc(refs, func(r rawRef) bool { return r.Type == t })
	}
	switch {
	case has("retweeted"), len(refs) == 0 && strings.HasPrefix(text, "RT @"):
		return Repost
	case has("replied_to"):
		return Reply
	case has("quoted"):
		return Quote
	}
	return Original
}

// idLess compares two post IDs as numbers (they are decimal strings that grow with time, so a longer one is newer).
func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// Newer reports whether post ID a is newer than b. An empty b is older than everything.
func Newer(a, b string) bool { return idLess(b, a) }

// ---- HTTP -------------------------------------------------------------------

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + "/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("x %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("x %s: %w", path, withoutURL(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("x %s: read response: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return newStatusError(path, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("x %s: unreadable response: %w", path, err)
	}
	return nil
}

// withoutURL drops the request URL from a network error. The URL itself holds no secret (the token travels in a header), but it is noise in a log.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

type statusError struct {
	path    string
	status  int
	message string
}

func (e *statusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("x %s: HTTP %d", e.path, e.status)
	}
	text := e.message
	if r := []rune(text); len(r) > 300 {
		text = string(r[:300]) + "…"
	}
	return fmt.Sprintf("x %s: HTTP %d: %s", e.path, e.status, text)
}

// Status is the HTTP status of the answer. Other packages ask for it through a small interface, so they do not need to know this type.
func (e *statusError) Status() int { return e.status }

func newStatusError(path string, status int, body []byte) error {
	var e struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(body, &e) // an unreadable body just leaves the message empty
	var parts []string
	for _, s := range []string{e.Title, e.Detail} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	for _, x := range e.Errors {
		if x.Message != "" {
			parts = append(parts, x.Message)
		}
	}
	return &statusError{path: path, status: status, message: strings.Join(parts, ": ")}
}
