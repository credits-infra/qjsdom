// page_runner.js —— 在 qjs 里解析页面、执行页面脚本、提取正文。
// 由 Go 侧把 __HTML__ / __URL__ / __SELECTOR__ 三个占位符替换为 JSON 字面量后注入。
// 注意:本脚本在【每个页面一个全新 Runtime】里运行,所以可以放心用全局变量。

// 全局环境绑定。
// ⚠️ 实测教训:linkedom 会把 navigator / location / window 等定义为【访问器属性】,
// 直接赋值会触发它的 setter,而 setter 内部可能引用我们尚未 stub 的 API
// (实测 Google 页面:globalThis.navigator = {...} 触发 "Image is not defined")。
// 所以这里统一用安全赋值:任何一项失败都只跳过该项,绝不中断整页处理。
(function () {
  const assign = (name, value) => {
    try { globalThis[name] = value; } catch (e) { /* 只读 / setter 抛错 → 跳过 */ }
  };
  assign("window", globalThis);
  assign("self", globalThis);
  assign("top", globalThis);
  assign("navigator", { userAgent: "Mozilla/5.0 (compatible; qjsdom/0.1)", language: "zh-CN" });
  assign("location", { href: __URL__, toString() { return __URL__; } });
})();

(function () {
  try {
    return runPage();
  } catch (e) {
    // 降级:DOM 库本身出错(例如内部引用了我们没 stub 的 API)时,
    // 绝不能整页失败 —— 用最粗暴的方式把可见文本抠出来返回。
    const msg = String((e && e.message) ? e.message : e).slice(0, 160);
    let text = "";
    try {
      text = String(__HTML__)
        .replace(/<script[\s\S]*?<\/script>/gi, " ")
        .replace(/<style[\s\S]*?<\/style>/gi, " ")
        .replace(/<noscript[\s\S]*?<\/noscript>/gi, " ")
        .replace(/<!--[\s\S]*?-->/g, " ")
        .replace(/<[^>]+>/g, " ")
        .replace(/&nbsp;/gi, " ")
        .replace(/&amp;/gi, "&")
        .replace(/&lt;/gi, "<")
        .replace(/&gt;/gi, ">")
        .replace(/&quot;/gi, '"')
        .replace(/&#39;/gi, "'")
        .replace(/[ \t\u00a0]+/g, " ")
        .replace(/\n\s*\n\s*\n+/g, "\n\n")
        .trim();
    } catch (_) { /* 兜底也失败就返回空 */ }

    // 尽力取个标题
    let title = "";
    try {
      const m = String(__HTML__).match(/<title[^>]*>([\s\S]*?)<\/title>/i);
      if (m) title = m[1].replace(/\s+/g, " ").trim();
    } catch (_) {}

    return JSON.stringify({
      title: title,
      text: text,
      textLen: text.length,
      scriptTags: 0,
      scriptsRan: 0,
      scriptErrs: ["渲染降级(未使用 DOM 解析): " + msg],
      links: [],
      images: [],
      hasBody: false,
      degraded: true,
    });
  }

  // ---- 正常路径 ----
  function runPage() {
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
    // 单个脚本失败绝不允许影响后续 —— 逐个 try 包裹。
    const scripts = Array.from(document.querySelectorAll("script"));
    let ran = 0;
    const errs = [];
    for (const s of scripts) {
      if (s.getAttribute && s.getAttribute("src")) continue;
      let code = "";
      try { code = s.textContent || ""; } catch (e) { continue; }
      if (!code.trim()) continue;
      try {
        (0, eval)(code);
        ran++;
      } catch (e) {
        errs.push(String((e && e.message) ? e.message : e).slice(0, 90));
      }
    }

    // ---- 3. 提取正文 ----
    const SELECTOR = __SELECTOR__;
    let node = null;
    try {
      if (SELECTOR) node = document.querySelector(SELECTOR);
    } catch (e) { /* 选择器非法,落到自动探测 */ }
    if (!node) {
      const fallbacks = [
        "article", "main", "[role=main]", "#mw-content-text", "#content",
        ".content", ".article", ".post", "#main", ".main", "body",
      ];
      for (const s of fallbacks) {
        let n = null;
        try { n = document.querySelector(s); } catch (e) { continue; }
        if (n && (n.textContent || "").trim().length > 200) { node = n; break; }
      }
    }
    if (!node) node = document.body || document.documentElement;

    let text = "";
    try {
      text = (node.textContent || "")
        .replace(/[ \t\u00a0]+/g, " ")
        .replace(/\n\s*\n\s*\n+/g, "\n\n")
        .trim();
    } catch (e) { text = ""; }

    // ---- 4. 收集链接与图片 ----
    const links = [];
    try {
      for (const a of document.querySelectorAll("a[href]")) {
        const h = a.getAttribute("href");
        if (h && !h.startsWith("#") && !h.startsWith("javascript:")) links.push(h);
        if (links.length >= 200) break;
      }
    } catch (e) {}

    // 图片:必须处理【懒加载】。
    // 实测:维基条目的 <img> 里 src 是 1x1 的 base64 占位 GIF,真实地址在
    // data-src 等属性里 —— 只看 src 会一张都收集不到。
    // 另外 <picture><source srcset> 也是常见来源。
    const images = [];
    const pushImg = (u) => {
      if (!u) return;
      u = String(u).trim();
      if (!u || u.startsWith("data:") || u.startsWith("javascript:")) return;
      if (images.length < 100) images.push(u);
    };
    const firstOfSrcset = (ss) => {
      if (!ss) return "";
      const first = String(ss).split(",")[0].trim().split(/\s+/)[0];
      return first || "";
    };
    try {
      for (const im of document.querySelectorAll("img")) {
        let u = im.getAttribute("src");
        // src 缺失,或是内联占位图(懒加载常用 1x1 GIF) → 去懒加载属性里找
        const isPlaceholder = !u || (u.startsWith("data:") && u.length < 300);
        if (isPlaceholder) {
          for (const attr of ["data-src", "data-original", "data-lazy-src", "data-lazyload",
                              "data-actualsrc", "data-echo", "data-url", "data-image"]) {
            const v = im.getAttribute(attr);
            if (v && !v.startsWith("data:")) { u = v; break; }
          }
        }
        // 仍无 → srcset 取第一个候选
        if (!u || u.startsWith("data:")) {
          u = firstOfSrcset(im.getAttribute("srcset") || im.getAttribute("data-srcset"));
        }
        pushImg(u);
        if (images.length >= 100) break;
      }
      // <picture> 里的 <source srcset>
      if (images.length < 100) {
        for (const s of document.querySelectorAll("source[srcset], source[data-srcset]")) {
          pushImg(firstOfSrcset(s.getAttribute("srcset") || s.getAttribute("data-srcset")));
          if (images.length >= 100) break;
        }
      }
    } catch (e) {}

    return JSON.stringify({
      title: (function () { try { return document.title || ""; } catch (e) { return ""; } })(),
      text: text,
      textLen: text.length,
      scriptTags: scripts.length,
      scriptsRan: ran,
      scriptErrs: errs,
      links: links,
      images: images,
      hasBody: !!document.body,
      degraded: false,
    });
  }
})()
