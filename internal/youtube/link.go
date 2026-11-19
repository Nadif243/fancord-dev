package youtube

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

	// A YouTube link inside a piece of text, with or without "https://".
	linkInText = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.|m\.|music\.)?(?:youtube\.com|youtu\.be)/[^\s<>()\[\]]+`)
)

// VideoID reads a video ID from a YouTube link: watch?v=, youtu.be/, /live/, /shorts/ and /embed/. It reports false for anything else (a channel, a playlist, another site, or an ID that is not 11 characters).
func VideoID(link string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), "m.")
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var id string
	switch {
	case host == "youtu.be":
		id = parts[0]
	case host == "youtube.com" || host == "music.youtube.com":
		switch {
		case parts[0] == "watch":
			id = u.Query().Get("v")
		case len(parts) >= 2 && (parts[0] == "live" || parts[0] == "shorts" || parts[0] == "embed"):
			id = parts[1]
		}
	}
	return id, videoIDPattern.MatchString(id)
}

// VideoIDs reads the videos from a list of links, once each, in order. Links that are not videos are skipped.
func VideoIDs(links []string) []string {
	var out []string
	for _, l := range links {
		if id, ok := VideoID(l); ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// VideoIDsIn finds the YouTube links in a piece of text and reads the videos from them, once each, in the order they appear.
func VideoIDsIn(text string) []string {
	var links []string
	for _, raw := range linkInText.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ".,;:!?'\"")
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		links = append(links, raw)
	}
	return VideoIDs(links)
}
