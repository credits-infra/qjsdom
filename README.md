# qjsdom

Read web pages from Go, including content that page JavaScript generates, without a browser and without Node or Bun. One static binary, about 80 MB of memory.

Two entry points:

- `qjsdom`, a command line tool
- `qjsdom-mcp`, an MCP server that any MCP client can call

## Install

```bash
go install github.com/endcredits/qjsdom/cmd/qjsdom@latest
go install github.com/endcredits/qjsdom/cmd/qjsdom-mcp@latest
```

Needs Go 1.25 or newer. No CGO, no system libraries.

## Command line

```bash
# Read a page
qjsdom https://example.com

# Read an article without the navigation
qjsdom -mode article https://en.wikipedia.org/wiki/Glycolysis

# Read a search results page, where the links are the content
qjsdom -mode links "https://html.duckduckgo.com/html/?q=glycolysis"

# Save the page's images so a model can look at them
qjsdom -download-images -images-dir /tmp/imgs https://example.com

# Get the article as Markdown, keeping headings, lists, and tables
qjsdom -markdown -mode article https://en.wikipedia.org/wiki/Glycolysis

# Render without letting the page's scripts touch the network
qjsdom -no-js-fetch -no-script-src https://example.com
```

`-mode` picks the extraction strategy. Use `article` for articles and documentation, `links` for search results and index pages. Leave it out when you are not sure. The default, `all`, returns the whole page and its link list, so it loses nothing.

Flags work in any position, so `qjsdom <url> -json` is fine.

Run `qjsdom -h` for the rest.

## MCP client

```json
{
  "mcpServers": {
    "qjsdom": {
      "command": "qjsdom-mcp",
      "env": { "QJSDOM_MAX_IMAGES": "5" }
    }
  }
}
```

The tool is `fetch_page`. It returns the page text, and when the page has images worth sending it returns them as image content blocks. A model that accepts images sees the figures directly, so charts and diagrams come through without the caller reading anything from disk.

Pass `format: "markdown"` to get the page as Markdown instead of plain text. Headings, lists, links, and tables survive the conversion, and relative links become absolute when the page has an http(s) address. The default is `text`.

Environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `QJSDOM_CACHE_DIR` | user cache dir `/qjsdom` | Where cached pages live |
| `QJSDOM_CACHE_TTL` | `10m` | Cache lifetime. `0` turns caching off |
| `QJSDOM_CACHE_MAX` | 64 MB | Cache size cap |
| `QJSDOM_TIMEOUT` | `20s` | Fetch timeout |
| `QJSDOM_MAX_TEXT` | `60000` | Truncate the text at this many characters. Counted in characters, not bytes |
| `QJSDOM_MAX_IMAGES` | `5` | Images per response. `0` returns none |
| `QJSDOM_MIN_IMAGE_SIDE` | `100` | Shorter side of an image that has known dimensions. An image at or below this on either side counts as an icon and is dropped |
| `QJSDOM_MIN_IMAGE_BYTES` | `5120` | Applied only when the dimensions cannot be read, such as for SVG |
| `QJSDOM_MAX_IMAGE_BYTES` | 2 MB | Per-image size cap |
| `QJSDOM_SAVE_IMAGES_DIR` | off | Write images here and report their paths. See below |
| `QJSDOM_DEBUG` | off | Set to `1` for diagnostics on stderr |
| `QJSDOM_ALLOW_SCHEMES` | empty | Extra URL schemes to allow. Empty means http and https only |
| `QJSDOM_MAX_CONCURRENCY` | `2` | Page requests handled at once. Further requests queue, up to twice this many, then get a busy error |
| `QJSDOM_JS_FETCH` | on | Let the page's own scripts make requests. `0` turns `fetch` and `XMLHttpRequest` off |
| `QJSDOM_SCRIPT_SRC` | on | Load `<script src>` files and run them where they sit. `0` leaves them alone |
| `QJSDOM_JS_FETCH_BUDGET` | `10s` | Total network time for the page. External scripts, page-script requests, and image probes share it, and the clock starts at the first request. `0` uses the default |

### URL schemes

Only `http` and `https` are fetchable, and that list is a boundary rather than a filter: it cannot develop holes the way an address list does. Set `QJSDOM_ALLOW_SCHEMES` to name extra schemes (`file` reads a local file), and the server prints a warning at startup when you do. Leaving it empty keeps everything else closed.

The `file` scheme is the one built-in addition. It is off by default because enabling it hands the caller a way to read files on the host. A deployment where anyone in a chat group can trigger a fetch should leave it off.

