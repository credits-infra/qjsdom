// fetch / Response / Headers 的宿主桥 —— 让页面脚本里的 fetch() 真能拿到数据。
//
// 依赖 Go 侧注册的全局函数 __qjsdom_httpFetch(url, method) → JSON 字符串。
// 若该函数不存在(调用方关掉了 JS 网络能力、或离线渲染本地文件),本文件直接
// 返回:此时页面脚本调用 fetch 会照旧得到 "fetch is not defined",与改造前一致。
//
// 设计取舍:
//   * 宿主调用是【同步】的(与 imageProbe 一致):Go 侧阻塞到响应回来,
//     这里把结果包成一个已 resolve 的 Promise。浏览器里 fetch 是异步的,
//     但我们是抓取器:整页有硬超时,同步执行让「脚本 → 响应 → 回调」的顺序
//     完全确定,不依赖事件循环的调度。
//   * 回调仍然走微任务(Promise.then),由宿主在每次 eval 返回后抽干 ——
//     所以 fetch(...).then(cb) 里的 cb 一定在抽取正文之前跑完。
//   * 只放行 GET:POST/PUT 等于"用别人的服务器向第三方发写请求",默认不给。
//   * 跨域、cookie、credentials 一概不涉及:没有源的概念,也没有 cookie 罐。
(function () {
  if (typeof globalThis.__qjsdom_httpFetch !== "function") return;

  // ---- Headers ----
  // 名字统一小写(HTTP 头大小写不敏感,浏览器也这么存)。重复头合并成逗号串。
  class Headers {
    constructor(init) {
      this._m = Object.create(null);
      if (init && typeof init === "object") {
        for (const k of Object.keys(init)) {
          const v = init[k];
          this._m[String(k).toLowerCase()] = Array.isArray(v) ? v.join(", ") : String(v);
        }
      }
    }
    get(name) {
      const v = this._m[String(name).toLowerCase()];
      return v === undefined ? null : v;
    }
    has(name) { return String(name).toLowerCase() in this._m; }
    forEach(cb, thisArg) {
      for (const k of Object.keys(this._m)) cb.call(thisArg, this._m[k], k, this);
    }
    entries() { return Object.keys(this._m).map((k) => [k, this._m[k]])[Symbol.iterator](); }
    keys() { return Object.keys(this._m)[Symbol.iterator](); }
    values() { return Object.keys(this._m).map((k) => this._m[k])[Symbol.iterator](); }
  }

  // ---- Response ----
  // 只做到页面脚本会用到的那一层:status / ok / url / headers / text / json。
  // 二进制走不了这条路(宿主只回文本),所以 arrayBuffer 是"文本的 UTF-8 字节",
  // 这一点在 README 里写明,免得调用方以为能拿图片字节。
  class Response {
    constructor(info) {
      this._info = info || {};
      this.status = this._info.status || 0;
      this.statusText = this._info.statusText || "";
      this.ok = this.status >= 200 && this.status < 300;
      this.url = this._info.url || "";
      this.redirected = !!this._info.redirected;
      this.type = "basic";
      this.headers = new Headers(this._info.headers);
      this.bodyUsed = false;
    }
    text() {
      this.bodyUsed = true;
      return Promise.resolve(String(this._info.body || ""));
    }
    json() {
      this.bodyUsed = true;
      let v;
      try {
        v = JSON.parse(String(this._info.body || ""));
      } catch (e) {
        // 与浏览器一致:JSON 坏了是 reject,不是 throw(调用方多半只挂了 catch)
        return Promise.reject(new SyntaxError("JSON 解析失败: " + ((e && e.message) || e)));
      }
      return Promise.resolve(v);
    }
    arrayBuffer() {
      this.bodyUsed = true;
      let buf;
      try { buf = new TextEncoder().encode(String(this._info.body || "")).buffer; } catch (e) { buf = new ArrayBuffer(0); }
      return Promise.resolve(buf);
    }
    blob() {
      this.bodyUsed = true;
      // linkedom 没有 Blob;给个够用的替代品(只有 size/type/text)
      const body = String(this._info.body || "");
      return Promise.resolve({ size: body.length, type: this.headers.get("content-type") || "", text: () => Promise.resolve(body) });
    }
    clone() { return new Response(this._info); }
  }

  globalThis.Headers = globalThis.Headers || Headers;
  globalThis.Response = globalThis.Response || Response;

  const 拒 = (msg) => Promise.reject(new TypeError(msg));

  globalThis.fetch = function (input, init) {
    init = init || {};

    const method = String(init.method || "GET").toUpperCase();
    if (method !== "GET") {
      // 明确拒绝,而不是悄悄按 GET 发出去 —— 静默改语义比报错更难查。
      return 拒("qjsdom 的 fetch 只支持 GET(收到 " + method + ")");
    }

    let url = "";
    try {
      url = typeof input === "string" ? input : String((input && input.url) || "");
    } catch (e) { /* 下面统一报错 */ }
    if (!url) return 拒("fetch 需要一个 URL");

    let info;
    try {
      info = JSON.parse(String(__qjsdom_httpFetch(url, method)));
    } catch (e) {
      return 拒("宿主 fetch 调用失败: " + ((e && e.message) || e));
    }
    if (info && info.error) return 拒(info.error);

    return Promise.resolve(new Response(info));
  };

  // XMLHttpRequest 的常见用法(open/send/onload/status/responseText/responseJSON)
  // 在抓取场景里和 fetch 是同一件事,这里建在 fetch 之上,不另开一条网络通路。
  class XMLHttpRequest {
    constructor() {
      this.readyState = 0;
      this.status = 0;
      this.statusText = "";
      this.responseText = "";
      this.response = "";
      this.responseType = "";
      this.onload = null;
      this.onerror = null;
      this.onreadystatechange = null;
      this._headers = {};
      this._url = "";
      this._async = true;
    }
    setRequestHeader(k, v) { this._headers[String(k)] = String(v); }
    getResponseHeader(name) {
      const v = this._resp && this._resp.headers.get(name);
      return v === undefined ? null : v;
    }
    getAllResponseHeaders() {
      if (!this._resp) return "";
      let out = "";
      this._resp.headers.forEach((v, k) => { out += k + ": " + v + "\r\n"; });
      return out;
    }
    open(method, url, async) {
      this._method = String(method || "GET").toUpperCase();
      this._url = String(url || "");
      this._async = async !== false;
      this.readyState = 1;
      if (typeof this.onreadystatechange === "function") this.onreadystatechange();
    }
    abort() { this.readyState = 0; }
    send() {
      const self = this;
      fetch(this._url, { method: this._method })
        .then(function (resp) {
          self._resp = resp;
          self.status = resp.status;
          self.statusText = resp.statusText;
          self.readyState = 4;
          return resp.text();
        })
        .then(function (text) {
          self.responseText = text;
          self.response = self.responseType === "json" ? (function () { try { return JSON.parse(text); } catch (e) { return null; } })() : text;
          if (typeof self.onreadystatechange === "function") self.onreadystatechange();
          if (typeof self.onload === "function") self.onload();
        })
        .catch(function (e) {
          self.status = 0;
          self.readyState = 4;
          if (typeof self.onerror === "function") self.onerror(e);
        });
    }
  }
  globalThis.XMLHttpRequest = globalThis.XMLHttpRequest || XMLHttpRequest;
})();
