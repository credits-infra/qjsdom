# How qjsdom compares

This page reports what qjsdom costs next to the approaches we considered, measured on the same pages on the same machine. Page shape changes these numbers more than the choice of tool does, so treat the tables as a starting point and run your own pages through it before you decide.

## Method

The machine is a 2-core AMD EPYC VPS with 1.8 GB of memory, running Go 1.25.1. Memory means the peak resident set of the whole process tree, sampled every 20 ms for the life of the run, so a tool that spawns children pays for all of them. Playwright, for example, starts a Python driver, a Node process, and chromium, and all three land in its number. Time is wall clock, median of three runs. The pages come from a local server so the network does not add noise.

The fixture pages, in order of how hostile they are to a scraper:

| Page | What it tests |
| --- | --- |
| `a.html` | Static article, one inline script, two images |
| `b.html` | Article built entirely by a script |
| `c.html` | Article that fills a paragraph from `fetch` |
| `d.html` | Escaped addresses and one lazy `data-src` image |
| `big.html` | 248 KB, 3000 paragraphs, for parse scaling |
| `tiny.html` | One paragraph, for fixed startup cost |

These are our own fixtures, which is worth remembering when qjsdom wins a column. The last section repeats the comparison on a real page.

## What each approach can do

| Approach | Runs page scripts | Page scripts reach the network | Collects lazy images | Runtime to install | Install size |
| --- | --- | --- | --- | --- | --- |
| qjsdom | yes | yes | yes | none, one binary | 15 MB |
| httpx + markdownify | no | no | no | Python | 15 MB plus Python itself |
| bun + happy-dom | yes | no | no | Bun | 85 MB plus 59 MB |
| Playwright + chromium | yes | yes | no | none, it ships its own | 389 MB, plus 262 MB for the headless shell |

r.jina.ai belongs in this table as a hosted approach we measured and could not use: it answers 403 from this host without an API key, so it appears nowhere else on this page.

Two rows explain most of the differences later on. happy-dom evaluates scripts but gives them no network, so `c.html` stays empty for it. Chromium and qjsdom both reach the network, which is why they are the slow, heavy rows.

## Timing

Median of three runs, in seconds. `tiny.html` is the fixed cost of starting the tool at all.

| Page | qjsdom | httpx + markdownify | bun + happy-dom | chromium |
| --- | --- | --- | --- | --- |
| `a.html` | 1.23 | 0.42 | 0.22 | 2.01 |
| `b.html` | 1.19 | 0.42 | 0.19 | 1.65 |
| `c.html` | 1.22 | 0.47 | 0.21 | 1.62 |
| `d.html` | 1.22 | 0.43 | 0.20 | 1.62 |
| `big.html` | 2.08 | 0.81 | 0.37 | 2.69 |
| `tiny.html` | 1.18 | 0.41 | 0.44 | 2.93 |

## Peak memory

Same runs, in megabytes.

| Page | qjsdom | httpx + markdownify | bun + happy-dom | chromium |
| --- | --- | --- | --- | --- |
| `a.html` | 88.9 | 49.9 | 56.4 | 594.8 |
| `b.html` | 89.1 | 49.9 | 55.2 | 595.3 |
| `c.html` | 89.1 | 49.9 | 55.7 | 593.3 |
| `d.html` | 88.8 | 49.9 | 56.0 | 596.5 |
| `big.html` | 90.2 | 54.5 | 72.3 | 578.8 |
| `tiny.html` | 88.1 | 49.8 | 55.4 | 558.3 |

## Text extracted

Characters of text, and whether the result contains the part of the page that matters. `b.html` has no content without a script runtime, and `c.html` has none without network from inside a script.

| Page | qjsdom | httpx + markdownify | bun + happy-dom | chromium |
| --- | --- | --- | --- | --- |
| `a.html` | 216, complete | 298, complete | 302, complete | 290, complete |
| `b.html` | 112, complete | 24, missing the article | 273, complete | 132, complete |
| `c.html` | 80, complete | 88, missing the fetched text | 264, missing the fetched text | 95, complete |
| `d.html` | 85, complete | 168, complete | 89, complete | 100, complete |

The character counts do not rank extraction quality. On the Wikipedia article below, httpx and markdownify return 121,292 characters of Markdown against qjsdom's 17,776, because markdownify keeps every navigation box and template on the page while qjsdom returns the article. More text is not the same as the right text.

## Images

| Page | qjsdom | httpx + markdownify | bun + happy-dom | chromium |
| --- | --- | --- | --- | --- |
| `a.html` (two plain images) | 2 | 2 | 2 | 2 |
| `d.html` (one `data-src` image) | 1 | 1, still double-escaped | 0 | 0 |

Chromium and happy-dom see `<img>` elements, so they report the same plain images as qjsdom. Neither of them loads an image whose address sits in `data-src`, which is correct browser behavior and a problem for a scraper: Wikipedia puts most of its figures there. qjsdom reads the attribute on purpose.

