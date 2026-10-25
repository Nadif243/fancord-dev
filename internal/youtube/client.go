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

	// MaxPages caps how deep one RecentStreams call may look (5 x 50 = 250 videos, at most 10 quota units).
	MaxPages = 5
)

// A channel ID is "UC" followed by 22 letters, digits, "-" or "_".
var channelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

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

// RecentStreams looks at the channel's newest uploads, `pages` pages of 50 (1 to MaxPages), and returns the ones that are (or were, or will be) live streams or premieres, in the order YouTube lists them (newest first).
// checked is how many uploads it looked at, which helps when debugging.
//
// Uploads include Shorts and clips, so 50 uploads is not 50 streams, and a stream older than the window is simply not seen. Each page costs 2 quota units: one call for the list, one for the details.
func (c *Client) RecentStreams(ctx context.Context, channelID string, pages int) (checked int, streams []broadcast.Stream, err error) {
	if !channelIDPattern.MatchString(channelID) {
		return 0, nil, errors.New("channel ID must start with UC and be 24 characters long")
	}
	pages = min(max(pages, 1), MaxPages)

	// Every channel has an "uploads" playlist whose ID is the channel ID with the leading "UC" replaced by "UU".
	playlistID := "UU" + channelID[2:]

	pageToken := ""
	for page := 0; page < pages; page++ {
		ids, next, err := c.uploadsPage(ctx, playlistID, pageToken)
		if err != nil {
			return 0, nil, err
		}
		if len(ids) > 0 {
			found, err := c.streamsAmong(ctx, ids)
			if err != nil {
				return 0, nil, err
			}
			streams = append(streams, found...)
			checked += len(ids)
		}
		if next == "" { // that was the last page
			break
		}
		pageToken = next
	}
	return checked, streams, nil
}

// uploadsPage returns the video IDs on one page of the uploads playlist, and the token for the next page ("" when there is none).
func (c *Client) uploadsPage(ctx context.Context, playlistID, pageToken string) (ids []string, next string, err error) {
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

// streamsAmong asks for the details of these videos (up to 50 at once) and keeps the streams, in the order of ids. Only streams carry liveStreamingDetails, which is how we tell them from ordinary uploads.
func (c *Client) streamsAmong(ctx context.Context, ids []string) ([]broadcast.Stream, error) {
	var details videosResponse
	err := c.get(ctx, "videos", url.Values{
		"part": {"snippet,liveStreamingDetails"},
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
			if st, isStream := toStream(v); isStream {
				streams = append(streams, st)
			}
		}
	}
	return streams, nil
}

// toStream turns YouTube's video record into our own Stream. The second result is false for ordinary uploads and for states we do not understand.
func toStream(v video) (broadcast.Stream, bool) {
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
	return st, true
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
		return apiError(endpoint, resp.StatusCode, body)
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

// apiError turns an error answer from YouTube into a short readable error.
func apiError(endpoint string, status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Message == "" {
		return fmt.Errorf("youtube %s: HTTP %d", endpoint, status)
	}
	reason := ""
	if len(e.Error.Errors) > 0 {
		reason = " (" + e.Error.Errors[0].Reason + ")"
	}
	text := e.Error.Message
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "…"
	}
	return fmt.Errorf("youtube %s: HTTP %d%s: %s", endpoint, status, reason, text)
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
	// A pointer, so a plain upload (no such field) is nil instead of empty.
	LiveStreamingDetails *struct {
		ScheduledStartTime string `json:"scheduledStartTime"`
		ActualStartTime    string `json:"actualStartTime"`
		ActualEndTime      string `json:"actualEndTime"`
	} `json:"liveStreamingDetails"`
}
