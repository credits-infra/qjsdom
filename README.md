# qjsdom

Read web pages from Go, including content that page JavaScript generates, without
a browser and without Node or Bun. One static binary, about 80 MB of memory.

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
```

`-mode` picks the extraction strategy. Use `article` for articles and
documentation, `links` for search results and index pages. Leave it out when you
are not sure. The default, `all`, returns the whole page and its link list, so it
loses nothing.

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

The tool is `fetch_page`. It returns the page text, and when the page has images
worth sending it returns them as image content blocks. A model that accepts
images sees the figures directly, so charts and diagrams come through without the
caller reading anything from disk.

Environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `QJSDOM_CACHE_DIR` | user cache dir `/qjsdom` | Where cached pages live |
| `QJSDOM_CACHE_TTL` | `10m` | Cache lifetime. `0` turns caching off |
| `QJSDOM_CACHE_MAX` | 64 MB | Cache size cap |
| `QJSDOM_TIMEOUT` | `20s` | Fetch timeout |
| `QJSDOM_MAX_TEXT` | `60000` | Truncate the text at this many characters. Counted in characters, not bytes |
| `QJSDOM_MAX_IMAGES` | `5` | Images per response. `0` returns none |
| `QJSDOM_MIN_IMAGE_SIDE` | `100` | Shorter side of an image that has known dimensions; smaller images count as icons and get dropped |
| `QJSDOM_MIN_IMAGE_BYTES` | `5120` | Applied only when the dimensions cannot be read, such as for SVG |
| `QJSDOM_MAX_IMAGE_BYTES` | 2 MB | Per-image size cap |
| `QJSDOM_SAVE_IMAGES_DIR` | off | Write images here and report their paths. See below |
| `QJSDOM_DEBUG` | off | Set to `1` for diagnostics on stderr |
| `QJSDOM_ALLOW_SCHEMES` | empty | Extra URL schemes to allow. Empty means http and https only |

### URL schemes

Only `http` and `https` are fetchable, and that list is a boundary rather than a
filter: it cannot develop holes the way an address list does. Set
`QJSDOM_ALLOW_SCHEMES` to name extra schemes (`file` reads a local file), and the
server prints a warning at startup when you do. Leaving it empty keeps everything
else closed.

The `file` scheme is the one built-in addition. It is off by default because
enabling it hands the caller a way to read files on the host. A deployment where
anyone in a chat group can trigger a fetch should leave it off.

```bash
# Render a saved page, images and all, with no network access
QJSDOM_ALLOW_SCHEMES=file qjsdom-mcp
```

### Sending the images to a user

An image content block lets the model see an illustration. It does not let your
program send that illustration to a user, because the bytes never reach the
filesystem.

Set `QJSDOM_SAVE_IMAGES_DIR` and the server writes each returned image there and
reports absolute paths in `image_paths`. What you do with those paths is up to
your client: post them, upload them, or ignore them.

The server does not tell you how to send a file. That is a platform decision, and
the right answer differs per client.

The response text ends with a list of the returned images, in the same order as the
image content blocks:

```
## 本页图片

1. https://example.com/logo.png [PNG 100×100]
   文件:/tmp/images/077362022503ff00.png
2. https://example.com/figure.png [PNG 250×581]
   未落盘
```

An image block carries no name, so this list is the only place that says where an
image came from. Without it a client can pair images to paths only by position, which
stops working as soon as one image is dropped or skipped. The file line reads
`未落盘` when the image was not written to disk, such as when
`QJSDOM_SAVE_IMAGES_DIR` is unset.

## What it will not do

This is not a browser. There is no layout engine, so a page that loads content
when you scroll, or that measures element geometry, comes back thin. A pure
client-side single page app renders as an empty shell. Server-rendered apps such
as Next.js, Nuxt, Astro, and VitePress work, which covers most of what people
call "a React page."

External scripts do not load, so a page that needs `<script src>` to build its
content stays empty. There is no login, no clicking, and no interaction.

`article` mode guesses. It walks a fixed list of likely containers and takes the
first one with enough text. When it picks wrong, pass `-selector` with the
container you want. For Wikipedia that is `#mw-content-text`.

## Docs

- [Design decisions](docs/decisions.md): why qjs and linkedom, why one runtime
  per page, and the measurements behind each choice
- [Architecture](docs/architecture.md): how the parts fit together

## Acknowledgments

[qjs](https://github.com/fastschema/qjs) runs QuickJS in pure Go through Wazero.
It handles ES2023, which is what makes this approach work at all.

[linkedom](https://github.com/WebReflection/linkedom) supplies the DOM with no
Node built-in dependencies, so it bundles small enough to embed.

The [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) handles the MCP
side.

### How this was built

The code and documentation here were written by an AI agent. Saying so outright
seems more useful than leaving it implied.

| | |
| --- | --- |
| Agent | [Hermes Agent](https://hermes-agent.nousresearch.com) v0.21.6 |
| Model | deepseek-v4.1-flash via [OpenCode Zen](https://opencode.ai/zen) |
| Human | chose the approach, set the constraints, reviewed every change |

The human half of that list is not a formality. The agent wrote that page images
were not worth returning because a model cannot see them. It can, and the user
said so. The agent also built an automatic content-type detector for extraction
when the caller already knows what it wants; the user pointed out that the
information was never in the page to begin with. Both corrections are the reason
the current design looks the way it does, and both are recorded in
[decisions](docs/decisions.md).

The documentation follows the
[Google developer documentation style guide](https://developers.google.com/style),
and the prose got a pass from [humanizer](https://github.com/blader/humanizer) to
strip the patterns that make writing read as machine-made.

## License

MIT
