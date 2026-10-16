# Design decisions

Every choice here came out of a measurement, and several of them went against
what I expected. Where a decision replaced an earlier attempt, the failed attempt
is still in here, because the reason it failed is the reason the current design
looks the way it does.

Some of these took an afternoon. A couple took a week of trying the wrong thing
first. Every measurement below comes from a 2-core VPS with 1.8 GB of memory,
which is why the numbers look small.

## Skip the browser

A browser renders anything. It also costs about 1.5 GB of memory, and the machine
this was built for has 1.8 GB total. Chromium was never an option.

| Approach | Memory | Runtime needed |
| --- | --- | --- |
| Playwright / chromium | 1483 MB | none |
| Node + jsdom | 43 MB | Node |
| Bun + happy-dom | 53 MB | Bun |
| qjs + linkedom | ~80 MB | none |

Node and Bun are cheaper, but both mean shipping and maintaining a second runtime
next to a Go binary. That left an in-process JavaScript engine with a DOM
library.

## qjs over goja

goja is the obvious Go choice and it does not work here. It implements ES5.1.

The DOM bundle cannot be downgraded to ES5 to accommodate it. esbuild refuses
outright:

```
✘ Transforming const to the configured target environment ("es5") is not supported yet
```

The bundle contains 16161 `const` declarations, 6317 `??` operators, and 656
`?.` operators. Rewriting it is not on the table.

qjs implements ES2023 including async/await, so the bundle loads as-is. This is
the decision that made the whole approach viable, and it came after two dead ends
described at the end of this file.

## linkedom over jsdom

jsdom runs page scripts through `node:vm`. That is a Node host capability, and
reimplementing it in Go means reimplementing a large part of Node.

| | jsdom | linkedom |
| --- | --- | --- |
| Bundle size | 10.8 MB | 0.5 MB |
| Node built-ins used | `fs`, `path`, `url`, `vm` | none |

linkedom uses no Node built-ins, so it embeds cleanly. It also does not execute
page scripts, which sounds like a loss and is actually convenient: we run them
ourselves with `(0, eval)(code)`. The indirect call gives global scope semantics,
which is what makes `var` land on `window` the way a real `<script>` does.

## One JavaScript runtime per page

Reusing a runtime leaks state between pages. I tested this rather than assuming
it:

```
Page A:  var siteName = "page A";
         Array.prototype.polluted = function(){...};

Page B sees: {"seenSiteName":"string", "arrayPolluted":"function"}
```

The variable leak is annoying. The prototype pollution is worse, because you
cannot clean it up afterwards, and you have no way to know which page did it.

A fresh runtime costs about 211 ms. The first one costs about 900 ms because it
loads the WASM module. That price buys complete isolation, and no isolation
scheme I could design would be cheaper.

The engine exposes one context per runtime, so "use a new context" is not an
available middle ground.

## A page error never aborts the render

Page scripts fail constantly. Most sites ship something that expects an API we do
not have, and a page whose script throws is still a page with readable text.

Each `<script>` gets its own try block. If the DOM library itself fails, the
runner falls back to stripping tags and returns what it can, flagged as
`degraded`, rather than returning an error.

This one took a while, because the obvious explanation was wrong. Google's search
page failed with `Image is not defined`, and my first guess was that a page
script needed the API. Disabling page scripts produced the same error, which
ruled that out.

The actual cause: linkedom defines `navigator`, `location`, and `window` as
accessor properties. Assigning to them invokes its setters, and a setter can
reference an API we have not stubbed. That throw lands outside a try block that
only wrapped the later code. The fix was wrapping every global assignment
individually.

A missing host API can break the DOM library itself. If disabling script
execution produces the same error, the fault is not in the page.

## Stub the host APIs in JavaScript

All host APIs live in `internal/renderer/js/host_shims.js`, written in plain
JavaScript. The Go side contains no DOM code at all. Go owns fetching, timeouts,
and file access; JavaScript owns everything the page can observe.

`Image` is the clearest example of why the stubs matter. Stubbing it took script
execution on Google's search page from 0 of 5 to 5 of 5.

## Extraction strategy belongs to the caller

There is a `mode` flag and there is no auto-detection. This was a deliberate
reversal.

My first design scored candidate containers by link density and picked the best
one. It measured 665 characters different from just returning the whole page, and
the container it chose still included the navigation. All that work bought
almost nothing.

The deeper problem is that the information needed to choose is not in the page.
The caller already knows whether it is looking something up or reading an
article. A model calling this tool knows exactly which one it wants. Deriving
intent from page features fails precisely on mixed pages, and link density
between 15% and 70% describes a lot of real pages.

A wrong choice costs one re-render, and the cache makes that cheap.

### `links` exists because readability is backwards there

