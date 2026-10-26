// Package youtube reads a channel's recent streams from the YouTube Data API.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
)

const (
	defaultBaseURL = "https://www.googleapis.com/youtube/v3"

	// The most videos the API lists per call. A call costs 1 quota unit whether it returns 5 videos or 50, so we always ask for the most.
	pageSize = 50

	// MaxPages caps how deep one RecentStreams call may look (5 x 50 = 250 videos).
	MaxPages = 5
)

// A channel ID is "UC" followed by 22 letters, digits, "-" or "_".
var channelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

// YouTube writes lengths like PT1H2M3S (1h 2m 3s), PT45S, or P0D (nothing).
var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

type Client struct {
	apiKey  string
	baseURL string // a field so tests can point it at a fake server
	http    *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// RecentStreams looks at the channel's newest uploads, `pages` pages of 50 (1 to MaxPages), and returns the live streams and premieres among them in the order YouTube lists them (newest first). Each one's Kind says which.
//
// Uploads include Shorts and clips, so 50 uploads is not 50 streams, and a stream older than the window is simply not seen.
//
// Telling streams from premieres: YouTube describes both the same way, but every channel also has a hidden playlist that is exactly its Live tab (premieres are not on it). See kindOf for how the two are told apart.
//
// Cost per page: 1 unit for the uploads list, 1 for the details, and 1 for the Live tab list.
func (c *Client) RecentStreams(ctx context.Context, channelID string, pages int) (broadcast.Scan, error) {
	if !channelIDPattern.MatchString(channelID) {
		return broadcast.Scan{}, errors.New("channel ID must start with UC and be 24 characters long")
	}
	pages = min(max(pages, 1), MaxPages)

	// Every channel has playlists whose IDs are the channel ID with the leading "UC" replaced: "UU" = all uploads, "UULV" = the Live tab.
	rest := channelID[2:]
	liveTab, err := c.videoIDs(ctx, "UULV"+rest, pages)
	if err != nil {
		return broadcast.Scan{}, err
	}

	var scan broadcast.Scan
	pageToken := ""
	for page := 0; page < pages; page++ {
		ids, next, err := c.playlistPage(ctx, "UU"+rest, pageToken)
		if err != nil {
			return broadcast.Scan{}, err
		}
		if len(ids) > 0 {
			found, err := c.streamsAmong(ctx, ids, liveTab)
			if err != nil {
				return broadcast.Scan{}, err
			}
			scan.Streams = append(scan.Streams, found...)
			scan.Checked += len(ids)
		}
		if next == "" { // that was the last page
			break
		}
		pageToken = next
	}

	if len(liveTab) == 0 {
		for _, st := range scan.Streams {
			if st.State == broadcast.Ended {
				scan.Notes = append(scan.Notes,
					"The Live tab list could not be read, so premieres cannot be told apart from streams that already ended.")
				break
			}
		}
	}
	return scan, nil
}

// videoIDs collects the video IDs on up to `pages` pages of a playlist. A playlist that does not exist (for example a channel that never streamed) is an empty result, not an error.
func (c *Client) videoIDs(ctx context.Context, playlistID string, pages int) (map[string]bool, error) {
	set := map[string]bool{}
	pageToken := ""
	for page := 0; page < pages; page++ {
		ids, next, err := c.playlistPage(ctx, playlistID, pageToken)
		if err != nil {
			var se *statusError
			if errors.As(err, &se) && se.status == http.StatusNotFound {
				return set, nil
			}
			return nil, err
		}
		for _, id := range ids {
			set[id] = true
		}
		if next == "" {
			break
		}
		pageToken = next
	}
	return set, nil
}

// playlistPage returns the video IDs on one page of a playlist, and the
// token for the next page ("" when there is none).
func (c *Client) playlistPage(ctx context.Context, playlistID, pageToken string) (ids []string, next string, err error) {
	q := url.Values{
		"part":       {"contentDetails"},
		"playlistId": {playlistID},
		"maxResults": {strconv.Itoa(pageSize)},
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	var list playlistItemsResponse
	if err := c.get(ctx, "playlistItems", q, &list); err != nil {
		return nil, "", err
	}
	for _, item := range list.Items {
		ids = append(ids, item.ContentDetails.VideoID)
	}
	return ids, list.NextPageToken, nil
}

// streamsAmong asks for the details of these videos (up to 50 at once) and keeps the streams and premieres, in the order of ids. Only those carry liveStreamingDetails, which is how we tell them from ordinary uploads.
func (c *Client) streamsAmong(ctx context.Context, ids []string, liveTab map[string]bool) ([]broadcast.Stream, error) {
	var details videosResponse
	err := c.get(ctx, "videos", url.Values{
		"part": {"snippet,liveStreamingDetails,contentDetails"},
		"id":   {strings.Join(ids, ",")},
	}, &details)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]video, len(details.Items))
	for _, v := range details.Items {
		byID[v.ID] = v
	}

	var streams []broadcast.Stream
	for _, id := range ids { // YouTube may reorder its answer, so walk our own order
		if v, ok := byID[id]; ok {
			if st, isStream := toStream(v, liveTab); isStream {
				streams = append(streams, st)
			}
		}
	}
	return streams, nil
}