```bash
# Render a saved page, images and all, with no network access
QJSDOM_ALLOW_SCHEMES=file qjsdom-mcp
```

### Sending the images to a user

An image content block lets the model see an illustration. It does not let your program send that illustration to a user, because the bytes never reach the filesystem.

Set `QJSDOM_SAVE_IMAGES_DIR` and the server writes each returned image there and reports absolute paths in `image_paths`. What you do with those paths is up to your client: post them, upload them, or ignore them.

The server does not tell you how to send a file. That is a platform decision, and the right answer differs per client.

The response text ends with a list of the returned images, in the same order as the image content blocks:

```
## 本页图片

1. https://example.com/logo.png [PNG 100×100]
   文件:/tmp/images/077362022503ff00.png
2. https://example.com/figure.png [PNG 250×581]
   未落盘
```

An image block carries no name, so this list is the only place that says where an image came from. Without it a client can pair images to paths only by position, which stops working as soon as one image is dropped or skipped. The file line reads `未落盘` when the image was not written to disk, such as when `QJSDOM_SAVE_IMAGES_DIR` is unset.

Images come from the content container, not from the whole document. `article` mode and `selector` both narrow the collection, so a page header logo or a sidebar badge stays out of the list. `all` mode without a selector keeps the whole page and with it every image on it. Pass `selector` when a figure is missing from a page that has one.

## Scripts on the page

Pages that build their content in JavaScript are common enough that skipping their scripts means returning an empty shell. So the scripts run, and they can fetch.

`fetch` and `XMLHttpRequest` are real here, but they go through the same door as the page fetch itself: the scheme allowlist, the SSRF guard (re-checked on every redirect), the size cap, and the timeout. A page's scripts do not get a second path into the network, because whatever they can reach is what a page author's code can scan. Four limits sit on top: `GET` only (a `POST` from a page would make this machine send writes to third parties), 15 requests per page, 1 MB per response, and one 10-second budget for all of the page's network traffic. That budget covers external scripts, `fetch` and `XMLHttpRequest`, and `Image` probes, and its clock starts at the first request the page makes rather than at the start of the render, so building the DOM does not eat it. Once it runs out, further requests fail and the render records why in `scriptErrs`.

Relative URLs resolve the way a browser resolves them: against the document's base URL, which is the page address unless the page declares `<base href>`. The first `<base href>` in the document wins, and `location.href` still reports the page address. `Response` carries what scripts actually read: `status`, `ok`, `url`, `headers`, `redirected`, `text()`, `json()`, `arrayBuffer()`. Binary bodies are not supported — the host bridge returns text, so `arrayBuffer()` gives the UTF-8 bytes of that text rather than the original bytes.

`<script src>` files are fetched too, then put back inline where they were. Keeping the source in place is what preserves execution order; a browser runs external scripts in document order, and inlining them at their original position does the same thing without a scheduler. `async` and `defer` are not implemented — scripts run in document order, and that order is the deterministic one. `type="module"` is skipped, since it needs import resolution. A response that is not 2xx, or that declares `text/html`, is not run at all: a 404 error page inlined as a script only produces a stray syntax error that hides the real reason. At most ten files are fetched, at most 512 KB each, and a script past either limit stays where it is with a line in `scriptErrs` naming it. A truncated script is skipped for the same reason: half a script runs into a syntax error that says nothing about the real cause.

Scripts run in two passes for one reason. QuickJS drains its microtask queue after each evaluation, not during, so a callback queued by `fetch(...).then(...)` would run *after* the content had already been extracted — the callback's changes to the DOM simply would not be there. The runner therefore executes scripts in one evaluation, lets the host drain the queue, and extracts content in the next.

Each failure carries a reason. When a script cannot be fetched, is not 2xx, or is skipped for its type, `scriptErrs` says which script and why. When one throws, the entry names the script and the line: `not a function` on its own tells you nothing once a page has jQuery plus its own scripts.

