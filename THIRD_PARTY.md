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

## Player execution provenance

`internal/player/prepare.go` adapts the preparation algorithm from
[yt-dlp/ejs](https://github.com/yt-dlp/ejs), revision
`d48c6303a5a6455a42ef9e0b445149cadea51279`. Its complete Unlicense notice
appears below.

`testdata/d5-reference.json` contains transformation reference inputs and
outputs generated with EJS/V8: 40 strings and six null results. These are data,
not Google Player source code.

### yt-dlp/ejs — Unlicense

```text
This is free and unencumbered software released into the public domain.

Anyone is free to copy, modify, publish, use, compile, sell, or
distribute this software, either in source code form or as a compiled
binary, for any purpose, commercial or non-commercial, and by any
means.

In jurisdictions that recognize copyright laws, the author or authors
of this software dedicate any and all copyright interest in the
software to the public domain. We make this dedication for the benefit
of the public at large and to the detriment of our heirs and
successors. We intend this dedication to be an overt act of
relinquishment in perpetuity of all present and future rights to this
software under copyright law.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS BE LIABLE FOR ANY CLAIM, DAMAGES OR
OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
OTHER DEALINGS IN THE SOFTWARE.

For more information, please refer to <https://unlicense.org/>
```

### Goja — MIT

Goja `github.com/dop251/goja v0.0.0-20261105002551-2413f2132ac8` is an
external Go dependency. Its module-root MIT notice, retained from ytx's
existing attribution, follows. Additional notices apply when redistributing
Goja's implementation, as described below.

```text
Copyright (c) 2016 Dmitry Panov

Copyright (c) 2012 Robert Krimen

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
documentation files (the "Software"), to deal in the Software without restriction, including without limitation
the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE
WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR
OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

## External dependencies and binary redistribution

External Go modules referenced by `go.mod` and `go.sum` are downloaded
separately; their implementations are not vendored in this repository.
This includes Potter's embedded BgUtils and LinkeDOM JavaScript bundles.
The adapted Potter files listed above contain neither bundle and are covered
by the MIT notice in this document.

Redistributed ytx sources must retain `LICENSE`, this document, and the
existing source notices. The ytx source tree does not itself distribute the
implementations of its external dependencies.

If distributing binaries or vendored dependencies, include the complete
applicable license texts, copyright notices, and any required upstream NOTICE
contents for the code and data actually included, including embedded JavaScript
and the Go runtime. Consult each included module's license and notice files;
[Potter v0.2.0's third-party documentation](https://github.com/G74v3Dw31137/potter/blob/337a09c5cd221f6408087687d331bbd19db2d601/THIRD_PARTY.md)
details its embedded components and linked-dependency obligations. Goja also
has additional notices in `ftoa/LICENSE_LUCENE` (Lucent Technologies) and
`ftoa/internal/fast/LICENSE_V8` (V8).

These references identify upstream materials; links do not replace the complete
texts required with a binary or vendored distribution. Go does not automatically
embed legal documents in binaries. This document alone is not a complete notice
bundle for such distributions.
