# qjsdom

**纯 Go 的网页渲染与内容提取 —— 不要 Node,不要浏览器。**

用 [qjs](https://github.com/fastschema/qjs)(QuickJS via Wazero WASM)+
[linkedom](https://github.com/WebReflection/linkedom) 在 Go 进程内解析网页、
执行页面 JavaScript、提取正文与图片。CGO-free,单个静态二进制。

## 为什么

给聊天机器人加「读网页」能力时,常见路线都有明显代价:

| 方案 | 内存 | 依赖 | 问题 |
|---|---|---|---|
| Playwright / chromium | **1483 MB** | 已装 ~500MB | 2GB 小机器装不下 |
| Node + jsdom | 43 MB | 需要 Node 运行时 | 要维护第二个运行时 |
| Bun + happy-dom | 53 MB | 需要 Bun 运行时 | 同上 |
| **qjsdom** | **~80 MB** | **无(单二进制)** | 见「局限」 |

## 实测数据

本机 2 核 / 1857 MB AlmaLinux,维基百科「糖酵解」条目(432 KB HTML):

```
标题 ✅          脚本 2/5 执行成功        正文 17776 字(mode=all)
图片收集 87 张    图片下载 20/20 成功      下载总量 888 KB
耗时 3.4s        峰值 RSS 79.7 MB         二进制 13.3 MB(含 0.5 MB linkedom bundle)
```

Google 搜索页(90 KB):脚本 **5/5** 执行成功 —— 补 Web API stub 之前是
0/5 且整页崩溃。

## 提取策略(mode)

**策略由调用方指定,不做自动判型。** 不同页面的「正文」定义本来就不同:

| mode | 适用页面 | 行为 |
|---|---|---|
| **`all`**(默认) | 通用 / 不确定时 | 整页文本 + 链接列表,**什么都不丢** |
| **`article`** | 文章、百科、博客、文档 | 按固定优先级找正文容器 |
| **`links`** | 搜索页、目录页、导航页 | 链接列表(标题 + URL)—— 链接即内容 |

```bash
qjsdom https://example.com                                    # all
qjsdom -mode article https://example.com/blog                 # 读文章
qjsdom -mode links "https://html.duckduckgo.com/html/?q=x"    # 读搜索结果
```

### 为什么不自动判型

「从页面特征反推意图」的启发式必然在混合页上出错 —— 链接密度落在
15%~70% 之间的页面该怎么办?而**意图信息本来就由调用方持有**:
模型调这个工具时,它知道自己是在「找东西」还是在「读文章」。
让它传一个参数是零成本的,猜错却是必然的。

选错 mode 的代价只是一次重新渲染(抓取与渲染是分开的两步)。

### `article` 的边界(重要)

`article` **不是「智能识别正文」**,它是**按固定优先级猜**:

```
article → main → [role=main] → #mw-content-text →
.post-content → .entry-content → .article-body →
#content → .content → .article → .post → #main → .main
```

取第一个文本量 ≥ 200 字的。**猜不中是常态,不是 bug。** 实测维基条目:

```
article 自动找:                        17111 字   ← 仍含侧栏/语言列表等导航
article + -selector "#mw-content-text": 16119 字   ← 干净正文
```

那 1000 字差的就是导航。**遇到这种情况请用 `-selector` 精确指定。**

曾实现过链接密度评分(在候选里选"最像正文"的),实测结果与 `all`
只差 665 字、且选中的容器仍然含导航 —— 收益远不抵复杂度,已移除。
现在的设计是:**简单的固定顺序 + 明确说明边界 + 留一个精调开关**。

### 常用站点精调参考

维基百科用 `#mw-content-text`(已实测:自动找 17111 字,指定后 16119 字,
差的正是导航部分)。其它站点先用 `-mode all` 看整页结构,或在浏览器里右键
「检查」找到正文容器的 id / class,再传给 `-selector`。

刻意只列验证过的选择器 —— 未经实测就写进文档,等于编造。

## 图片

qjsdom **会下载**页面图片 —— 图片内容是有价值的产出:支持图像输入的模型
(Gemini 等)能直接看图,课本插图里的数值、箭头、流向、调控关系都在图里。

```bash
qjsdom -download-images -images-dir /tmp/imgs -mode article <url>
```

- **懒加载已处理**:现代站点的 `<img>` 里 `src` 常是 1x1 的 base64 占位 GIF,
  真实地址在 `data-src` / `data-srcset` 等属性里。实测维基:只看 `src`
  收集 **0 张**,处理后 **87 张**。
- **`<picture><source srcset>`** 也会收集。
- 只接受 `image/*` 响应(有些站点用 HTML 伪装成图片地址);文件名用 URL
  的 sha1,避免路径穿越与重名;单张上限 5MB,单页最多 20 张。
- **`new Image()` 会真的去 fetch**(取尺寸,不保留像素)——
  页面脚本普遍用它做预加载和布局判断,给假数据会让它们走进错误分支。
- **视频 / 音频刻意不实现**:体积大、处理慢,不是我们要的信息载体。

## 缓存

同一个 URL 换 mode 重试时,qjsdom 不必重新抓取 —— 这正是「选错 mode 就换一个
再试」能成立的前提。它缓存的是 **HTML 源码**而不是提取结果:抓取与渲染本就是
分开的两步,所以换 mode 只重跑渲染。

```bash
qjsdom -cache -mode article <url>    # 抓取并缓存
qjsdom -cache -mode all <url>        # 命中缓存,只重跑渲染
```

两道防线防止缓存挤爆磁盘:

| 防线 | 默认值 | 作用 |
|---|---|---|
| TTL | 10 分钟 | 超时的条目视为失效 —— 网页会变,不能无限信任 |
| 容量上限 | 64 MB | 超出后按最久未访问淘汰(LRU) |

qjsdom 在每次写入前后各清理一次,所以长期运行也不会无限增长。单条超过总容量
上限的页面会被拒绝缓存 —— 不值得为它挤掉整个缓存。目录权限 0700,文件 0600。

用 `-cache-clear` 清空缓存,或用 `-cache-dir` / `-cache-ttl` / `-cache-max` 调整。

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

jsdom 执行页面脚本靠 `node:vm`,那是 Node 的宿主能力,要在 Go 里补等于重写
Node。linkedom 零 Node 依赖,打包后可直接注入 —— 页面脚本由我们自己用
`(0, eval)(code)` 在全局作用域执行,等价于浏览器的 `<script>` 语义
(`var` 会挂到 `window` 上)。

### 3. 宿主 API 全部在 JS 侧 shim

`internal/renderer/js/host_shims.js` 用纯 JS 补上 `Buffer` / `TextEncoder` /
`TextDecoder` / `process` / `URL` / `URLSearchParams` / `Image` /
`localStorage` / `history` / `requestAnimationFrame` / `MutationObserver` 等。
**Go 侧零 DOM 代码。**

`Image` 缺失会让 **linkedom 自己**不可用(不只是页面脚本报错)——
实测 Google 页面:补 stub 前脚本 0/5 且整页崩溃,补后 5/5。

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

### 5. 异常绝不中断整页

页面脚本本来就该「跑得动就跑、跑不动跳过」。runner 逐个捕获并记录每个
`<script>` 的错误;DOM 库自身出错时,它退回「粗暴去标签」的文本提取,
结果里标 `degraded: true`,而不是返回错误。

注意 `linkedom` 把 `navigator` / `location` / `window` 定义为**访问器属性** ——
直接赋值会触发它的 setter,而 setter 内部可能引用未 stub 的 API
(这就是 Google 页面 `Image is not defined` 抛在我们 try 之外的真因)。
所以这些赋值全部 try 包裹,失败只跳过该项。

### 6. SSRF 防护

`internal/fetcher` 会拒绝解析到内网 / 回环 / 链路本地 / 云元数据地址的目标 ——
这个组件将来要暴露给聊天群里的任意人调用,不能让它变成内网探测器。

## 用法

```bash
go build -o qjsdom ./cmd/qjsdom
```

```
-mode string        提取策略: all(默认) / article / links
-selector string    正文容器 CSS 选择器(对 all / article 生效)
-max int            正文截断长度,0=不截断
-file string        渲染本地 HTML 文件,不联网
-json               以 JSON 输出(含脚本统计、链接、图片)
-no-scripts         不执行页面内联脚本(仅静态解析)
-no-images          不启用 Image 网络探测
-download-images    下载页面图片(需配合 -images-dir)
-images-dir string  图片保存目录
-max-images int     最多下载几张图片(默认 20)
-stats              诊断信息打印到 stderr
-timeout duration   抓取超时(默认 20s)
-cache              启用页面源码缓存(同一 URL 换 mode 重试时不必重新抓取)
-cache-dir string   缓存目录(默认:用户缓存目录/qjsdom)
-cache-ttl duration 缓存有效期(默认 10m)
-cache-max int      缓存总容量上限,字节(默认 64MB)
-cache-clear        清空缓存后退出
-allow-private      允许访问内网地址(仅本地测试)
-version            打印版本
```

**flags 可以写在位置参数之后**(`qjsdom <url> -json` 也能用)——
Go 标准库的 flag 包默认在第一个非 flag 参数处停止解析,会**静默丢弃**
后面的 flag(不报错)。这个坑太隐蔽,所以做了参数重排。

## 重建 linkedom bundle

`internal/renderer/assets/linkedom.core.js` 是 esbuild 产物。要更新:

```bash
./scripts/build-bundle.sh
```

它会:① 用 `--alias:canvas=` 顶掉可选依赖;② 打包成 IIFE;③ 把
`js/host_shims.js` 前置拼接进去。

## 目录

```
cmd/qjsdom/                 CLI 入口
internal/renderer/          引擎:qjs 加载 + 页面处理
  js/host_shims.js          宿主 API 的纯 JS shim
  js/image_fetch.js         Image 的真实网络实现(覆盖 shim 版)
  js/page_runner.js         解析 HTML / 执行脚本 / 按 mode 提取
  assets/linkedom.core.js   esbuild 产物(shims + linkedom)
internal/fetcher/           HTTP 抓取 + SSRF 防护 + 图片探测 / 下载
scripts/build-bundle.sh     重建 bundle
testdata/                   离线测试页
```

## 局限(诚实列出)

- **不渲染 CSS 布局** —— 和 jsdom / happy-dom 一样,只是「假 DOM」。
  真正依赖布局计算的页面(滚动懒加载、虚拟滚动)拿不到内容。
- **完整 SPA 跑不动** —— 没有 `fetch` / `XHR` / History API。
  不过多数「Vue/React 页面」其实是 SSR/SSG(Next.js / Nuxt / Astro /
  VitePress),首屏 HTML 里就有内容,能处理;纯客户端 SPA 不行。
- **页面脚本执行率约 40%**(实测维基 2/5)—— 缺失的 Web API 会报错,
  但通常不影响内容提取。
- **不处理 `<script src="...">`** —— 需宿主提供网络。
- **`article` 靠固定优先级猜**,不保证命中,见上文「边界」。
- **中文编码**:`gbk`/`gb2312` 页面暂未转码(需引入 `golang.org/x/text`)。

## Roadmap

- [ ] 注入 Go 侧实现的 `fetch`,支持 `<script src>` 与页面内 XHR
- [ ] `<script src>` 加载与执行
- [ ] qjs `Compile()` 字节码缓存,降低每页注入开销
- [ ] `gbk`/`gb2312` 解码
- [ ] 输出 Markdown 而非纯文本
- [ ] 常驻服务模式(供 MCP / 其它进程调用)

## 许可

MIT