`document.write` is available, and scripts it writes run. For which globals come from the engine, which from the DOM library, and which are shims in this repository, see [Architecture](docs/architecture.md#what-is-native-what-is-a-shim).

## Encodings

qjsdom decodes the page before the script runtime sees it, because linkedom and QuickJS both expect UTF-8 and a GBK article decoded as UTF-8 is a page of mojibake that nothing downstream can repair. The decoder reads the encoding in the order the HTML spec uses: a byte order mark first, then the `Content-Type` header, then a `<meta>` tag in the first kilobyte. Any label on the WHATWG list works, which covers `gbk`, `gb2312`, `gb18030`, `big5`, `shift_jis`, `euc-kr`, and the `windows-125x` family.

When a page declares nothing at all, qjsdom guesses in the order that hurts least. Bytes that are already valid UTF-8 are taken as UTF-8. Otherwise the decoder looks for the two-byte structure of GBK Chinese and treats the page as GB18030 if it finds it. Everything else becomes windows-1252. Chromium is the only other tool we measured that gets both of these right, and its advantage there comes from a full encoding detector rather than from the spec.

The result carries `charset` and `charsetSource`, so a caller holding unexpected text can see what the decoder decided. `charsetSource` is one of `header`, `meta`, `bom`, or `guessed`. A cache hit reports neither, since the cache stores decoded text.

Undeclared Big5 and Shift_JIS pages are not guessed at, because their byte structure does not overlap with GBK and a wrong guess turns a readable page into a different kind of unreadable one. Those pages need a declaration, the same as they do in every tool that lacks a language detector.

## What it will not do

This is not a browser. There is no layout engine, so a page that loads content when you scroll, or that measures element geometry, comes back thin. A pure client-side single page app renders as an empty shell. Server-rendered apps such as Next.js, Nuxt, Astro, and VitePress work, which covers most of what people call "a React page."

Scripts run, and `<script src>` files load, so a page that builds its content in JavaScript usually comes back with that content. What does not happen is interaction: no login, no clicking, no scrolling, and nothing that only appears after a person does something.

`document.write` works, including for the pages that assemble their content with it: [quotes.toscrape.com/js/](https://quotes.toscrape.com/js/) returns its quotes, its authors, and its tags. What is written lands where the script sits, and scripts written that way run, up to three rounds deep.

Two limits come from doing it this way. Each call is parsed on its own, so a tag split across two calls — `write("<div>")` then `write("</div>")` — does not survive; the parser closes it at the end of the first call. And a write that arrives after parsing finished appends to the body instead of clearing the document, with a line in `scriptErrs` saying so: the browser wipes the page in that case, which destroys the content rather than producing it.

`article` mode guesses. It walks a fixed list of likely containers and takes the first one with enough text. When it picks wrong, pass `-selector` with the container you want. For Wikipedia that is `#mw-content-text`.

## How it compares

The alternatives we measured, on the same pages on the same machine:

| Approach | Runs page scripts | Page scripts reach the network | Runtime to install | Install size | Peak memory on a small page |
| --- | --- | --- | --- | --- | --- |
| qjsdom | yes | yes | none, one binary | 15 MB | 89 MB |
| httpx + markdownify | no | no | Python | 15 MB plus Python | 50 MB |
| bun + happy-dom | yes | no | Bun | 85 MB plus 59 MB | 56 MB |
| Playwright + chromium | yes | yes | none | 389 MB | 595 MB |

qjsdom costs about 1.2 s and 89 MB of fixed startup and uses 4.7-6.7x less memory than chromium on the pages we measured, which is less than we hoped for and more than nothing on a 1.8 GB machine. bun with happy-dom is 2-5x faster as long as the page does not need to fetch anything from inside a script. See [docs/comparison.md](docs/comparison.md) for the full tables, the method, and the traps we hit on the way.

## Docs

- [Design decisions](docs/decisions.md): why qjs and linkedom, why one runtime
per page, and the measurements behind each choice
- [Architecture](docs/architecture.md): how the parts fit together

## Acknowledgments

[qjs](https://github.com/fastschema/qjs) runs QuickJS in pure Go through Wazero. It handles ES2023, which is what makes this approach work at all.

[linkedom](https://github.com/WebReflection/linkedom) supplies the DOM with no Node built-in dependencies, so it bundles small enough to embed.

The [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) handles the MCP side.

### How this was built

The code and documentation here were written by an AI agent. Saying so outright seems more useful than leaving it implied.

| | |
| --- | --- |
| Agent | [Hermes Agent](https://hermes-agent.nousresearch.com) v0.21.6 |
| Model | deepseek-v4.1-flash via [OpenCode Zen](https://opencode.ai/zen) |
| Human | chose the approach, set the constraints, reviewed every change |

The human half of that list is not a formality. The agent wrote that page images were not worth returning because a model cannot see them. It can, and the user said so. The agent also built an automatic content-type detector for extraction when the caller already knows what it wants; the user pointed out that the information was never in the page to begin with. Both corrections are the reason the current design looks the way it does, and both are recorded in [decisions](docs/decisions.md).

The documentation follows the [Google developer documentation style guide](https://developers.google.com/style), and the prose got a pass from [humanizer](https://github.com/blader/humanizer) to strip the patterns that make writing read as machine-made.

## License

MIT
