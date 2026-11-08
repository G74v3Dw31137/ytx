package ytx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/G74v3Dw31137/potter"
)

// ErrPlayerResponse indicates malformed or inconsistent Player metadata.
var ErrPlayerResponse = errors.New("ytx: invalid player response")

// ErrNoFormats means the response contains no formats in either supported array.
var ErrNoFormats = errors.New("ytx: player response has no formats")

// PlaybackError reports a non-OK playability status. Reason is remote text and is
// intentionally omitted from Error. GetVideo also returns the metadata result.
type PlaybackError struct{ Status, Reason string }

func (e *PlaybackError) Error() string { return "ytx: video is not playable" }

// GetVideo acquires exactly one atomic Potter snapshot and uses only its context,
// Player proof and homepage to fetch Player JS, extract STS, and request metadata.
// No ytx retries, JS execution, deciphering, URL resolution or media requests occur.
// PlaybackError and ErrNoFormats return a non-nil Video for inspection; other
// failures return nil. A borrowed Potter requires an explicit WithHTTPClient.
func (c *Client) GetVideo(ctx context.Context, videoID string) (*Video, error) {
	work, done, err := c.begin(ctx, videoID)
	if err != nil {
		return nil, err
	}
	defer done()
	if c.http == nil {
		return nil, ErrHTTPPolicy
	}
	snapshot, err := c.snapshot(work, videoID)
	if err != nil {
		return nil, err
	}
	target, err := discoverPlayerScript(snapshot)
	if err != nil {
		return nil, err
	}
	asset, err := c.playerAsset(work, target)
	if err != nil {
		return nil, err
	}
	data, err := c.requestPlayer(work, snapshot, asset.sts, videoID)
	if err != nil {
		return nil, err
	}
	v, err := parseVideo(data, snapshot, asset, videoID)
	if v != nil {
		v.owner = c.identity
	}
	if ctxErr := work.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return v, err
}

func setBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", potter.UserAgent)
	req.Header.Set("Origin", "https://www.youtube.com")
}

func (c *Client) requestPlayer(ctx context.Context, s SessionSnapshot, sts int, id string) ([]byte, error) {
	body := map[string]any{
		"context": s.config.Context, "videoId": id, "contentCheckOk": true, "racyCheckOk": true,
		"serviceIntegrityDimensions": map[string]string{"poToken": s.tokens.Player},
		"playbackContext":            map[string]any{"contentPlaybackContext": map[string]any{"signatureTimestamp": sts, "html5Preference": "HTML5_PREF_WANTS"}},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, ErrHomepageConfig
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Youtube-Client-Name", "2")
	headers.Set("X-Youtube-Client-Version", s.config.ClientVersion)
	headers.Set("X-Goog-Visitor-Id", s.config.VisitorData)
	raw, _, err := c.readMetadata(ctx, "player", http.MethodPost, playerEndpoint, bytes.NewReader(data), headers)
	return raw, err
}

func parseVideo(raw []byte, snapshot SessionSnapshot, asset playerAsset, id string) (*Video, error) {
	var p playerResponse
	if json.Unmarshal(raw, &p) != nil || p.Playability.Status == "" || (p.Details.VideoID != "" && p.Details.VideoID != id) {
		return nil, ErrPlayerResponse
	}
	asset.prepared = nil // Videos retain source, not resident compiled cache state.
	v := &Video{identity: &ownerToken{}, snapshot: snapshot, player: asset, response: raw}
	v.info = VideoInfo{ID: id, Title: p.Details.Title, Author: p.Details.Author, ChannelID: p.Details.ChannelID,
		LengthSeconds: p.Details.LengthSeconds, PlaybackStatus: p.Playability.Status, PlaybackReason: p.Playability.Reason}
	seconds, err := strconv.ParseInt(p.Details.LengthSeconds, 10, 64)
	if err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
		v.info.Duration = time.Duration(seconds) * time.Second
	}
	if p.Playability.Status != "OK" {
		return v, &PlaybackError{p.Playability.Status, p.Playability.Reason}
	}
	if p.Streaming == nil {
		return v, ErrNoFormats
	}
	v.info.ExpiresInSeconds = p.Streaming.ExpiresInSeconds
	for i, array := range [][]json.RawMessage{p.Streaming.Formats, p.Streaming.AdaptiveFormats} {
		for _, item := range array {
			var f Format
			if bytes.Equal(bytes.TrimSpace(item), []byte("null")) || json.Unmarshal(item, &f) != nil {
				return nil, ErrPlayerResponse
			}
			f.Adaptive = i == 1
			enrichFormat(&f)
			f.identity, f.ordinal = v.identity, len(v.formats)
			v.formats = append(v.formats, f)
		}
	}
	if len(v.formats) == 0 {
		return v, ErrNoFormats
	}
	return v, nil
}
