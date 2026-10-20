// page_runner.js —— 在 qjs 里解析页面、执行页面脚本、提取正文。
// 由 Go 侧把 __HTML__ / __URL__ / __BASE__ / __SELECTOR__ 等占位符替换为 JSON 字面量后注入。
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
  // navigator 得单独处理:linkedom 把它定义成【只读的访问器属性】,`globalThis.navigator = {...}`
  // 会静默失效 —— 实测页面脚本读到的仍是引擎默认值(UA = quickjs-ng/0.10.1、language = undefined),
  // 与我们想给的完全不符。改成在现有对象上逐属性 defineProperty 覆盖(不触发 setter,
  // 也就避开了"setter 内部引用未 stub 的 API"那个坑)。
  // UA 由 Go 侧传入(__USER_AGENT__),与抓取用的 UA 是同一个值 —— 页面脚本看到的身份
  // 不该和实际请求的身份不一致。
  (function () {
    const 覆盖 = (对象, 名, 值) => {
      try {
        Object.defineProperty(对象, 名, { value: 值, configurable: true, writable: true });
      } catch (e) { /* 覆盖不了就保持原样,绝不中断整页 */ }
    };
    let 目标 = null;
    try { 目标 = globalThis.navigator; } catch (e) { 目标 = null; }
    if (!目标) {
      目标 = {};
      try {
        Object.defineProperty(globalThis, "navigator",
          { value: 目标, configurable: true, writable: true });
      } catch (e) { /* 定义不了就只能算了 */ }
    }
    覆盖(目标, "userAgent", __USER_AGENT__);
    覆盖(目标, "language", "zh-CN");
  })();
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
    // 调用方是否要执行页面脚本。关掉时仍然解析 HTML 并提取正文。
    const RUN_SCRIPTS = __RUN_SCRIPTS__;
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
    // 脚本类型判定 —— 与 Go 侧内联外链脚本用同一套规则:
    //   * 空 / JS MIME → 可执行;
    //   * type=module → 内联的按普通脚本执行(import 解析不了,记一条),
    //     外链的跳过(Go 侧已记过);
    //   * application/json、ld+json、text/template 之类是【数据块】,不是脚本。
    //     浏览器不执行它们,我们也不该执行 —— 实测照跑会往 scriptErrs 里灌三条
    //     "expecting ';'" / "unexpected token '<'" 之类的噪音,把真错误埋掉。
    const 脚本类型 = (s) => {
      try { return String((s.getAttribute && s.getAttribute("type")) || "").trim().toLowerCase().split(";")[0].trim(); }
      catch (e) { return ""; }
    };
    const 是可执行类型 = (t) => t === "" || t === "text/javascript" ||
      t === "application/javascript" || t === "application/x-javascript" ||
      t === "text/ecmascript" || t === "application/ecmascript" || t === "text/jscript";
    let 已提示模块 = false;

    // 脚本执行轮数上限:第一轮 + document.write 注入的后续批。
    // 3 轮足够覆盖"脚本写出脚本再写出内容"的常见形态,又不至于让自我复制的
    // 脚本把整页拖住。
    const 脚本轮数上限 = 3;

    // 单脚本字节上限(由 Go 侧传入,与 scriptsrc.go 的 maxScriptSrcBytes 同源)
    const 最大脚本字节 = __MAX_SCRIPT_BYTES__;

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
      // document.write / writeln / open / close 的 shim —— linkedom 没有这四个方法。
      // 只补这一层:解析交给 innerHTML(linkedom 自己的解析器),搬运用原生 DOM 方法。
      try {
        if (typeof __qjsdom_安装documentWrite === "function") __qjsdom_安装documentWrite(document);
      } catch (e) { /* 装不上不算致命,页面脚本照旧能跑 */ }
      if (typeof document.defaultView !== "undefined" && document.defaultView) {
        globalThis.window = document.defaultView;
        globalThis.window.document = document;
      }

      // 关掉脚本时,解析完就把文档交给宿主,跳过整个脚本循环。
      // 外链脚本在 Go 侧也不会被取回(见 renderer.Render),所以这里不会出现
      // "有脚本没跑"的统计矛盾:scriptTags 照实计数,scriptsRan 为 0。
      if (!RUN_SCRIPTS) {
        scripts = Array.from(document.querySelectorAll("script"));
        globalThis.__qjsdom_脚本统计 = { ran: 0, errs: errs, scripts: scripts };
        return JSON.stringify({ phase: "scripts", scriptTags: scripts.length, scriptsRan: 0 });
      }

      // ---- 2. 执行页面内联脚本 ----
      // 浏览器行为:解析到 <script> 就在全局作用域执行,var 会挂到 window 上。
      // 这里用 (0, eval) 走间接调用 → 拿到全局作用域语义,而不是本函数的局部作用域。
      // 单个脚本失败绝不允许影响后续 —— 逐个 try 包裹。
      // 分轮执行:第一轮是文档里原有的脚本;之后每轮处理 【document.write 注入
      // 进来的脚本】(浏览器也会执行它们 —— 旧式页面就靠这个把内容写进文档)。
      // 轮数必须设上限:脚本可以写出新脚本,不设限就是死循环。
      const 标记已跑 = (s) => { try { s.__qjsdom已执行 = true; } catch (e) {} };
      const 跑过没 = (s) => { try { return !!s.__qjsdom已执行; } catch (e) { return false; } };
      const 允许外链 = __ALLOW_SCRIPT_SRC__;

      for (let 轮 = 0; 轮 < 脚本轮数上限; 轮++) {
        const 本轮 = Array.from(document.querySelectorAll("script")).filter((s) => !跑过没(s));
        if (!本轮.length) break;
        for (const s of 本轮) {
          标记已跑(s);

          // 外链脚本(由 Go 侧取回并内联)带来源标签 —— 报错时点名,否则
          // 页面上脚本一多就只剩下一句"某某不是函数"。
          let 来源 = "";
          try { 来源 = s.getAttribute("data-qjsdom-from") || ""; } catch (e) {}

          let src = "";
          try { src = (s.getAttribute && s.getAttribute("src")) || ""; } catch (e) {}

          const 类型 = 脚本类型(s);
          const 模块 = 类型 === "module";
          // 数据块(JSON-LD / JSON / 模板)不是脚本,一律不执行
          if (!是可执行类型(类型) && !模块) continue;
          if (模块 && !已提示模块) {
            已提示模块 = true;
            if (!src) errs.push("note: 内联 type=module 脚本按普通脚本执行(import 不会解析)");
          }

          if (src) {
            // ①Go 侧判定过"不执行"的脚本带 data-qjsdom-skip —— 它的判定是最终的。
            // 少了这一句,Go 侧那些上限(404 / text-html / 超上限 / 数量上限 /
            // module / 数据块)会被这里的重新取回全部绕过:实测 404 的 JS 体与
            // 超上限脚本的前缀都被执行了,而 scriptErrs 同时写着"取不到"(自相矛盾)。
            let 跳过原因 = "";
            try { 跳过原因 = s.getAttribute("data-qjsdom-skip") || ""; } catch (e) {}
            if (跳过原因) continue;

            // ②剩下的是【document.write 注入的外链脚本】,只能在 JS 侧取回 ——
            // 走同一条守卫通路(协议白名单 + SSRF 守卫),次数也算进每页上限。
            if (模块) {
              errs.push("document.write 注入的 module 脚本未执行(" + src + "): 需要 import 支持");
              continue;
            }
            if (!允许外链) {
              errs.push("document.write 注入的外链脚本未执行(" + src +
                "): 外链脚本加载或页面脚本的网络能力已关闭(-no-script-src / -no-js-fetch)");
              continue;
            }
            if (typeof globalThis.__qjsdom_httpFetch !== "function") {
              errs.push("document.write 注入的外链脚本未执行(" + src + "): 页面脚本的网络能力已关闭");
              continue;
            }
            let r;
            try { r = JSON.parse(String(__qjsdom_httpFetch(src, "GET"))); } catch (e) { r = { error: String(e) }; }
            if (!r || r.error) {
              errs.push("document.write 注入的外链脚本取不到(" + src + "): " + ((r && r.error) || "未知错误"));
              continue;
            }
            // ③与 Go 侧同等的校验:非 2xx、声明 text/html、超过单脚本上限的都不执行。
            // 少了这一段,document.write 注入的脚本就成了一条更宽松的第二通路。
            if (!(r.status >= 200 && r.status < 300)) {
              errs.push("document.write 注入的外链脚本未执行(" + src + "): HTTP " + r.status);
              continue;
            }
            const 类型头 = String((r.headers || {})["content-type"] || "").toLowerCase();
            if (类型头.indexOf("html") >= 0) {
              errs.push("document.write 注入的外链脚本未执行(" + src + "): 响应类型是 " + 类型头 + ",不是脚本");
              continue;
            }
            if (r.truncated || Number(r.bytes || 0) > 最大脚本字节) {
              errs.push("document.write 注入的外链脚本未执行(" + src +
                "): 超过单脚本上限 " + 最大脚本字节 + " 字节");
              continue;
            }
            try {
              globalThis.__qjsdom_当前脚本 = s;
              (0, eval)(String(r.body || ""));
              ran++;
            } catch (e) {
              errs.push(脚本错误信息(e, src));
            } finally {
              globalThis.__qjsdom_当前脚本 = null;
            }
            continue;
          }

          let code = "";
          try { code = s.textContent || ""; } catch (e) { continue; }
          if (!code.trim()) continue;
          try {
            globalThis.__qjsdom_当前脚本 = s; // document.write 靠它知道"写哪儿"
            (0, eval)(code);
            ran++;
          } catch (e) {
            errs.push(脚本错误信息(e, 来源));
          } finally {
            // 跑完就清空:此后(微任务、定时器里)的 document.write 会走
            // "追加到 body 并记提示"那条明确的路,而不是写进早就跑完的脚本后面。
            globalThis.__qjsdom_当前脚本 = null;
          }
        }
      }
      // 轮数用尽还有没跑的,说明 write 还在往外写脚本 —— 记一条,别静默丢
      if (Array.from(document.querySelectorAll("script")).filter((s) => !跑过没(s)).length > 0) {
        errs.push("note: 脚本执行轮数达到上限(" + 脚本轮数上限 + "),document.write 注入的后续脚本未执行");
      }
      // scriptTags 取【最终】文档里的数量:write 可能又写进来一批
      scripts = Array.from(document.querySelectorAll("script"));

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
    // shim 记下的页面级提示(document.write 在脚本之外被调用等)在【这里】读:
    // 微任务 —— 以及其中的 document.write —— 是在阶段一的 eval 返回【之后】
    // 才被宿主抽干执行的,阶段一末尾去读会读空。
    try {
      if (Array.isArray(globalThis.__qjsdom_页面提示)) errs = errs.concat(globalThis.__qjsdom_页面提示);
    } catch (e) {}

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
      // HTML 解析器会把 CRLF 归一成 LF;linkedom 的 textContent 保留原文。
      // 不归一的话,同一条链接标题在静态路径与 JS 路径上会差一个 \r,
      // 正文的 textLen 也会被换行符放大。
      .replace(/\r\n?/g, "\n")
      .replace(/[ \t\u00a0]+/g, " ")
      .replace(/\n\s*\n\s*\n+/g, "\n\n")
      .trim();

    // 相对地址的基准,不是文档地址:页面里第一个 <base href> 会顶掉文档地址,
    // 浏览器就是这么做的(links / images 的相对路径都按它补全)。
    const PAGE_URL = __BASE__;

    // HTML 实体解码。getAttribute 不解码,`&amp;` 会原样带出来 —— 链接和图片
    // 都要过这一道,否则带查询串的地址会被当成字面值:实测维基缩略图的
    // `?utm_source=…&amp;utm_campaign=…` 原样进了抓取地址,也原样进了文件名。
    // 一次扫描表替换,不能写成一条条 .replace() 串起来:那样 `&amp;lt;` 会先被解成
    // `&lt;`、再被同一条链上的 `&lt;` 规则解成 `<` —— 连解两层。带查询串的地址里
    // 出现字面量 `&lt;` 是合法的(页面自己转义过一次),多解一层等于改写了地址。
    const 实体表 = { amp: "&", lt: "<", gt: ">", quot: '"', "#39": "'", "#38": "&" };
    const decodeEntities = (raw) => String(raw || "").trim()
      .replace(/&(amp|lt|gt|quot|#39|#38);/g, (_, 名) => 实体表[名]);

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
          // 标题和地址一样要解实体:linkedom 的 textContent 会留下 `&amp;`,
          // 而静态路径的解析器已经解过;两条路径必须给出同一个标题。
          const t = decodeEntities(normText(a.textContent || "")).replace(/\n+/g, " ");
          // 过滤掉纯图标链接与超长文本(后者多半是整段包裹在 <a> 里的导航)。
          // 用码点而不是 UTF-16 单元计数:静态路径用 utf8.RuneCountInString,
          // 一个 emoji 在两边都该算 1 个字,而不是 JS 的 length 算 2。
          const 标题长度 = Array.from(t).length;
          if (!t || 标题长度 < 2 || 标题长度 > 300) continue;
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
