// Package xapi reads a user's posts from the X (Twitter) API v2.
//
// X bills reads per post returned, so every call asks only for what is new (since_id) and the caller counts what came back.
package xapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
}

// Page is what one call returned.
type Page struct {
	Posts []Post // oldest first
	// Billed is how many posts X most likely charged for: the posts returned plus the originals that came with them.
	Billed int
}

// Client talks to the API with one app-only bearer token.
type Client struct {
	token   string
	baseURL string // a field so tests can point it at a fake server
	http    *http.Client
	// X's documentation calls the parameter "post.fields" in places and the API has long used "tweet.fields". We start with the long-standing one and switch once if X says it does not know it.
	fieldsParam string
}

func NewClient(bearerToken string) *Client {
	return &Client{
		token:       bearerToken,
		baseURL:     defaultBaseURL,
		http:        &http.Client{Timeout: 15 * time.Second},
		fieldsParam: "tweet.fields",
	}
}

// UserID looks a handle up (without the @). It costs one user read, so the caller should keep the answer.
func (c *Client) UserID(ctx context.Context, username string) (string, error) {
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.get(ctx, "users/by/username/"+url.PathEscape(username), nil, &out); err != nil {
		return "", err
	}
	if out.Data.ID == "" {
		return "", fmt.Errorf("x user lookup: no account named @%s", username)
	}
	return out.Data.ID, nil
}

// Posts returns the user's posts newer than sinceID, oldest first. With an empty sinceID it returns the 5 newest (the smallest page X allows), which is enough to learn where "now" is.
func (c *Client) Posts(ctx context.Context, userID, sinceID string) (Page, error) {
	page, err := c.posts(ctx, userID, sinceID)
	var se *statusError
	if errors.As(err, &se) && se.status == http.StatusBadRequest && c.swapFieldsParam(se.message) {
		return c.posts(ctx, userID, sinceID)
	}
	return page, err
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
	q := url.Values{}
	q.Set(c.fieldsParam, "created_at,entities,referenced_tweets")
	q.Set("expansions", "referenced_tweets.id")
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

type rawPost struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	Entities  struct {
		URLs []struct {
			Expanded string `json:"expanded_url"`
			Unwound  string `json:"unwound_url"`
		} `json:"urls"`
	} `json:"entities"`
	// X's documentation has used both names for this list.
	RefTweets []rawRef `json:"referenced_tweets"`
	RefPosts  []rawRef `json:"referenced_posts"`
}

type rawRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type response struct {
	Data     []rawPost `json:"data"`
	Includes struct {
		Tweets []rawPost `json:"tweets"`
		Posts  []rawPost `json:"posts"`
	} `json:"includes"`
}

func (r response) page() Page {
	originals := map[string]rawPost{}
	for _, p := range append(append([]rawPost(nil), r.Includes.Tweets...), r.Includes.Posts...) {
		originals[p.ID] = p
	}
	var out []Post
	for _, p := range r.Data {
		post := Post{ID: p.ID, Text: p.Text, Kind: Original, URLs: p.urls()}
		if t, err := time.Parse(time.RFC3339, p.CreatedAt); err == nil {
			post.CreatedAt = t
		}
		refs := append(append([]rawRef(nil), p.RefTweets...), p.RefPosts...)
		post.Kind = kindOf(refs, p.Text)
		for _, ref := range refs {
			if ref.Type == "retweeted" || ref.Type == "quoted" {
				if orig, ok := originals[ref.ID]; ok {
					post.URLs = append(post.URLs, orig.urls()...)
				}
			}
		}
		out = append(out, post)
	}
	sort.Slice(out, func(i, j int) bool { return idLess(out[i].ID, out[j].ID) })
	return Page{Posts: out, Billed: len(r.Data) + len(originals)}
}

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

// kindOf decides the kind from the post's references. A repost wins over a reply, and a reply over a quote. The "RT @" text is the fallback when the references are missing.
func kindOf(refs []rawRef, text string) Kind {
	has := func(t string) bool {
		for _, r := range refs {
			if r.Type == t {
				return true
			}
		}
		return false
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

// Status is the HTTP status of an error from X, or 0 for any other error.
func Status(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.status
	}
	return 0
}

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
