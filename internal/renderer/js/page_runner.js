// page_runner.js —— 在 qjs 里解析页面、执行页面脚本、提取正文。
// 由 Go 侧把 __HTML__ / __URL__ / __SELECTOR__ 三个占位符替换为 JSON 字面量后注入。
// 注意:本脚本在【每个页面一个全新 Runtime】里运行,所以可以放心用全局变量。

globalThis.window = globalThis;
globalThis.self = globalThis;
globalThis.top = globalThis;
// 注意:linkedom 会自己定义 navigator / location,且是只读的。
// 直接赋值会抛 "is read-only",所以这里统一 try 包裹 —— 能覆盖就覆盖,不能就沿用 linkedom 的。
try {
  globalThis.navigator = { userAgent: "Mozilla/5.0 (compatible; qjsdom/0.1)", language: "zh-CN" };
} catch (e) { /* linkedom 已定义只读 navigator,沿用 */ }
try {
  globalThis.location = {
    href: __URL__,
    toString() { return __URL__; },
  };
} catch (e) { /* 同上 */ }

(function () {
  const { parseHTML } = Linkedom;

  // ---- 1. 解析 HTML ----
  const { document } = parseHTML(__HTML__);
  globalThis.document = document;
  if (typeof document.defaultView !== "undefined" && document.defaultView) {
    globalThis.window = document.defaultView;
    globalThis.window.document = document;
  }

  // ---- 2. 执行页面内联脚本 ----
  // 浏览器行为:解析到 <script> 就在全局作用域执行,var 会挂到 window 上。
  // 这里用 (0, eval) 走间接调用 → 拿到全局作用域语义,而不是本函数的局部作用域。
  const scripts = Array.from(document.querySelectorAll("script"));
  let ran = 0;
  const errs = [];
  for (const s of scripts) {
    // 带 src 的脚本本轮不处理(需要宿主提供网络,见 fetchExternalScripts)
    if (s.getAttribute && s.getAttribute("src")) continue;
    const code = s.textContent || "";
    if (!code.trim()) continue;
    try {
      (0, eval)(code);
      ran++;
    } catch (e) {
      errs.push(String(e && e.message ? e.message : e).slice(0, 90));
    }
  }

  // ---- 3. 提取正文 ----
  const SELECTOR = __SELECTOR__;
  let node = null;
  if (SELECTOR) {
    node = document.querySelector(SELECTOR);
  }
  if (!node) {
    // 常见正文容器,按优先级试着找
    const fallbacks = [
      "article", "main", "[role=main]", "#mw-content-text", "#content",
      ".content", ".article", ".post", "#main", ".main", "body",
    ];
    for (const s of fallbacks) {
      const n = document.querySelector(s);
      if (n && (n.textContent || "").trim().length > 200) { node = n; break; }
    }
  }
  if (!node) node = document.body || document.documentElement;

  let text = "";
  try {
    text = (node.textContent || "").replace(/[ \t\u00a0]+/g, " ").replace(/\n\s*\n\s*\n+/g, "\n\n").trim();
  } catch (e) {
    text = "";
  }

  // ---- 4. 顺带收集链接与图片(供后续功能使用)----
  const links = [];
  try {
    for (const a of document.querySelectorAll("a[href]")) {
      const h = a.getAttribute("href");
      if (h && !h.startsWith("#") && !h.startsWith("javascript:")) links.push(h);
      if (links.length >= 200) break;
    }
  } catch (e) {}
  const images = [];
  try {
    for (const im of document.querySelectorAll("img[src]")) {
      const srcv = im.getAttribute("src");
      if (srcv) images.push(srcv);
      if (images.length >= 50) break;
    }
  } catch (e) {}

  return JSON.stringify({
    title: document.title || "",
    text: text,
    textLen: text.length,
    scriptTags: scripts.length,
    scriptsRan: ran,
    scriptErrs: errs,
    links: links,
    images: images,
    hasBody: !!document.body,
  });
})()
