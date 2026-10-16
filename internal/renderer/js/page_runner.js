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
      mode: String(__MODE__ || "all").toLowerCase(),
    });
  }

  // ---- 正常路径 ----
  function runPage() {
    const PHASE = __PHASE__;
    const { parseHTML } = Linkedom;

    // ── 为什么拆成两个阶段 ──────────────────────────────────────────────
    // quickjs 的宿主循环(js_std_loop)在【每次 eval 返回之后】抽干微任务队列。
    // 页面脚本里的 Promise.then / fetch().then 回调都排在微任务里 —— 如果
    // 抽取和它们挤在同一次 eval,抽取就跑在回调【之前】,回调改的 DOM 全部
    // 白改(实测:then 里写入的文本进不了正文,拿到的还是旧内容)。
    // 所以:阶段一只解析+跑脚本,把舞台交给宿主去抽微任务;阶段二再抽取。
    let document = null;
    let scripts = [];
    let ran = 0;
    let errs = [];

    // 页面脚本出错时记下"哪个脚本 + 第几行"。
    // 实测:一句 "not a function" 在多脚本页面上等于没信息 —— 而 qjs 的 e.stack
    // 带行列号,顶帧就是页面脚本里的位置(形如 "at <eval> (<input>:2:21)")。
    // 外链脚本由 Go 侧内联时带了来源标签,拼在最前面。
    const 脚本错误信息 = (e, 来源) => {
      const msg = String((e && e.message) ? e.message : e).slice(0, 90);
      let 位置 = "";
      try {
        const m = String((e && e.stack) || "").match(/<input>:(\d+):(\d+)/);
        if (m) 位置 = " (脚本第 " + m[1] + " 行)";
      } catch (_) { /* 拿不到行号就算了,别让报错本身变成错误 */ }
      return ((来源 ? 来源 + ": " : "") + msg + 位置).slice(0, 140);
    };


    if (PHASE === "scripts") {
      // ---- 1. 解析 HTML ----
      ({ document } = parseHTML(__HTML__));
      globalThis.document = document;
      if (typeof document.defaultView !== "undefined" && document.defaultView) {
        globalThis.window = document.defaultView;
        globalThis.window.document = document;
      }

      // ---- 2. 执行页面内联脚本 ----
      // 浏览器行为:解析到 <script> 就在全局作用域执行,var 会挂到 window 上。
      // 这里用 (0, eval) 走间接调用 → 拿到全局作用域语义,而不是本函数的局部作用域。
      // 单个脚本失败绝不允许影响后续 —— 逐个 try 包裹。
      scripts = Array.from(document.querySelectorAll("script"));
      for (const s of scripts) {
        if (s.getAttribute && s.getAttribute("src")) continue;
        let code = "";
        try { code = s.textContent || ""; } catch (e) { continue; }
        if (!code.trim()) continue;
        // 外链脚本(由 Go 侧取回并内联)带来源标签 —— 报错时点名,否则
        // 页面上脚本一多就只剩下一句"某某不是函数"。
        let 来源 = "";
        try { 来源 = s.getAttribute("data-qjsdom-from") || ""; } catch (e) {}
        try {
          (0, eval)(code);
          ran++;
        } catch (e) {
          errs.push(脚本错误信息(e, 来源));
        }
      }

      // 交给宿主:这次 eval 一返回,微任务队列就会被抽干,之后才轮到阶段二。
      globalThis.__qjsdom_脚本统计 = { ran: ran, errs: errs, scripts: scripts };
      return JSON.stringify({ phase: "scripts", scriptTags: scripts.length, scriptsRan: ran });
    }

    // ---- 阶段二:抽取 ----
    // 文档与脚本统计都从全局取:既不重新解析(那会丢掉脚本刚做的改动),
    // 也绝不重跑脚本。
    document = globalThis.document;
    const st = globalThis.__qjsdom_脚本统计;
    if (!document) {
      // 阶段一没跑成(DOM 库在解析时就炸了)。这里只补解析、不跑脚本 ——
      // 重跑一遍脚本是错误的:副作用执行两次,而且多半还是会炸。
      ({ document } = parseHTML(__HTML__));
      globalThis.document = document;
      errs.push("note: 脚本阶段未完成,本次未执行页面脚本");
    } else if (st) {
      scripts = st.scripts || [];
      ran = st.ran || 0;
      errs = st.errs || [];
    }

    // ---- 3. 提取内容(按 mode 分流)----
    //
    // mode 由【调用方】指定,不做自动判型:
    //   意图信息本来就由调用方持有 —— 模型知道自己是在搜索还是在读文章,
    //   而"从页面特征反推意图"的启发式判型必然会在混合页上出错。
    //   all(默认)什么都不丢,选错时的重试成本只是一次渲染。
    const MODE = String(__MODE__ || "all").toLowerCase();

    // 是否需要把正文容器的 HTML 交回 Go 侧(调用方要 Markdown 时才要)。
    // 序列化整页 HTML 不是零成本,而且会给不需要它的调用方徒增体积,所以按需。
    const NEED_HTML = __NEED_HTML__;

    const normText = (s) => String(s || "")
      .replace(/[ \t\u00a0]+/g, " ")
      .replace(/\n\s*\n\s*\n+/g, "\n\n")
      .trim();

    const PAGE_URL = __URL__;

    // HTML 实体解码。getAttribute 不解码,`&amp;` 会原样带出来 —— 链接和图片
    // 都要过这一道,否则带查询串的地址会被当成字面值:实测维基缩略图的
    // `?utm_source=…&amp;utm_campaign=…` 原样进了抓取地址,也原样进了文件名。
    const decodeEntities = (raw) => String(raw || "").trim()
      .replace(/&amp;/g, "&").replace(/&#38;/g, "&")
      .replace(/&lt;/g, "<").replace(/&gt;/g, ">")
      .replace(/&quot;/g, '"').replace(/&#39;/g, "'");

    // 链接清理。三件事:
    //  · HTML 实体 —— getAttribute 不解码,&amp; 会原样带出来
    //  · 协议相对 URL(//host/path)→ 补当前页面的协议
    //  · 重定向包装 —— 搜索结果页普遍这样(DDG 的 uddg=、Google 的 url=/q=),
    //    真实地址藏在查询参数里,不解开的话给出去的全是跳转链
    const cleanURL = (raw) => {
      let u = decodeEntities(raw);
      if (!u) return "";
      if (u.startsWith("//")) {
        let proto = "https:";
        try {
          const m = String(PAGE_URL).match(/^([a-z]+:)/i);
          if (m) proto = m[1];
        } catch (e) {}
        u = proto + u;
      }
      try {
        const q = u.indexOf("?");
        if (q >= 0) {
          for (const key of ["uddg", "url", "q", "u", "target", "redirect"]) {
            const m = u.slice(q + 1).match(new RegExp("(?:^|&)" + key + "=([^&]+)"));
            if (!m) continue;
            let v = "";
            try { v = decodeURIComponent(m[1]); } catch (e) { v = m[1]; }
            // 只接受真正的绝对地址 —— 否则 q= 这类参数会把搜索词当 URL
            if (/^https?:\/\//i.test(v)) { u = v; break; }
          }
        }
      } catch (e) {}
      return u;
    };

    // 收集链接(标题 + URL)。links 模式把它当正文输出。
    const collectLinks = () => {
      const out = [];
      try {
        for (const a of document.querySelectorAll("a[href]")) {
          const h = a.getAttribute("href");
          if (!h || h.startsWith("#") || h.startsWith("javascript:")) continue;
          const t = normText(a.textContent || "").replace(/\n+/g, " ");
          // 过滤掉纯图标链接与超长文本(后者多半是整段包裹在 <a> 里的导航)
          if (!t || t.length < 2 || t.length > 300) continue;
          const url = cleanURL(h);
          if (!url) continue;
          out.push({ title: t, url: url });
          if (out.length >= 200) break;
        }
      } catch (e) {}
      return out;
    };

    // article 模式的候选容器,按语义强度排序,取第一个命中的。
    //
    // 刻意【不做评分排序】:mode 已经由调用方指定,「选哪个最好」这件事
    // 不在我们这里 —— 调用方觉得不准,传 selector 精确指定即可。
    // (曾实现过链接密度评分,实测结果与 all 只差 665 字、且选中的容器
    //  仍然含导航 —— 收益远不抵复杂度,已移除。)
    const ARTICLE_SELECTORS = [
      "article", "main", "[role=main]", "#mw-content-text",
      ".post-content", ".entry-content", ".article-body",
      "#content", ".content", ".article", ".post", "#main", ".main",
    ];

    let node = null;
    let text = "";
    let linkList = [];
    // 正文容器的 HTML(仅 NEED_HTML 时填充),给 Go 侧的 Markdown 转换用。
    let 正文HTML = "";

    if (MODE === "links") {
      // 搜索页 / 目录页 / 导航页:链接就是内容本身
      linkList = collectLinks();
      text = linkList.map((l) => l.title + "\n  " + l.url).join("\n\n");
    } else {
      if (MODE === "article") {
        // 按固定优先级取第一个像正文的容器(文本量 ≥ 200 字才算数)。
        // 不做评分、不做自动判型 —— 判断已经交给调用方了。
        try {
          for (const sel of ARTICLE_SELECTORS) {
            let n = null;
            try { n = document.querySelector(sel); } catch (e) { continue; }
            if (n && normText(n.textContent || "").length >= 200) { node = n; break; }
          }
        } catch (e) {}
      } else {
        // all(默认):整页文本,什么都不丢
        node = document.body || document.documentElement;
      }

      // 调用方显式指定容器时优先 —— 对 article / all 都生效。
      // mode 决定"怎么找正文",selector 是"直接指定正文在哪"。
      const SELECTOR = __SELECTOR__;
      if (SELECTOR) {
        try {
          const n = document.querySelector(SELECTOR);
          if (n) node = n;
        } catch (e) { /* 选择器非法,沿用 mode 的结果 */ }
      }

      // article 模式若一个候选都没命中,退回整页
      if (!node) node = document.body || document.documentElement;

      // 取文本时排除 <script>/<style> 的源码 —— 注意两点:
      //  ① 页面脚本【已经在上面执行过了】,这里删的不是"该跑的代码",
      //     而是跑完后残留在 DOM 里的源码文本(否则正文里会混进 JS)。
      //  ② 对【克隆体】操作,不动原始 DOM,免得影响页面脚本后续的异步逻辑。
      let target = node;
      try {
        target = node.cloneNode(true);
        for (const el of target.querySelectorAll("script,style,noscript,template")) {
          if (el.parentNode) el.parentNode.removeChild(el);
        }
      } catch (e) { target = node; }
      try { text = normText(target.textContent || ""); } catch (e) { text = ""; }
      // Markdown 走 Go 侧现成的转换器(见 internal/markdown):交出去的 HTML 就是
      // 上面这个已经去掉 script/style 的克隆体,两边取的是同一份内容 ——
      // 免得"纯文本里有、Markdown 里没有"这种对不上的情况。
      if (NEED_HTML) {
        try { 正文HTML = target.outerHTML || ""; } catch (e) { 正文HTML = ""; }
      }
      linkList = collectLinks();
    }

    // ---- 4. 收集图片(所有 mode 都需要)----
    // 图片:必须处理【懒加载】。
    // 实测:维基条目的 <img> 里 src 是 1x1 的 base64 占位 GIF,真实地址在
    // data-src 等属性里 —— 只看 src 会一张都收集不到。
    // 另外 <picture><source srcset> 也是常见来源。
    //
    // 收集范围跟随正文容器:文档级的 querySelectorAll("img") 会把页眉 LOGO、
    // 侧栏徽标一并算进来,而它们与"这页在讲什么"无关。调用方给了 selector
    // (或 article 模式选中了容器)时按容器收;没容器时(links 模式)才退回全文档。
    const 图片范围 = (node && typeof node.querySelectorAll === "function") ? node : document;
    const images = [];
    const pushImg = (raw) => {
      const u = decodeEntities(raw);
      if (!u || u.startsWith("data:") || u.startsWith("javascript:")) return;
      if (images.length < 100) images.push(u);
    };
    const firstOfSrcset = (ss) => {
      if (!ss) return "";
      const first = String(ss).split(",")[0].trim().split(/\s+/)[0];
      return first || "";
    };
    try {
      for (const im of 图片范围.querySelectorAll("img")) {
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
        for (const s of 图片范围.querySelectorAll("source[srcset], source[data-srcset]")) {
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
      links: linkList,
      images: images,
      html: 正文HTML,
      hasBody: !!document.body,
      degraded: false,
      mode: MODE,
    });
  }
})()
