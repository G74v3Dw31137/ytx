package ytx

import (
	"context"
	"errors"
	"reflect"

	"github.com/G74v3Dw31137/ytx/internal/player"
)

// Resolution errors contain fixed stage information, never URLs, proofs or JS stacks.
var (
	ErrOwnership             = errors.New("ytx: invalid video or format ownership")
	ErrFormatMutation        = errors.New("ytx: mutated format descriptor")
	ErrUnsupportedFormat     = errors.New("ytx: unsupported format source")
	ErrInvalidCipher         = errors.New("ytx: invalid signature cipher")
	ErrInvalidURL            = errors.New("ytx: invalid media URL")
	ErrMissingGVS            = errors.New("ytx: missing originating GVS proof")
	ErrUnsupportedPlayerJS   = player.ErrUnsupported
	ErrAmbiguousDiscovery    = player.ErrAmbiguous
	ErrPreparation           = player.ErrPreparation
	ErrRuntimeInitialization = player.ErrInitialization
	ErrSignature             = errors.New("ytx: signature transformation failed")
	ErrN                     = errors.New("ytx: n transformation failed")
	ErrExpiredURL            = errors.New("ytx: expired media URL")
)

// Resolve builds one media URL using only the video's retained Player JS and
// originating GVS proof. It performs no HTTP or Potter calls. Format must be an
// unchanged copy returned by video.Formats. Independent resolutions use fresh
// JavaScript runtimes. Success establishes local construction, not remote access.
// Callers must not mutate a descriptor concurrently with this call.
func (c *Client) Resolve(ctx context.Context, video *Video, format Format) (result string, err error) {
	work, done, err := c.begin(ctx, "resolve")
	if err != nil {
		return "", err
	}
	defer done()
	defer func() {
		if err != nil {
			result = ""
			if c.life.Err() != nil {
				err = errors.Join(err, ErrClosed)
			}
		}
	}()
	if video == nil || video.owner == nil || video.owner != c.identity || video.identity == nil || format.identity != video.identity || format.ordinal < 0 || format.ordinal >= len(video.formats) {
		return "", ErrOwnership
	}
	canonical := video.formats[format.ordinal]
	if !reflect.DeepEqual(format, canonical) {
		return "", ErrFormatMutation
	}
	input, e := parseMedia(canonical)
	if e != nil {
		return "", e
	}
	query, e := newMediaQuery(input.raw)
	if e != nil {
		return "", e
	}
	if e = query.validate(input.sp, input.s != ""); e != nil {
		return "", e
	}
	proof := video.snapshot.tokens.GVS
	if proof == "" {
		return "", ErrMissingGVS
	}
	if len(proof) > player.MaxValue {
		return "", ErrInvalidURL
	}
	if old, exists, e := query.value("pot"); e != nil {
		return "", e
	} else if exists && old != proof {
		return "", ErrInvalidURL
	}
	n, _, e := query.value("n")
	if e != nil {
		return "", e
	}
	if input.s != "" || n != "" {
		program, e := c.preparedPlayer(work, video.player)
		if e != nil {
			return "", e
		}
		// Client-wide admission spans initialization, both transforms and interrupt cleanup.
		select {
		case c.resolveSlots <- struct{}{}:
		case <-work.Done():
			return "", work.Err()
		}
		defer func() { <-c.resolveSlots }()
		if e = work.Err(); e != nil {
			return "", e
		}
		initialize := c.initializeRuntime
		if initialize == nil {
			initialize = player.NewSession
		}
		session, e := initialize(work, program)
		if e != nil {
			return "", e
		}
		if input.s != "" {
			sig, e := session.Signature(work, input.s)
			if e != nil {
				return "", errors.Join(ErrSignature, e)
			}
			if e = query.edit(input.sp, sig); e != nil {
				return "", e
			}
		}
		if n != "" {
			value, e := session.N(work, n)
			if e != nil {
				return "", errors.Join(ErrN, e)
			}
			if e = query.edit("n", value); e != nil {
				return "", e
			}
		}
	}
	if e = query.edit("pot", proof); e != nil {
		return "", e
	}
	if e = work.Err(); e != nil {
		return "", e
	}
	return query.String(), nil
}
