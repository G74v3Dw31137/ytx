package ytx

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"
)

// Video is an immutable metadata result. It privately retains its atomic Potter
// snapshot, complete Player response and exact Player JS for later resolution.
// It holds no client or runtime lease and remains readable after Close. Keeping
// many results can retain substantial memory. URLs/proofs can expire remotely.
type ownerToken struct {
	_ byte
}

type Video struct {
	owner    *ownerToken
	identity *ownerToken
	info     VideoInfo
	formats  []Format
	snapshot SessionSnapshot
	player   playerAsset
	response []byte
}

// VideoInfo is a caller-owned metadata copy. Zero duration means unknown or zero;
// LengthSeconds preserves the original representation, including absence.
type VideoInfo struct {
	ID, Title, Author, ChannelID   string
	Duration                       time.Duration
	LengthSeconds                  string
	PlaybackStatus, PlaybackReason string
	ExpiresInSeconds               string
}

// VideoProvenance identifies the exact inputs retained with a video. Generation
// is scoped to its originating Potter client and promises no remote freshness.
type VideoProvenance struct {
	VideoID                                                    string
	Generation                                                 uint64
	DocumentURL, PlayerJSURL, PlayerJSFinalURL, PlayerJSSHA256 string
	SignatureTimestamp                                         int
}

// Info returns an independent metadata value.
func (v *Video) Info() VideoInfo { return v.info }

// Formats returns independent copies of both formats arrays, in response order.
// URL, SignatureCipher and Cipher are raw server values, not resolved media URLs.
// Keep these values private; do not assume a direct URL is ready for playback.
func (v *Video) Formats() []Format {
	formats := append([]Format(nil), v.formats...)
	for i := range formats {
		formats[i].Codecs = append([]string(nil), formats[i].Codecs...)
		if formats[i].AudioTrack != nil {
			track := *formats[i].AudioTrack
			formats[i].AudioTrack = &track
		}
		if formats[i].InitRange != nil {
			r := *formats[i].InitRange
			formats[i].InitRange = &r
		}
		if formats[i].IndexRange != nil {
			r := *formats[i].IndexRange
			formats[i].IndexRange = &r
		}
	}
	return formats
}

// Provenance returns the identities of the associated request inputs.
func (v *Video) Provenance() VideoProvenance {
	return VideoProvenance{VideoID: v.info.ID, Generation: v.snapshot.Generation(), DocumentURL: v.snapshot.DocumentURL(),
		PlayerJSURL: v.player.url, PlayerJSFinalURL: v.player.finalURL, PlayerJSSHA256: v.player.sha256, SignatureTimestamp: v.player.sts}
}

// Format retains wire metadata without filling missing values from itag tables.
// String-valued numbers remain strings to preserve absence and large values.
// Numeric/bool zero values can indicate either absence or an explicit zero.
// Adaptive records the originating array; it does not imply an audio/video kind.
// HasAudio/HasVideo describe positive metadata evidence, not guaranteed absence.
type Format struct {
	identity         *ownerToken
	ordinal          int
	Itag             int         `json:"itag"`
	URL              string      `json:"url"`
	SignatureCipher  string      `json:"signatureCipher"`
	Cipher           string      `json:"cipher"`
	MIMEType         string      `json:"mimeType"`
	Bitrate          int64       `json:"bitrate"`
	AverageBitrate   int64       `json:"averageBitrate"`
	Width            int         `json:"width"`
	Height           int         `json:"height"`
	FPS              int         `json:"fps"`
	ContentLength    string      `json:"contentLength"`
	AudioSampleRate  string      `json:"audioSampleRate"`
	AudioChannels    int         `json:"audioChannels"`
	Quality          string      `json:"quality"`
	QualityLabel     string      `json:"qualityLabel"`
	AudioQuality     string      `json:"audioQuality"`
	ApproxDurationMS string      `json:"approxDurationMs"`
	LastModified     string      `json:"lastModified"`
	ProjectionType   string      `json:"projectionType"`
	AudioTrack       *AudioTrack `json:"audioTrack"`
	InitRange        *ByteRange  `json:"initRange"`
	IndexRange       *ByteRange  `json:"indexRange"`
	Codecs           []string    `json:"-"`
	HasAudio         bool        `json:"-"`
	HasVideo         bool        `json:"-"`
	Adaptive         bool        `json:"-"`
}

// AudioTrack retains optional audio track identity and default selection.
type AudioTrack struct {
	ID             string `json:"id"`
	DisplayName    string `json:"displayName"`
	AudioIsDefault bool   `json:"audioIsDefault"`
}

// ByteRange retains the inclusive offsets supplied by the Player response.
// It does not perform a media range request.
type ByteRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func (v Video) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, "ytx.Video{redacted}")
}
func (f Format) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, "ytx.Format{redacted}")
}

func enrichFormat(f *Format) {
	mediaType, params, err := mime.ParseMediaType(f.MIMEType)
	if err == nil {
		if value := params["codecs"]; value != "" {
			for codec := range strings.SplitSeq(value, ",") {
				f.Codecs = append(f.Codecs, strings.TrimSpace(codec))
			}
		}
		f.HasAudio = strings.HasPrefix(mediaType, "audio/")
		f.HasVideo = strings.HasPrefix(mediaType, "video/")
	}
	f.HasAudio = f.HasAudio || f.AudioChannels > 0 || f.AudioSampleRate != "" || f.AudioQuality != "" || f.AudioTrack != nil
	f.HasVideo = f.HasVideo || f.Width > 0 || f.Height > 0
	for _, codec := range f.Codecs {
		c := strings.ToLower(codec)
		switch {
		case strings.HasPrefix(c, "mp4a"), c == "opus", c == "vorbis", c == "ac-3", c == "ec-3", c == "flac":
			f.HasAudio = true
		case strings.HasPrefix(c, "avc1"), strings.HasPrefix(c, "avc3"), strings.HasPrefix(c, "av01"), strings.HasPrefix(c, "vp09"), c == "vp9", c == "vp8", strings.HasPrefix(c, "hev1"), strings.HasPrefix(c, "hvc1"), strings.HasPrefix(c, "mp4v"):
			f.HasVideo = true
		}
	}
}

type playerResponse struct {
	Playability struct{ Status, Reason string }                                   `json:"playabilityStatus"`
	Details     struct{ VideoID, Title, Author, ChannelID, LengthSeconds string } `json:"videoDetails"`
	Streaming   *struct {
		Formats, AdaptiveFormats []json.RawMessage
		ExpiresInSeconds         string
	} `json:"streamingData"`
}