On a search results page the links are the content. A readability heuristic
faithfully deletes the actual results. Measured against DuckDuckGo, the
link-density approach returned the region selector from the sidebar and none of
the search results.

### `article` is a guess, and says so

`article` walks a fixed list of likely containers and takes the first one with
enough text. It is not intelligent and does not pretend to be.

```
article → main → [role=main] → #mw-content-text →
.post-content → .entry-content → .article-body →
#content → .content → .article → .post → #main → .main
```

On Wikipedia it picks a container that still includes the sidebar:

```
article alone:                            17111 characters
article with -selector #mw-content-text:  16119 characters
```

The 1000-character difference is the navigation. Documenting this boundary and
offering `-selector` is more honest than adding more heuristics that fail in new
ways.

Only verified selectors appear in the documentation. Writing down a selector I
have not run would be inventing it.

## Keep the images

I initially wrote that images were not worth returning because a model cannot see
them. That was wrong, and the user corrected me. Models accept image input, and
for textbook material the figures carry the content: the values, the arrows, the
direction of flow, the regulatory relationships.

So images get downloaded and handed to the model. Video and audio still do not,
because they are large, slow to process, and not the payload anyone asked for.

Lazy loading is the normal case now. A modern `<img>` often carries a 1x1 base64
placeholder in `src` while the real URL sits in `data-src` or `srcset`. Reading
`src` alone found zero images on a Wikipedia article. Handling the lazy
attributes found 87.

MCP returns images as content blocks rather than file paths. The protocol has a
native image content type, so a vision-capable model receives the illustration
directly and the caller never touches the filesystem.

## Filter the images before sending them

Most `<img>` elements on a real page are icons, logos, and placeholders. A
Wikipedia article yielded 87 image URLs, of which 60 were too small to be worth
sending. Passing all of them wastes context and buries the figures that matter.

The MCP server drops images below 100 pixels on either side, or below 5 KB, and
reports how many it skipped. The model then knows the result is curated rather
than complete.

## Cache the HTML, not the extraction

The `mode` flag only works as a design if retrying is cheap. Fetching and
rendering are separate steps, so the cache stores the page source. Switching mode
re-renders without re-fetching.

Two defenses, because either one alone lets the cache grow without limit:

| Defense | Default | Purpose |
| --- | --- | --- |
| TTL | 10 minutes | Pages change, so nothing stays trustworthy forever |
| Size cap | 64 MB | Evicts least recently used entries |

The first version purged only before writing, which looked correct and was not.
The new entry does not exist yet at that point, so it is absent from the
accounting and the write lands after the check. With a 600 KB cap, two 440 KB
pages stayed on disk. Purging on both sides of the write fixed it.

An MCP server is long-lived, so it enables caching by default. The command line
tool does not, because a one-shot command gains nothing from it.

## Retry rate limits, and only rate limits

Downloading a Wikipedia article's images in bulk returned HTTP 429 on two of
them. Rate limiting is temporary; giving up discards content that was a second
away.

The retry logic covers 429, 500, 502, 503, and 504. It does not retry 404 or 403,
because those will answer the same way however many times you ask, and retrying
them only makes failures slower. It honors `Retry-After` when the value is a sane
number of seconds. It rebuilds the request on each attempt, since a sent
request's body is already consumed.

Deduplicating URLs belongs in the same change. The 87 image URLs on that article
reduce to 65 unique addresses, and duplicate requests multiply the very request
count that triggers rate limiting.

## Report failures with their reason

"5 images failed" tells a caller nothing. The first version printed only the
first five entries, and since the failures were at the end, the output showed a
success count and no explanation at all.

The user asked what had failed. I could not answer, which is how the truncation
got found. Now the success list stays short and failures are never truncated.
Current output names each failing URL and why, which is how I learned that one of
them was not a failure at all: MediaWiki uses an `<img>` tag for an auto-login
probe, and the code was correctly rejecting it by content type.

## Let a library convert HTML to Markdown

The Markdown output is `github.com/JohannesKaufmann/html-to-markdown/v2`, wrapped
by `internal/markdown`. Writing that converter here was never on the table: the
work is entirely edge cases — nested lists, tables, backticks inside code blocks,
inline elements inside a word, which characters need escaping — and each one is a
bug report waiting to happen. The library is pure Go with no CGO, adds two
dependencies, and costs about 670 KB of binary.

The conversion runs on the Go side, not in the page runner. The runner already
builds the rendered DOM, so it hands back the HTML of the content container and
Go converts it. Both outputs therefore come from the same container, and neither
language carries a second set of conversion rules.

Two measurements shaped the wiring. Converting a 441 KB Wikipedia page takes 45 ms,
which is noise next to a render of several seconds; and serialising the container
costs real bytes, so it happens only when the caller asks for Markdown
(`__NEED_HTML__`), which also keeps `-json` from growing by the size of the page.

