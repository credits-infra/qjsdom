// 宿主 shim —— 用 esbuild --banner:js= 注入到 bundle 最前面。
// 目的:让 linkedom 在 qjs(非 Node、非浏览器)里也能加载。
// 原则:只补 linkedom 实际用到的那几个,能少则少。

// ---- Buffer:linkedom 只用它做 base64 解码(decodeBase64 / data URI)----
if (typeof globalThis.Buffer === "undefined") {
  const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  const decodeB64 = (str) => {
    const s = String(str).replace(/[^A-Za-z0-9+/]/g, "");
    const out = new Uint8Array((s.length * 3) >> 2);
    let p = 0, buf = 0, bits = 0;
    for (let i = 0; i < s.length; i++) {
      buf = (buf << 6) | b64chars.indexOf(s[i]);
      bits += 6;
      if (bits >= 8) {
        bits -= 8;
        out[p++] = (buf >> bits) & 0xff;
      }
    }
    return out.subarray(0, p);
  };

  globalThis.Buffer = {
    from(input, encoding) {
      if (encoding === "base64") return decodeB64(input);
      if (typeof input === "string") {
        const a = new Uint8Array(input.length);
        for (let i = 0; i < input.length; i++) a[i] = input.charCodeAt(i) & 0xff;
        return a;
      }
      if (input instanceof Uint8Array || Array.isArray(input)) return input;
      return new Uint8Array(0);
    },
    isBuffer: () => false,
    alloc: (n) => new Uint8Array(n),
    concat: (list) => {
      let total = 0;
      for (const x of list) total += x.length;
      const out = new Uint8Array(total);
      let o = 0;
      for (const x of list) { out.set(x, o); o += x.length; }
      return out;
    },
  };
}

// ---- TextEncoder / TextDecoder:linkedom 的字符集处理会碰到 ----
if (typeof globalThis.TextEncoder === "undefined") {
  globalThis.TextEncoder = class {
    encode(str) {
      const a = new Uint8Array(str.length * 3);
      let p = 0;
      for (let i = 0; i < str.length; i++) {
        let c = str.codePointAt(i);
        if (c > 0xffff) i++;
        if (c < 0x80) a[p++] = c;
        else if (c < 0x800) { a[p++] = 0xc0 | (c >> 6); a[p++] = 0x80 | (c & 63); }
        else if (c < 0x10000) { a[p++] = 0xe0 | (c >> 12); a[p++] = 0x80 | ((c >> 6) & 63); a[p++] = 0x80 | (c & 63); }
        else { a[p++] = 0xf0 | (c >> 18); a[p++] = 0x80 | ((c >> 12) & 63); a[p++] = 0x80 | ((c >> 6) & 63); a[p++] = 0x80 | (c & 63); }
      }
      return a.subarray(0, p);
    }
  };
}
if (typeof globalThis.TextDecoder === "undefined") {
  globalThis.TextDecoder = class {
    decode(buf) {
      const a = buf instanceof Uint8Array ? buf : new Uint8Array(buf || []);
      let s = "";
      for (let i = 0; i < a.length; ) {
        const b = a[i];
        if (b < 0x80) { s += String.fromCharCode(b); i++; }
        else if (b < 0xe0) { s += String.fromCharCode(((b & 31) << 6) | (a[i + 1] & 63)); i += 2; }
        else if (b < 0xf0) { s += String.fromCharCode(((b & 15) << 12) | ((a[i + 1] & 63) << 6) | (a[i + 2] & 63)); i += 3; }
        else {
          const cp = ((b & 7) << 18) | ((a[i + 1] & 63) << 12) | ((a[i + 2] & 63) << 6) | (a[i + 3] & 63);
          s += String.fromCodePoint(cp); i += 4;
        }
      }
      return s;
    }
  };
}

// ---- process:linkedom 只读 env 判断运行环境 ----
if (typeof globalThis.process === "undefined") {
  globalThis.process = { env: {}, version: "", platform: "qjs", argv: [] };
}

