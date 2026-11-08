# ytx

[![Go Reference](https://pkg.go.dev/badge/github.com/G74v3Dw31137/ytx.svg)](https://pkg.go.dev/github.com/G74v3Dw31137/ytx)
[![CI](https://github.com/G74v3Dw31137/ytx/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/G74v3Dw31137/ytx/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fg74v3dw31137.github.io%2Fytx%2Fcoverage.json)](https://g74v3dw31137.github.io/ytx/coverage.json)
[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/G74v3Dw31137/ytx/blob/main/LICENSE)

ytx is a Go library for extracting YouTube video metadata and media streams. It uses [Potter](https://github.com/G74v3Dw31137/potter) for session management and proof tokens.

## Installation

Requires Go 1.27 or later.

```sh
go get github.com/G74v3Dw31137/ytx@v0.1.0
```

## Usage

Retrieve video metadata, select an audio format, and resolve its media URL:

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/G74v3Dw31137/ytx"
)

func main() {
    client, err := ytx.NewClient()
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    ctx := context.Background()

    video, err := client.GetVideo(ctx, "dQw4w9WgXcQ")
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(video.Info().Title)

    for _, format := range video.Formats() {
        if !format.HasAudio || format.HasVideo {
            continue
        }

        url, err := client.Resolve(ctx, video, format)
        if err != nil {
            log.Fatal(err)
        }

        fmt.Println(url)
        return
    }

    log.Fatal("no audio-only formats available")
}
```

`GetVideo` accepts a video ID, not a YouTube URL. `Video.Info()` provides metadata such as title, author, duration, and playback status.

`Video.Formats()` returns available media formats. You can inspect their codec, bitrate, resolution, and audio/video properties to select one.

`Resolve` returns a direct media URL without making additional network requests. URLs are temporary and may contain authorization tokens, so avoid logging or sharing them in production.

Formats returned by `Video.Formats()` must be passed unchanged to `Resolve` or `Open`, using the same client and video.

### Streaming

Use `Open` to read a media stream directly:

```go
stream, err := client.Open(ctx, video, format)
if err != nil {
    return err
}
defer stream.Close()

_, err = io.Copy(dst, stream)
return err
```

`Open` returns an `io.ReadCloser`. Streaming is sequential and does not support seeking. The caller is responsible for closing the stream.

Canceling the context interrupts the request and subsequent reads. Closing the ytx client does not close streams that have already been opened.

## Configuration

Clients can be reused across concurrent requests. Call `Close` when the client is no longer needed.

### HTTP client

By default, ytx uses a 30-second timeout for metadata and other bounded HTTP requests. Media streams opened with `Open` are not subject to this default timeout.

To use a custom HTTP client:

```go
httpClient := &http.Client{
    Transport: http.DefaultTransport,
}

client, err := ytx.NewClient(ytx.WithHTTPClient(httpClient))
```

A custom `http.Client.Timeout` also applies to `Open` and limits the entire stream duration. For long-running streams, leave it at zero and use contexts to control request lifetimes.

ytx shares the supplied client's transport, cookie jar, and redirect policy. The same HTTP configuration is used by its internally managed Potter client.

### Potter client

You can also provide an existing Potter client:

```go
client, err := ytx.NewClient(
    ytx.WithPotterClient(potterClient),
    ytx.WithHTTPClient(httpClient),
)
```

In this case, ytx does not take ownership of the Potter client. You must keep it open while using ytx and close it separately.

`WithHTTPClient` is required when supplying an existing Potter client, since ytx cannot access its HTTP configuration.

## Limitations

- SABR and manifest-based streaming are not supported.
- `Open` does not support seeking, Range requests, or automatic retries.
- Audio and video tracks are not muxed.
- YouTube may change its player or delivery protocols, requiring updates to ytx.

Videos with no supported formats return `ErrNoFormats`. `GetVideo` may return partial metadata alongside `ErrNoFormats` or `*PlaybackError`.

Errors support `errors.Is` and `errors.As`. HTTP errors expose their status through `*ytx.HTTPError`.

## License

ytx is licensed under the [MIT License](LICENSE). See [THIRD_PARTY.md](THIRD_PARTY.md) for third-party licenses and attribution.
