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
| `QJSDOM_MAX_TEXT` | `60000` | Truncate the text at this many characters |
| `QJSDOM_MAX_IMAGES` | `5` | Images per response. `0` returns none |
| `QJSDOM_MIN_IMAGE_SIDE` | `100` | Smaller images count as icons and get dropped |
| `QJSDOM_MIN_IMAGE_BYTES` | `5120` | Smaller images get dropped |
| `QJSDOM_MAX_IMAGE_BYTES` | 2 MB | Per-image size cap |
| `QJSDOM_DEBUG` | off | Set to `1` for diagnostics on stderr |

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

The documentation follows the
[Google developer documentation style guide](https://developers.google.com/style).

The prose got a pass from [humanizer](https://github.com/blader/humanizer) to
strip the patterns that make writing read as machine-made.

## License

MIT