httpx and markdownify find the `data-src` address with a regular expression, and hand it back with `&amp;amp;` still in it because nothing decodes entities on that path.

## A real page

The Chinese Wikipedia article on glycolysis, one run per tool, same machine.

| Tool | Time | Peak memory | Text |
| --- | --- | --- | --- |
| qjsdom | 4.78 s | 150.2 MB | 17,776 characters |
| httpx + markdownify, default user agent | 0.72 s | 50.5 MB | 126 characters, which is a bot wall |
| httpx + markdownify, browser user agent | 0.72 s | 50.5 MB | 121,292 characters of Markdown |
| bun + happy-dom | 0.74 s | 101.4 MB | 35,211 characters |
| chromium | 4.65 s | 706.9 MB | 9,797 characters |

Wikimedia rejects the user agent that httpx sends by default and answers 403 with a 126-character notice, so the first httpx row measures a refusal rather than the tool. qjsdom sends a user agent and gets the article. If your pipeline uses httpx, set one.

The page is 441 KB and the CDN answer moves between runs, so read this table as one sample rather than a stable measurement. Two separate qjsdom runs on it returned the same 17,776 characters.

## What the numbers say

The fixed cost dominates every small page. qjsdom spends about 1.2 s and 88 MB before it looks at your content, and chromium spends 2.9 s and 558 MB, so on a one-paragraph page the runtime is the measurement. On the 248 KB page qjsdom parses in 2.08 s against chromium's 2.69 s, and the gap widens with page size.

Memory against chromium lands between 4.7x and 6.7x on these pages: 88.9 against 594.8 on the small fixture, 90.2 against 578.8 on the big one, 150.2 against 706.9 on the real article. That is a real advantage on a 1.8 GB machine, and it is not the 10x we hoped to quote. If you need the exact ratio for a page class, measure that class. Chromium memory grows with the page and with the number of frames, and neither of these fixtures has frames or video.

Only chromium does the same work as qjsdom, since it also runs page scripts and lets them reach the network. Against that one comparable tool, qjsdom uses 4.7 to 6.7 times less memory, returns 17,776 characters of article text against 9,797 on the Wikipedia page, and installs as one 15 MB binary instead of a 389 MB browser plus a 262 MB headless shell. Speed is where qjsdom loses. bun with happy-dom beats it by 2-5x on every page here, because bun starts in 0.2 s and because happy-dom does less work: no page-script networking, no image probes. Choose bun and happy-dom when you already have the HTML and only need script evaluation on top of it. Choose qjsdom when scripts need to fetch, when you want the lazy images, or when installing a JavaScript runtime is not an option.

## Things that surprised us

A page that declares no character set sends chromium to the HTML spec's windows-1252 default, and Chinese text comes back as mojibake. qjsdom, happy-dom, and httpx all assume UTF-8 instead. Chromium follows the spec and is still the one that gives you unusable output, which is worth knowing before you compare extraction quality on a page with no `<meta charset>`.

Our first fixture pages had no character set and our local server sent no charset either, so chromium lost the whole comparison run to mojibake. That was a bug in the fixture, not a difference between tools. Fix your fixtures before you read a chromium row.

happy-dom 21 has no `document.images`; the property is undefined. Use `document.querySelectorAll("img")` to count images. Our first image comparison reported zero for happy-dom because our probe trusted that property.

## Reproduce it

The fixture pages live in a scratch directory rather than this repository, since they measure the tools and not qjsdom. To rebuild the comparison, serve any HTML file from a local server and run the four commands below against the same URL.

```sh
# qjsdom
qjsdom -json http://127.0.0.1:8735/a.html

# httpx + markdownify
python3 -c 'import sys,httpx,markdownify; print(markdownify.markdownify(httpx.get(sys.argv[1],headers={"User-Agent":"Mozilla/5.0"}).text))' http://127.0.0.1:8735/a.html

# bun + happy-dom
bun -e 'import { Window } from "happy-dom"; const u = process.argv[2]; const w = new Window({url: u}); w.document.write(await (await fetch(u)).text()); await w.happyDOM.waitUntilComplete(); console.log(w.document.body.textContent)' http://127.0.0.1:8735/a.html

# chromium
python3 -c 'import sys; from playwright.sync_api import sync_playwright; p = sync_playwright().start(); b = p.chromium.launch(args=["--no-sandbox"]); pg = b.new_page(); pg.goto(sys.argv[1], wait_until="networkidle"); print(pg.evaluate("document.body.innerText")); b.close()' http://127.0.0.1:8735/a.html
```

Peak memory needs the process tree, not just the direct child, or Playwright's number comes back as the driver's 30 MB. Walk `/proc` for descendants of the process you started and sum the resident sizes while it runs.
