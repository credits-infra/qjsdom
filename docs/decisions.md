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

## Run the page's scripts, but through the same door

A page whose content is assembled by JavaScript is common enough that skipping its
scripts returns an empty shell. So the scripts run, and `fetch` exists. What the
scripts must not have is a second route into the network: whatever the page author's
code can reach is what this machine can be made to scan. `fetch` therefore calls the
same `fetcher` used for the page itself — scheme allowlist, SSRF guard with a
re-check on every redirect, size cap, timeout. Two limits come from that reasoning
rather than from politeness: `GET` only, because a `POST` issued from page script
would have this machine writing to third parties, and a request count per page,
because a loop that never stops fetching should not hold the render open.

`fetch` is called synchronously into Go. Browsers make it asynchronous, but this is
a scraper with a hard page timeout, and a synchronous call makes the order
"script → response → callback" deterministic instead of dependent on event-loop
scheduling. Callbacks still run as microtasks, which is where the next problem was.

QuickJS drains its microtask queue in `js_std_loop`, after each evaluation returns.
The runner used to queue scripts and extract content inside a single evaluation,
which meant every `Promise.then` or `fetch().then` callback ran *after* extraction —
its DOM changes were never in the result. Measured with a four-line fixture: the
page returned the paragraph's original text. Splitting the runner into two
evaluations (scripts, then extraction, with the host draining the queue between
them) fixed it, and it is a necessary precondition for `fetch` to be worth having at
all. The extract pass reads the document from a global rather than re-parsing, and
never re-runs scripts: side effects must not happen twice.

`<script src>` is fetched in Go and put back inline at its original position,
instead of being loaded by page script. Order is the reason: browsers execute
external scripts in document order, and inlining at the original spot preserves that
without writing a serial scheduler. `async` and `defer` are not implemented, so the
order that remains is the deterministic one.

One failure mode shaped the code. A 404 response body is an HTML error page; inlined
as a script it produces a stray `expecting ';'` in `scriptErrs` and hides the actual
reason, which is that the script could not be fetched. Non-2xx responses and
responses declaring `text/html` are now recorded with their status and skipped
instead of being run.

## Implement the missing layer, not a second DOM

`document.write` is how a whole generation of pages assembled their content:
[quotes.toscrape.com/js/](https://quotes.toscrape.com/js/) loads jQuery and then writes
each quote into the document, and without the method it returns a shell while every
script reports success. linkedom has no `write`, `writeln`, `open`, or `close`, so the
four have to come from somewhere.

They come from as little new code as possible. Parsing the written HTML is linkedom's
own `innerHTML` path — writing a parser again is the single worst thing to do here,
because entities, comments, table fixups, and error recovery are all traps, and the
library already has one. Node insertion uses `parentNode`, `firstChild`,
`nextSibling`, and `insertBefore`. What we add is the method layer and the position
decision, nothing else. The same rule fixed the taxonomy in `architecture.md`: a
missing API belongs to whichever layer owns it, and patching a different layer leaves
the real cause in place.

Position is the decision with content consequences: written nodes go after the script
that wrote them, which is where a browser puts them and where these pages expect their
content to appear. Writes are inserted before a stable reference node rather than
always immediately after the script — the obvious `insertAdjacentHTML("afterend")`
reverses the order of the second write, because the second insertion lands before the
first.

Two behaviours deliberately differ from a browser. Multiple calls cannot build one tag,
since each call is parsed independently; a browser streams them through one parser. And
a write after parsing finished appends to the body instead of wiping the document —
that is what a browser does, and it destroys the content a scraper came for. Both
deviations are documented, and the second one leaves a line in `scriptErrs` instead of
changing the contract silently.

Scripts written by `document.write` run, in up to three rounds: a browser runs them,
and a page that writes a script to write its content would otherwise lose that content.
The round limit exists because a script can write a script that writes a script, and
nothing bounds that but us. An external script discovered this way is fetched through
the same guard as everything else, since the Go-side inlining happened before it
existed.

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

## Follow `<base href>`, like a browser

The README promised that relative URLs resolve the way a browser resolves them, and
for a while that was not true: every relative address was completed against the
document address. A page carrying `<base href="/zh/wiki/">` then resolved its
external scripts and its own `fetch` calls against the site root, so the render
produced a handful of 404s and an empty article. The symptom points at the site
("they must be blocking us") rather than at the resolution, which is why it took a
review to surface.

One function now decides the base: the first `<base>` with an `href`, in tree order,
resolved against the document address when it is relative. The spec allows a `base`
element in the body, so the search covers the whole document rather than the head.
Everything that completes a relative URL uses it — the external-script pass, the
image probe, the page scripts' fetch bridge, image downloads, and Markdown link
completion.

`location.href` deliberately still reports the document address. A browser keeps
"which document am I" and "relative URLs relative to what" apart, and page scripts
depend on the difference: one is the page the user is on, the other is where assets
come from.

## Bound the time a page's network may take

A request count is not a time bound. Fifteen page-script requests each waiting out
its own ten-second timeout is two and a half minutes of wall clock for one page, and
the count cap cannot tell fifteen fast requests from fifteen that hang. Measured
worst case before this change: about 150 seconds, serialized, with nothing the
caller could do about it.

So the page's own scripts get a total wall-clock budget for the network, measured
from the start of the render. Ten seconds by default; `QJSDOM_JS_FETCH_BUDGET` (or
`-js-fetch-budget`) changes it. A request that arrives after the budget is spent
fails immediately with a message naming the budget, and that message lands in
`scriptErrs` — the page still renders, and the caller can see the network was cut
short instead of wondering why content is missing.

Time-based rather than another count, deliberately: slow pages are the failure
mode, while a page making fifteen fast requests is fine and should get all fifteen.

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
