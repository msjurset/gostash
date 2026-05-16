package feeds

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ResolveYouTubeFeed turns a user-friendly YouTube channel URL into
// the RSS feed URL that the RSS parser understands. Accepts:
//
//   - https://www.youtube.com/feeds/videos.xml?channel_id=UC...  (passthrough)
//   - https://www.youtube.com/channel/UC...                      (extract ID)
//   - https://www.youtube.com/@handle                            (HTML scrape)
//   - https://www.youtube.com/user/legacy                        (HTML scrape)
//   - https://www.youtube.com/c/custom                           (HTML scrape)
//
// The HTML-scrape path fetches the channel page and pulls
// `"channelId":"UC..."` out of the embedded metadata. YouTube's HTML
// has carried this same shape for years; if Google ever changes it,
// we fail clean (returns the original URL with an error) so the user
// can paste the feed URL directly as a fallback.
func ResolveYouTubeFeed(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty URL")
	}
	// Allow scheme-less input (`youtube.com/@channel`) since that's
	// what users typically paste from the address bar. `url.Parse`
	// puts a scheme-less authority into Path, not Host, and our
	// host-suffix check silently fails on the empty host.
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	host := strings.ToLower(u.Host)
	if !strings.HasSuffix(host, "youtube.com") && !strings.HasSuffix(host, "youtu.be") {
		return "", fmt.Errorf("not a YouTube URL: %s", host)
	}

	// Pre-cooked feed URL: pass through.
	if strings.HasPrefix(u.Path, "/feeds/videos.xml") && u.Query().Get("channel_id") != "" {
		return raw, nil
	}

	// /channel/UC... → direct ID extract.
	if strings.HasPrefix(u.Path, "/channel/") {
		id := strings.TrimPrefix(u.Path, "/channel/")
		if i := strings.IndexAny(id, "/?"); i >= 0 {
			id = id[:i]
		}
		if isChannelID(id) {
			return "https://www.youtube.com/feeds/videos.xml?channel_id=" + id, nil
		}
		return "", fmt.Errorf("malformed /channel/ URL: %q", raw)
	}

	// /@handle, /user/X, /c/X — scrape HTML for the channelId.
	id, err := scrapeChannelID(raw)
	if err != nil {
		return "", err
	}
	return "https://www.youtube.com/feeds/videos.xml?channel_id=" + id, nil
}

var channelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

func isChannelID(s string) bool { return channelIDPattern.MatchString(s) }

var (
	// Three patterns that have all been seen in the channel page's
	// embedded JSON over the years. First match wins.
	scrapePatterns = []*regexp.Regexp{
		regexp.MustCompile(`"channelId":"(UC[A-Za-z0-9_-]{22})"`),
		regexp.MustCompile(`"externalId":"(UC[A-Za-z0-9_-]{22})"`),
		regexp.MustCompile(`channel/(UC[A-Za-z0-9_-]{22})`),
	}
)

// scrapeChannelID fetches the page and pulls a UC... id out of the
// embedded metadata. Times out fast — this only runs once when the
// user adds a source, not on every poll.
func scrapeChannelID(rawURL string) (string, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "gostash-yt-resolver/1.0 (+https://github.com/msjurset/gostash)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 2MB cap. YouTube channel pages are typically 1-1.5MB.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", err
	}
	for _, re := range scrapePatterns {
		if m := re.FindSubmatch(body); m != nil {
			return string(m[1]), nil
		}
	}
	return "", fmt.Errorf("channel id not found on page")
}
