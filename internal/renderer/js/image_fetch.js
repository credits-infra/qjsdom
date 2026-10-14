// Image 的「真实 fetch」实现 —— 覆盖 host_shims.js 里的简易版本。
//
// 依赖 Go 侧注册的全局函数 __qjsdom_imageProbe(url) → JSON 字符串
// ({ok,width,height,bytes,format,error})。若该函数不存在(例如离线渲染
// 本地文件、或调用方关掉了网络),本文件直接返回,保留 host_shims 的简易实现。
//
// 设计取舍:
//   * 只取尺寸/格式/字节数,**不保留像素数据** —— 模型看不了图片内容,
//     视频音频更是完全不需要,拿回来只是浪费内存。
//   * Go 侧探测是同步的,所以这里 setter 返回时尺寸已经就绪。
//     浏览器里 src 赋值后立即读 naturalWidth 会得到 0(异步加载),
//     我们这里直接给真值 —— 对页面脚本更友好,不会走进「等 onload」的死路。
//   * 有数量上限:避免一个含 500 张图的页面把抓取时间拖爆。
(function () {
  if (typeof globalThis.__qjsdom_imageProbe !== "function") return;

  const MAX_PROBES = 20; // 单页最多真实探测 20 张图
  const cache = new Map(); // url → 探测结果,同页内同 URL 只探一次
  let probes = 0;

  globalThis.Image = class Image {
    constructor(width, height) {
      this.width = width || 0;
      this.height = height || 0;
      this.naturalWidth = 0;
      this.naturalHeight = 0;
      this.complete = false;
      this.crossOrigin = null;
      this.decoding = "auto";
      this.loading = "eager";
      this.alt = "";
      this._src = "";
      this._listeners = {};
    }

    get src() { return this._src; }

    set src(v) {
      this._src = String(v);
      if (!this._src) {
        this.complete = true;
        return;
      }

      let res = cache.get(this._src);
      if (res === undefined) {
        if (probes >= MAX_PROBES) {
          // 超出配额:按「加载失败」处理,不再发起请求
          res = { ok: false, error: "超出单页图片探测配额" };
        } else {
          probes++;
          try {
            res = JSON.parse(__qjsdom_imageProbe(this._src));
          } catch (e) {
            res = { ok: false, error: "探测调用失败: " + String(e && e.message ? e.message : e) };
          }
        }
        cache.set(this._src, res);
      }

      this.complete = true;
      if (res && res.ok) {
        this.naturalWidth = res.width || 0;
        this.naturalHeight = res.height || 0;
        // 未显式指定尺寸时,按真实尺寸填充(与浏览器一致)
        if (!this.width) this.width = this.naturalWidth;
        if (!this.height) this.height = this.naturalHeight;
        this._fire("load", { type: "load", target: this });
      } else {
        this._fire("error", { type: "error", target: this });
      }
    }

    addEventListener(t, fn) { (this._listeners[t] = this._listeners[t] || []).push(fn); }
    removeEventListener(t, fn) {
      const a = this._listeners[t];
      if (a) this._listeners[t] = a.filter((f) => f !== fn);
    }
    _fire(t, ev) {
      const hs = this._listeners[t] || [];
      for (const fn of hs) { try { fn(ev); } catch (e) {} }
      const h = this["on" + t];
      if (typeof h === "function") { try { h(ev); } catch (e) {} }
    }
    decode() { return Promise.resolve(); }
  };

  // 暴露统计,便于 Go 侧诊断
  globalThis.__qjsdom_imageStats = function () {
    return JSON.stringify({ probes: probes, cached: cache.size });
  };
})();
