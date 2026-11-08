# Reused source

`internal/page/page.go`, `internal/page/page_lexer.go`, and
`testdata/protocol-mock.js` are adapted from
[Potter v0.2.0](https://github.com/G74v3Dw31137/potter/tree/v0.2.0),
commit `337a09c5cd221f6408087687d331bbd19db2d601`:
`internal/botguard/page.go`, `internal/botguard/page_lexer.go`, and
`internal/botguard/testdata/protocol-mock.js`, respectively.

The scanner changes only the package, local error construction and comments.
The synthetic protocol fixture is unchanged apart from attribution. It is not
BotGuard and produces no valid server proofs. No Potter session or challenge
implementation is copied into ytx.

`testdata/player-response.json` is the author's original synthetic response.
It contains fictitious `media.invalid` URLs and synthetic signature inputs.

`testdata/player-sts.js` and `testdata/resolve-player.js` are original
synthetic fixtures. No Google Player source excerpts, complete scripts, signed
media URLs, or proofs are distributed in this repository.

The MIT license below applies to the adapted Potter files listed above:

MIT License

Copyright (c) 2026 G74v3Dw31137

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.


Player execution provenance:

- `internal/player/prepare.go` adapts the preparation algorithm from
  [yt-dlp/ejs](https://github.com/yt-dlp/ejs), revision
  `d48c6303a5a6455a42ef9e0b445149cadea51279`. Its Unlicense notice is retained in
  [notices/ejs.txt](notices/ejs.txt).
- `testdata/d5-reference.json` contains transformation reference inputs and
  outputs generated with EJS/V8: 40 strings and six null results. These are data,
  not Google Player source code.
- Goja `github.com/dop251/goja v0.0.0-20261105002551-2413f2132ac8`
  is MIT licensed, Copyright (c) 2016 Dmitry Panov and (c) 2012 Robert Krimen.
  Its complete notice is retained in [notices/goja-LICENSE](notices/goja-LICENSE).
