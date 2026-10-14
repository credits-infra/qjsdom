# Architecture

## What happens on a fetch

```
URL
 │
 ├─ fetcher ──── HTTP with SSRF guard and retry
 │                  │
 │                  └─ cache lookup first (if enabled)
 │
 ▼
HTML source
 │
 ├─ renderer ─── fresh qjs runtime
 │                  │
 │                  1. eval host_shims.js
 │                  2. eval linkedom bundle
 │                  3. register __qjsdom_imageProbe (a Go callback)
 │                  4. eval page_runner.js with HTML, URL, selector, mode
 │                  5. close the runtime
 │
 ▼
Result
 │
 ├─ optional: download images
 │
 ▼
text, links, images
```

The renderer holds no DOM logic. It loads JavaScript, hands the page over, and
reads JSON back.

## Packages

| Package | Responsibility |
| --- | --- |
| `internal/fetcher` | HTTP, SSRF guard, retry, image probe and download |
| `internal/renderer` | qjs lifetime, script injection, result decoding |
| `internal/cache` | Page source cache with TTL and size cap |
| `cmd/qjsdom` | Command line interface |
| `cmd/qjsdom-mcp` | MCP server |

## The JavaScript side

Three files carry the browser emulation, and all three are plain JavaScript.

`internal/renderer/js/host_shims.js` fills in the host APIs the DOM library and
page scripts expect. Buffer, TextEncoder, TextDecoder, process, URL, Image,
localStorage, history, requestAnimationFrame, MutationObserver, and others.
Each stub does the minimum needed to keep the page running. Anything we do not
need a result from, such as audio, gets an empty shell.

`internal/renderer/js/image_fetch.js` replaces the simple `Image` stub with one
that calls back into Go for real image dimensions. It caches per URL and caps how
many images it probes per page, because a page with hundreds of images would
otherwise stall.

`internal/renderer/js/page_runner.js` does the actual work: parse the HTML, run
each inline script in global scope, extract content according to `mode`, collect
links and image URLs. It runs inside a try block, and if the DOM library fails it
falls back to stripping tags.

The whole runner is wrapped in an IIFE whose result is a JSON string. Go reads
that string before closing the runtime, since values do not survive the close.

## The DOM bundle

`internal/renderer/assets/linkedom.core.js` is an esbuild artifact. It is the
host shims concatenated with linkedom, bundled as an IIFE.

Rebuild it with:

```bash
./scripts/build-bundle.sh
```

The script installs linkedom into a temporary directory, runs esbuild with
`--alias:canvas=` to satisfy an optional dependency, and prepends the shims. It
does not touch the repository's dependencies.

## Boundaries

A fake DOM has no layout. Pages that load content on scroll, or that measure
element geometry to decide what to show, come back thin.

There is no `fetch`, no `XMLHttpRequest`, and no History API, so a pure
client-side single page app renders as an empty shell. Server-rendered apps work,
which covers Next.js, Nuxt, Astro, VitePress, and most things people describe as
"a React page."

Page script execution is partial. On a Wikipedia article two of five scripts
succeed. The failures are missing APIs, and they rarely affect the extracted
content.

External scripts do not load. A page that builds its content in `<script src>`
stays empty until the host supplies a `fetch` implementation.

Text encoding beyond UTF-8 is not handled. A `gbk` or `gb2312` page comes back
mangled until `golang.org/x/text` gets pulled in.

## Where to look when something breaks

Run the stages separately. When a page fails, the useful question is which stage
failed, and a probe program that prints each stage's error answers it in one run:

```
1. eval the host shims
2. eval the DOM bundle
3. print typeof for the bundle global and the suspect API
4. parse the HTML alone, with page scripts disabled
5. run the full pipeline
```

If a stage succeeds alone and the next one fails, the fault is localized. This is
how a DOM library defect got separated from a page script defect.

Read values out of the runtime before closing it. A closed runtime invalidates
them and reading afterwards crashes.

Before blaming the fetcher for a failed request, check that the target responds at
all. A host refusing this machine looks exactly like a bug in your code. One
Wikimedia host returns 400 from this VPS while the main site works fine.

## Roadmap

- Inject a Go implementation of `fetch`, so `<script src>` and page XHR work
- Cache compiled bytecode with `Compile()` to cut per-page setup cost
- Decode `gbk` and `gb2312`
- Output Markdown instead of plain text
- Prebuilt binaries and GitHub Releases, for people who do not have Go