The one trap found by testing: relative links resolve against the page address, and
a `file://` base produces `http://file:/wiki/x` — an address nobody can open. Only
http(s) page addresses take part in link resolution now; anything else keeps the
relative link as it stands, matching what the plain-text link list already shows.

## Guard against SSRF

This component ends up exposed to a chat group where anyone can trigger a fetch.
Without a guard it becomes a scanner for the internal network.

Two rules do the work, and they are not equally strong. The first is a protocol
allowlist: only `http` and `https` are fetchable, and everything else stays shut
until an operator names it in `QJSDOM_ALLOW_SCHEMES`. That list can be read at a
glance, which is the point of it. An address list cannot be: Go's `IsPrivate()`
covers RFC 1918 and RFC 4193 but not `100.64.0.0/10` (RFC 6598), so
`100.100.100.200`, the Alibaba Cloud metadata address, passes the address check on
a host that uses that range.

The second rule is the address check. It rejects loopback, private, and link-local
addresses, plus the AWS and GCP metadata address `169.254.169.254`, on the entry
URL and again on every redirect. Treat it as a backstop rather than the boundary:
it is a list of known-bad ranges, and lists of known-bad things belong behind a
rule that is closed by default.

## Accept flags after the URL

Go's `flag` package stops parsing at the first non-flag argument and silently
drops the rest. `qjsdom <url> -json` loses `-json` with no error message.

This cost me real time. A Wikipedia image download appeared to be broken when the
actual problem was that `-download-images` had never taken effect. The tool now
reorders its arguments so flags can appear anywhere.

## Keep the transport out of the tool

The MCP server returns data. It does not tell the caller how to deliver that data.

An early draft appended `MEDIA:<path>` lines to the response text, because that
is how Hermes sends a file to a chat platform. The user caught it: `MEDIA:` is a
Hermes convention, not an MCP one. A client that is not Hermes receives an
instruction it cannot execute, and the same path would not resolve on a remote
machine in any case.

The split now:

| Layer | Provides |
| --- | --- |
| MCP server | text, image content blocks, absolute paths |
| The client | whatever it takes to show those images to a person |

The general rule, which is why this is worth writing down: a tool that reaches
across a protocol boundary should speak that protocol and leave platform
conventions to the platform.

## Label the images, do not number them

An image content block carries bytes and a MIME type. It carries no name. The path
next to it is the SHA-1 of its URL, which identifies nothing a reader can use. So a
caller holding three images and three paths can only pair them by position — and
every step between the server and the model is free to change the positions.

That is how a site logo went out as a figure. The model had three paths, the client
had rejected one image (an SVG, which a raster-only media cache will not accept),
and it sent the first path it could see.

The first cause found was a different bug: the server pre-encoded the image bytes to
base64 and the SDK encoded them again, so no client could decode them and the model
had seen no images at all. Fixing it is the reason to distrust the obvious
conclusion, because once it landed the model could see the images and recovered the
right one by itself.

Measured on `gemini-3.1-flash-lite`, twelve runs per arm, one Wikipedia article, both
arms fed byte-identical text and images:

| Scenario | Paths returned | Images attached | Without the list | With the list |
| --- | --- | --- | --- | --- |
| Loose question | 3 | 2 | 12/12 | 12/12 |
| Name the figure | 8 | 6 | 6/12 | 12/12 |

The small case is not evidence for the list. With one image missing out of three, the
model counts correctly and finds the figure anyway, so the list is redundant there.
The large case is the evidence: six of twelve runs sent the site logo, and the list
removed every failure. The URL says `icons/wikipedia.png`; nothing has to be counted.

The harness lied first, and in the flattering direction. Its opening run gave the arm
under test a perfect score, because the table used to interpret the answers came from
the same list that the code had stripped out of that arm's text. The metric was
measuring its own blind spot. Derive the expectation from the unmodified result, never
from what you handed the model.

The rule: state the mapping the server holds, rather than expecting every client to
preserve an order the protocol never promised. It is not a workaround for one client,
because no client can recover a URL from a SHA-1 filename.

## Paths not taken

**markitdown** converts many formats to Markdown and looks like a natural fit. It
pulls in magika for file-type detection, which brings onnxruntime and numpy:
thirteen packages, roughly 300 MB, to handle HTML that a 3 MB parser handles
already. Its real value is PDF and Office documents, which is a separate problem.

**QuickJS alone** is a JavaScript engine. No HTML parser, no CSS, no Web APIs.
Using it directly means writing a browser, which is not what an embedded engine
is for.

**WebKitGTK** would render properly at 200 to 400 MB, well under chromium. It
needs `webkit2gtk4.1`, `python3-gobject`, and a virtual display. That last part is
the problem: the machine has no headless graphics environment at all. Installing
one to avoid a browser is a poor trade.