// ---- URL / URLSearchParams:linkedom 的 URL 处理 ----
if (typeof globalThis.URL === "undefined") {
  globalThis.URL = class URL {
    constructor(input, base) {
      const raw = String(input || "");
      const s = raw.startsWith("http") || raw.startsWith("data:") || raw.startsWith("blob:")
        ? raw
        : (base ? String(base).replace(/\/+$/, "") + "/" + raw : raw);
      this.href = s;
      const m = s.match(/^([a-zA-Z][\w+.-]*:)?(?:\/\/([^/?#]*))?([^?#]*)?(\?[^#]*)?(#.*)?$/);
      this.protocol = (m && m[1]) || "";
      this.host = (m && m[2]) || "";
      this.hostname = this.host.replace(/:\d+$/, "");
      this.pathname = (m && m[3]) || "";
      this.search = (m && m[4]) || "";
      this.hash = (m && m[5]) || "";
      this.origin = this.protocol + "//" + this.host;
    }
    toString() { return this.href; }
    toJSON() { return this.href; }
  };
}
if (typeof globalThis.URLSearchParams === "undefined") {
  globalThis.URLSearchParams = class URLSearchParams {
    constructor(init) {
      this._p = [];
      if (typeof init === "string") {
        for (const kv of String(init).replace(/^\?/, "").split("&")) {
          if (!kv) continue;
          const i = kv.indexOf("=");
          this._p.push([i < 0 ? kv : kv.slice(0, i), i < 0 ? "" : kv.slice(i + 1)]);
        }
      }
    }
    get(k) { const h = this._p.find((x) => x[0] === k); return h ? h[1] : null; }
    has(k) { return this._p.some((x) => x[0] === k); }
    toString() { return this._p.map(([a, b]) => a + "=" + b).join("&"); }
  };
}

// ============================================================================
// 以下为「高频缺失 Web API」的最小 stub。
//
// 实测动机:Google 搜索页让整个渲染挂掉,原因只是缺一个 Image ——
// 而且 linkedom 内部就会引用它(不是页面脚本的问题,关掉脚本照样崩)。
// 原则:只补「能让页面脚本继续跑下去」所需的最小行为;
// 视频/音频/Canvas 这类我们不需要结果的东西,给个空壳就行。
// ============================================================================

// ---- Image:linkedom 内部与页面脚本都会用 ----
// 完整版(真的去 fetch 取尺寸)在后续提交里由 Go 侧注入替换。
if (typeof globalThis.Image === "undefined") {
  globalThis.Image = class Image {
    constructor(width, height) {
      this.width = width || 0;
      this.height = height || 0;
      this.naturalWidth = 0;
      this.naturalHeight = 0;
      this.complete = false;
      this.crossOrigin = null;
      this._src = "";
      this._listeners = {};
    }
    get src() { return this._src; }
    set src(v) {
      this._src = String(v);
      // 没有网络层时:立即以「加载失败」收场,避免页面脚本永远等待
      this.complete = true;
      const ev = { type: "error", target: this };
      this._fire("error", ev);
    }
    addEventListener(t, fn) { (this._listeners[t] = this._listeners[t] || []).push(fn); }
    removeEventListener(t, fn) {
      const a = this._listeners[t];
      if (a) this._listeners[t] = a.filter((f) => f !== fn);
    }
    _fire(t, ev) {
      for (const fn of (this._listeners[t] || [])) { try { fn(ev); } catch (e) {} }
      const h = this["on" + t];
      if (typeof h === "function") { try { h(ev); } catch (e) {} }
    }
    decode() { return Promise.resolve(); }
  };
}

// ---- 存储:页面脚本常用来做特性检测,给内存实现即可 ----
function makeStorage() {
  const m = new Map();
  return {
    getItem: (k) => (m.has(String(k)) ? m.get(String(k)) : null),
    setItem: (k, v) => { m.set(String(k), String(v)); },
    removeItem: (k) => { m.delete(String(k)); },
    clear: () => { m.clear(); },
    key: (i) => Array.from(m.keys())[i] ?? null,
    get length() { return m.size; },
  };
}
if (typeof globalThis.localStorage === "undefined") globalThis.localStorage = makeStorage();
if (typeof globalThis.sessionStorage === "undefined") globalThis.sessionStorage = makeStorage();

// ---- history:SPA 路由用;只做不报错的最小实现 ----
if (typeof globalThis.history === "undefined") {
  globalThis.history = {
    length: 1,
    state: null,
    scrollRestoration: "auto",
    pushState() {}, replaceState() {}, back() {}, forward() {}, go() {},
  };
}

// ---- 定时与帧:页面脚本普遍依赖 ----
if (typeof globalThis.requestAnimationFrame === "undefined") {
  globalThis.requestAnimationFrame = (cb) => setTimeout(() => { try { cb(Date.now()); } catch (e) {} }, 16);
  globalThis.cancelAnimationFrame = (id) => clearTimeout(id);
}
if (typeof globalThis.requestIdleCallback === "undefined") {
  globalThis.requestIdleCallback = (cb) => setTimeout(() => { try { cb({ didTimeout: false, timeRemaining: () => 0 }); } catch (e) {} }, 1);
  globalThis.cancelIdleCallback = (id) => clearTimeout(id);
}

// ---- 事件与观察者 ----
if (typeof globalThis.CustomEvent === "undefined") {
  globalThis.CustomEvent = class CustomEvent {
    constructor(type, opts) {
      this.type = type;
      this.detail = (opts && opts.detail) !== undefined ? opts.detail : null;
      this.bubbles = !!(opts && opts.bubbles);
      this.cancelable = !!(opts && opts.cancelable);
      this.defaultPrevented = false;
    }
    preventDefault() { this.defaultPrevented = true; }
    stopPropagation() {}
  };
}
if (typeof globalThis.MutationObserver === "undefined") {
  globalThis.MutationObserver = class MutationObserver {
    constructor(cb) { this._cb = cb; }
    observe() {} disconnect() {} takeRecords() { return []; }
  };
}
if (typeof globalThis.IntersectionObserver === "undefined") {
  globalThis.IntersectionObserver = class IntersectionObserver {
    constructor(cb) { this._cb = cb; }
    observe() {} unobserve() {} disconnect() {} takeRecords() { return []; }
    get root() { return null; } get rootMargin() { return "0px"; } get thresholds() { return [0]; }
  };
}
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class ResizeObserver {
    constructor(cb) { this._cb = cb; }
    observe() {} unobserve() {} disconnect() {}
  };
}

// ---- 性能与屏幕:只读数值,给零值即可 ----
if (typeof globalThis.performance === "undefined") {
  const t0 = Date.now();
  globalThis.performance = {
    now: () => Date.now() - t0,
    timeOrigin: t0,
    mark() {}, measure() {}, clearMarks() {}, clearMeasures() {},
    getEntries: () => [], getEntriesByName: () => [], getEntriesByType: () => [],
  };
}
if (typeof globalThis.screen === "undefined") {
  globalThis.screen = { width: 1280, height: 800, availWidth: 1280, availHeight: 800, colorDepth: 24, pixelDepth: 24 };
}
if (typeof globalThis.devicePixelRatio === "undefined") globalThis.devicePixelRatio = 1;

// ---- 样式查询:不实现 CSS 计算,返回空对象即可(调用方通常只读几个属性)----
if (typeof globalThis.getComputedStyle === "undefined") {
  globalThis.getComputedStyle = () => ({
    getPropertyValue: () => "",
    length: 0,
    item: () => "",
  });
}

// ---- 媒体:我们不需要结果,给空壳避免页面脚本报错 ----
if (typeof globalThis.Audio === "undefined") {
  globalThis.Audio = class Audio {
    constructor(src) { this.src = src || ""; this.paused = true; this.volume = 1; }
    play() { return Promise.resolve(); }
    pause() {}
    addEventListener() {} removeEventListener() {}
  };
}
if (typeof globalThis.HTMLMediaElement === "undefined") {
  // linkedom 已有元素类;这里只兜底,避免 typeof 检测炸掉
  globalThis.HTMLMediaElement = function HTMLMediaElement() {};
}

// ---- 其它常见构造器兜底 ----
if (typeof globalThis.DOMParser === "undefined") {
  globalThis.DOMParser = class DOMParser {
    parseFromString(str) {
      try {
        return Linkedom.parseHTML(String(str));
      } catch (e) {
        return null;
      }
    }
  };
}
if (typeof globalThis.XMLHttpRequest === "undefined") {
  globalThis.XMLHttpRequest = class XMLHttpRequest {
    constructor() { this.readyState = 0; this.status = 0; this.responseText = ""; }
    open() {} setRequestHeader() {} send() {} abort() {}
    addEventListener() {} removeEventListener() {}
  };
}
