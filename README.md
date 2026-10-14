# qjsdom

**纯 Go 的网页渲染与正文提取 —— 不要 Node,不要浏览器。**

用 [qjs](https://github.com/fastschema/qjs)(QuickJS via Wazero WASM)+
[linkedom](https://github.com/WebReflection/linkedom) 在 Go 进程内解析网页、
执行页面 JavaScript、提取正文。CGO-free,单个静态二进制。

## 为什么

给聊天机器人加「读网页」能力时,常见路线都有明显代价:

| 方案 | 内存 | 依赖 | 问题 |
|---|---|---|---|
| Playwright / chromium | **1483 MB** | 已装 ~500MB | 2GB 小机器装不下 |
| Node + jsdom | 43 MB | 需要 Node 运行时 | 要维护第二个运行时 |
| Bun + happy-dom | 53 MB | 需要 Bun 运行时 | 同上 |
| **qjsdom** | **~80 MB** | **无(单二进制)** | 见「局限」 |

## 实测数据(本机,2 核 / 1857 MB AlmaLinux)

```
维基百科「糖酵解」   432 KB HTML
  → 标题 ✅   脚本 2/5 执行成功   正文 29143 字
  → 峰值 RSS 79.7 MB
  → 二进制 13.3 MB(含 0.5 MB linkedom bundle)
```

## 关键设计决策(都有实测依据)

### 1. 为什么用 qjs 而不是 goja

goja 是 **ES5.1** 引擎。jsdom 的 bundle 里有 16161 处 `const`、6317 处 `??`、
656 处 `?.`,而且 esbuild **明确拒绝**降级到 ES5:

```
✘ Transforming const to the configured target environment ("es5") is not supported yet
```

qjs 完整支持 **ES2023 + async/await**,这一关直接过。

### 2. 为什么用 linkedom 而不是 jsdom

| | jsdom | linkedom |
|---|---|---|
| 打包产物 | 10.8 MB | **0.5 MB** |
| Node 内建依赖 | `fs` / `path` / `url` / **`vm`** | **零** |
| 跑页面 `<script>` | ✅(靠 `vm.runInContext`) | ❌(设计如此) |

jsdom 执行页面脚本靠 `node:vm`,那是 Node 的宿主能力,要在 Go 里补等于重写 Node。
linkedom 零 Node 依赖,打包后可直接注入 —— 页面脚本由我们自己用
`(0, eval)(code)` 在全局作用域执行,等价于浏览器的 `<script>` 语义。

### 3. 宿主 API 全部在 JS 侧 shim

`internal/renderer/js/host_shims.js` 用纯 JS 补上 `Buffer` / `TextEncoder` /
`TextDecoder` / `process` / `URL` / `URLSearchParams`。
**Go 侧零 DOM 代码。**

### 4. 每个页面一个全新 Runtime(重要)

实测:复用同一 Runtime 会让上一页的状态泄漏到下一页 ——

```
页面A:  var siteName = "页面A";
        Array.prototype.polluted = function(){...};

页面B 看到的: {"seenSiteName":"string", "arrayPolluted":"function"}
                                    ↑ 全局泄漏      ↑ 原型污染(无法手工清除)
```

而新建 Runtime 的开销**仅 ~211 ms**(首次含 WASM 加载约 900 ms)。
所以:**每页 `qjs.New()` → 跑完 `Close()`,状态零残留。**

### 5. SSRF 防护

`internal/fetcher` 会拒绝解析到内网/回环/链路本地/云元数据地址的目标 ——
这个组件将来要暴露给聊天群里的任意人调用,不能让它变成内网探测器。

## 用法

```bash
go build -o qjsdom ./cmd/qjsdom
```

```bash
# 抓取并提取正文
qjsdom https://example.com

# 指定正文容器
qjsdom -selector "#mw-content-text" https://zh.wikipedia.org/wiki/糖酵解

# 渲染本地 HTML(不联网)
qjsdom -file testdata/wiki.html -stats

# JSON 输出(含脚本统计、链接、图片)
qjsdom -json https://example.com

# 只做静态解析,不执行页面脚本
qjsdom -no-scripts https://example.com
```

## 重建 linkedom bundle

`internal/renderer/assets/linkedom.core.js` 是 esbuild 产物。要更新:

```bash
./scripts/build-bundle.sh
```

它会:① 用 `--alias:canvas=` 顶掉可选依赖;② 打包成 IIFE;③ 把
`js/host_shims.js` 前置拼接进去。

## 目录

```
cmd/qjsdom/             CLI 入口
internal/renderer/      引擎:qjs 加载 + 页面处理
  js/host_shims.js      宿主 API 的纯 JS shim
  js/page_runner.js     解析 HTML / 执行脚本 / 提取正文
  assets/linkedom.core.js   esbuild 产物(shims + linkedom)
internal/fetcher/       HTTP 抓取 + SSRF 防护
scripts/build-bundle.sh 重建 bundle
testdata/               离线测试页
```

## 局限(诚实列出)

- **不渲染 CSS 布局** —— 和 jsdom / happy-dom 一样,只是「假 DOM」。
  真正依赖布局计算的页面(懒加载、虚拟滚动)拿不到内容。
- **完整 SPA 跑不动** —— 没有 `fetch` / `XHR` / History API。
  (可后续用 Go 侧注入 `fetch` 实现)
- **页面脚本执行率约 40%**(实测维基 2/5)—— 缺失的 Web API 会报错,
  但正文提取通常不受影响。
- **不处理 `<script src="...">`** —— 需宿主提供网络,见 Roadmap。
- **中文编码**:`gbk`/`gb2312` 页面暂未转码(需引入 `golang.org/x/text`)。

## Roadmap

- [ ] 正文提取改用链接密度评分(现按标签名探测,会把导航算进去)
- [ ] 注入 Go 侧实现的 `fetch`,支持 `script src` 与页面内 XHR
- [ ] `<script src>` 加载与执行
- [ ] qjs `Compile()` 字节码缓存,降低每页注入开销
- [ ] `gbk`/`gb2312` 解码
- [ ] 输出 Markdown 而非纯文本
- [ ] 常驻服务模式(供 MCP / 其它进程调用)

## 许可

MIT
