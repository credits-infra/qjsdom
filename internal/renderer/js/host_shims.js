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