// toStream turns YouTube's video record into our own Stream. The second result is false for ordinary uploads and for states we do not understand.
func toStream(v video, liveTab map[string]bool) (broadcast.Stream, bool) {
	d := v.LiveStreamingDetails
	if d == nil {
		return broadcast.Stream{}, false
	}
	st := broadcast.Stream{VideoID: v.ID, Title: v.Snippet.Title}

	switch v.Snippet.LiveBroadcastContent {
	case "upcoming":
		st.State = broadcast.Upcoming
		st.StartAt = firstTime(d.ScheduledStartTime, d.ActualStartTime)
	case "live":
		st.State = broadcast.Live
		st.StartAt = firstTime(d.ActualStartTime, d.ScheduledStartTime)
	case "none": // a stream with details that is no longer live has ended
		st.State = broadcast.Ended
		st.StartAt = firstTime(d.ActualStartTime, d.ScheduledStartTime)
	default:
		return broadcast.Stream{}, false
	}
	st.Kind = kindOf(st.State, v.ID, v.ContentDetails.Duration, liveTab)
	return st, true
}

// kindOf decides stream or premiere, using two clues.
//
//  1. On the Live tab list: a stream. (Premieres are listed under Videos.)
//  2. Otherwise it depends on the state:
//     - Upcoming or live: a stream has no length yet ("P0D"), but a premiere
//     is a finished video and already has one.
//     - Ended: not on the Live tab, so a premiere. If we could not read the
//     Live tab at all, we cannot tell and call it a stream, as before.
//
// Weak spot: a stream that ended a minute ago may not be on the Live tab list yet. The poller will therefore remember what it decided while the stream was still live, instead of asking again.
func kindOf(state broadcast.State, id, duration string, liveTab map[string]bool) broadcast.Kind {
	if liveTab[id] {
		return broadcast.KindStream
	}
	switch state {
	case broadcast.Upcoming, broadcast.Live:
		if length, ok := parseDuration(duration); ok && length > 0 {
			return broadcast.KindPremiere
		}
		return broadcast.KindStream
	default:
		if len(liveTab) == 0 {
			return broadcast.KindStream
		}
		return broadcast.KindPremiere
	}
}

// parseDuration reads YouTube's ISO 8601 lengths such as "PT1H2M3S".
func parseDuration(s string) (time.Duration, bool) {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil || s == "P" {
		return 0, false
	}
	n := func(i int) time.Duration {
		v, _ := strconv.Atoi(m[i]) // empty (a missing part) reads as 0
		return time.Duration(v)
	}
	return n(1)*24*time.Hour + n(2)*time.Hour + n(3)*time.Minute + n(4)*time.Second, true
}

// firstTime returns the first value that parses as a time, else the zero time.
func firstTime(values ...string) time.Time {
	for _, v := range values {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// get calls one API endpoint and decodes the JSON answer into out.
func (c *Client) get(ctx context.Context, endpoint string, q url.Values, out any) error {
	q.Set("key", c.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/"+endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("youtube %s: %w", endpoint, withoutURL(err))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("youtube %s: %w", endpoint, withoutURL(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("youtube %s: read response: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return newStatusError(endpoint, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("youtube %s: unreadable response: %w", endpoint, err)
	}
	return nil
}

// withoutURL strips the request URL from a network error. net/http errors quote the whole URL, and ours contains the API key, which must never reach a log or a Discord message.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// statusError is YouTube answering with an error status. It is its own type so callers can look at the status (see videoIDs).
type statusError struct {
	endpoint string
	status   int
	reason   string // YouTube's short code, such as "quotaExceeded"
	message  string
}

// apiError turns an error answer from YouTube into a short readable error.
func (e *statusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("youtube %s: HTTP %d", e.endpoint, e.status)
	}
	reason := ""
	if e.reason != "" {
		reason = " (" + e.reason + ")"
	}
	text := e.message
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "…"
	}
	return fmt.Sprintf("youtube %s: HTTP %d%s: %s", e.endpoint, e.status, reason, text)
}

func newStatusError(endpoint string, status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	se := &statusError{endpoint: endpoint, status: status}
	if json.Unmarshal(body, &e) == nil {
		se.message = e.Error.Message
		if len(e.Error.Errors) > 0 {
			se.reason = e.Error.Errors[0].Reason
		}
	}
	return se
}

// The shapes below are only the parts of YouTube's answers that we read. encoding/json ignores everything else.

type playlistItemsResponse struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []struct {
		ContentDetails struct {
			VideoID string `json:"videoId"`
		} `json:"contentDetails"`
	} `json:"items"`
}

type videosResponse struct {
	Items []video `json:"items"`
}

type video struct {
	ID      string `json:"id"`
	Snippet struct {
		Title string `json:"title"`
		// "upcoming", "live" or "none"
		LiveBroadcastContent string `json:"liveBroadcastContent"`
	} `json:"snippet"`
	ContentDetails struct {
		Duration string `json:"duration"` // "P0D" while a stream has no length yet
	} `json:"contentDetails"`
	// A pointer, so a plain upload (no such field) is nil instead of empty.
	LiveStreamingDetails *struct {
		ScheduledStartTime string `json:"scheduledStartTime"`
		ActualStartTime    string `json:"actualStartTime"`
		ActualEndTime      string `json:"actualEndTime"`
	} `json:"liveStreamingDetails"`
}
