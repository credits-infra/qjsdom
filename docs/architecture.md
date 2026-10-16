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

Four shim files plus the runner carry the browser emulation, and all of them are
plain JavaScript.

`internal/renderer/js/host_shims.js` fills in the host APIs the DOM library and page
scripts expect: `Buffer`, `TextEncoder`, `TextDecoder`, `process`, `URL`,
`localStorage`, `history`, `requestAnimationFrame`, the observers, and others. Each
one is installed only when the name is missing, so a future engine or library that
provides it wins. Each stub does the minimum needed to keep the page running;
anything whose value we never read, such as audio, gets an empty shell.

`internal/renderer/js/image_fetch.js` replaces the simple `Image` stub with one that
calls back into Go for real image dimensions. It caches per URL and caps how many
images it probes per page, because a page with hundreds of images would otherwise
stall.

`internal/renderer/js/fetch_shim.js` builds `fetch`, `Response`, `Headers`, and
`XMLHttpRequest` on top of the Go host function `__qjsdom_httpFetch`, so a page
script's requests go through the same guard as the page fetch.

`internal/renderer/js/document_write.js` installs the four methods the DOM library
lacks (`write`, `writeln`, `open`, `close`). It supplies only that layer: parsing is
the library's own `innerHTML` path, and node insertion uses native DOM methods.

`internal/renderer/js/page_runner.js` does the actual work, in two evaluations per
page: parse the HTML and run each script in global scope, then extract content
according to `mode`, collecting links and image URLs. Splitting it in two is what
lets a callback queued by `fetch(...).then(...)` change the DOM before extraction —
QuickJS drains its microtask queue after an evaluation returns, never during. The
runner sits inside a try block, and if the DOM library fails it falls back to
stripping tags.

The runner is wrapped in an IIFE whose result is a JSON string. Go reads that string
before closing the runtime, since values do not survive the close.

## What is native, what is a shim

Three layers provide the globals a page sees. Knowing which one owns a missing API
saves patching the wrong layer.

| Capability | Provided by |
| --- | --- |
| `setTimeout`, `setInterval`, `queueMicrotask`, `Promise`, `Symbol`, `performance`, `navigator` | the qjs runtime: QuickJS compiled to wasm, plus qjs's own C shims |
| DOM: `parseHTML`, `querySelector`, `innerHTML` parsing, `insertAdjacentHTML`, `after`/`before`/`replaceChildren`, `createElement`, `cloneNode`, node traversal | linkedom, inside the bundle |
| `Buffer`, `TextEncoder`, `TextDecoder`, `URL`, `URLSearchParams`, `process` | `host_shims.js` |
| `fetch`, `Response`, `Headers`, `XMLHttpRequest` | `fetch_shim.js`, over the Go function `__qjsdom_httpFetch` |
| `Image` with real dimensions | `image_fetch.js`, over `__qjsdom_imageProbe` |
| `document.write`, `writeln`, `open`, `close` | `document_write.js`, over linkedom's parsing and node insertion |
| `localStorage`, `sessionStorage`, `history`, `location`, `screen`, `getComputedStyle`, `requestAnimationFrame`, `MutationObserver`, `IntersectionObserver`, `ResizeObserver`, `CustomEvent`, `Audio`, `DOMParser` | `host_shims.js` |
| Not provided at all: `atob`, `btoa`, `matchMedia`, `customElements`, `XMLSerializer`, `Node`, `HTMLElement`, `Event` | — |

Measured by evaluating a `typeof globalThis[name]` probe after each injection stage
and recording the earliest stage that provides each name. Two things that only show
up when it is measured:

- linkedom does not load without the shims. Injecting the library by itself dies with
  `ReferenceError: Buffer is not defined` in its base64 decoder, and `Buffer` is one
  of ours — which is why the bundle prepends the shims instead of shipping the
  library alone.
- `navigator` and `performance` come from qjs itself, not from linkedom and not from
  the shim files. A missing API belongs to one specific layer, and patching another
  one leaves the real cause where it was.

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

The prepending is not cosmetic: linkedom's base64 decoder reads `Buffer` while the
library loads, and `Buffer` is one of our shims. Evaluating the library on its own
ends in `ReferenceError: Buffer is not defined` before any page runs.

## Boundaries

A fake DOM has no layout. Pages that load content on scroll, or that measure
element geometry to decide what to show, come back thin.

There is no layout, no timer that fires after the page has been handed back, and no
interaction. A pure client-side single page app that needs a click or a scroll
before it shows anything still renders as an empty shell. Server-rendered apps work,
which covers Next.js, Nuxt, Astro, VitePress, and most things people describe as
"a React page."

Page script execution is partial, and the failures are missing APIs: three of the
five scripts on a Wikipedia article run, and the rest report why. Those scripts
rarely affect the extracted content.

`document.write` works, but each call is parsed on its own, so a page cannot split a
tag across two calls the way a browser lets it. A write that arrives after the page
finished parsing appends to the body instead of clearing the document, and says so in
`scriptErrs` — the browser behaviour there destroys content rather than producing it.

Text encoding beyond UTF-8 is not handled. A `gbk` or `gb2312` page comes back
mangled until `golang.org/x/text` gets pulled in.

## Where to look when something breaks

Run the stages separately. When a page fails, the useful question is which stage
failed, and a probe program that prints each stage's error answers it in one run:

```
1. eval the host shims, then the DOM bundle, then the document.write shim
2. print typeof for the bundle global and the suspect API
3. parse the HTML alone, with page scripts disabled
4. run the script phase, then the extraction phase
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

- Cache compiled bytecode with `Compile()` to cut per-page setup cost
- Decode `gbk` and `gb2312`
- Provide `atob` and `btoa`, which the probe above shows are missing; a few pages
  feature-detect them before doing anything
- Prebuilt binaries and GitHub Releases, for people who do not have Go
