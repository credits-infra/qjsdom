// document.write / writeln / open / close 的 shim。
//
// ⚠️ 分工(文档里也要写明的这条线):
//   * 【linkedom 原生】HTML 解析 —— `容器.innerHTML = html` 走它自己的解析器
//     (实测:"<b>粗</b>文本" → 2 个节点,`<script>` 也能解析出节点);节点搬运
//     用 createElement / parentNode / firstChild / nextSibling / insertBefore;
//   * 【我们补的】document 上确实没有 write / writeln / open / close 四个方法
//     (实测 typeof 全是 undefined),这里只补这一层,不重写任何 DOM 逻辑。
//
// 为什么只补这一层:解析器自己写一遍是最不该做的事(实体、注释、表格补全、
// 错误恢复全是坑),而 linkedom 已经有解析器,innerHTML 就是它的入口。
//
// 位置语义:写入的节点插在【当前正在执行的 <script> 之后】,与浏览器一致 ——
// 旧式页面正是靠这个把内容"写"在脚本所在的位置。没有当前脚本时(例如在
// 微任务/定时器里调用,页面早已解析完)退回追加到 <body> 末尾,并记一条提示:
// 浏览器此时会清空整个文档重写,那个语义对抓取没有价值,只会毁掉已拿到的内容。
//
// 已知限制(写进 README):
//   * 不支持"跨调用拼一个标签"(浏览器里可以 document.write("<div>") 之后
//     再 write("</div>")):每次调用都是独立解析,未闭合标签会被自动闭合
//     (实测 "<div><span>内" → <div><span>内</span></div>)。实际页面里罕见;
//   * open() / close() 是 no-op(不清空文档),只保证特性探测代码不炸。
(function () {
  if (typeof globalThis.__qjsdom_安装documentWrite === "function") return; // 已装过

  const 提示 = (msg) => {
    try {
      if (!Array.isArray(globalThis.__qjsdom_页面提示)) globalThis.__qjsdom_页面提示 = [];
      if (globalThis.__qjsdom_页面提示.length < 20) globalThis.__qjsdom_页面提示.push(msg);
    } catch (e) { /* 提示本身不该出错 */ }
  };

  globalThis.__qjsdom_安装documentWrite = function (document) {
    if (!document || document.__qjsdom_write已装) return;
    try { document.__qjsdom_write已装 = true; } catch (e) { /* 只读对象就算了 */ }

    // 写入锚点:第一次 write 时定位到"当前脚本",之后依次跟在已写入内容后面。
    // 锚点只在一次 write 的搬运过程中使用,不留在 DOM 里(不插标记节点)。
    let 锚点 = null;
    let 退回次数 = 0;

    const 定位锚点 = () => {
      if (锚点 && 锚点.parentNode) return 锚点;
      const 当前 = globalThis.__qjsdom_当前脚本;
      if (当前 && 当前.parentNode) {
        锚点 = 当前;
        return 锚点;
      }
      return null;
    };

    const write = function (html) {
      const 文本 = html === undefined || html === null ? "" : String(html);
      try {
        // linkedom 原生解析:丢给 innerHTML,不自己解析
        const 容器 = document.createElement("div");
        容器.innerHTML = 文本;

        const 目标 = 定位锚点();
        if (!目标) {
          // 没有当前脚本 → 追加到 body 末尾(见文件头的语义说明)
          退回次数++;
          if (退回次数 === 1) {
            提示("note: document.write 在脚本之外被调用(页面已解析完),内容追加到 body 末尾;" +
              "浏览器的行为是清空整个文档重写,这里不做");
          }
          const body = document.body || document.documentElement;
          if (!body) return;
          while (容器.firstChild) body.appendChild(容器.firstChild);
          return;
        }

        // 逐个搬到锚点之后。用"插入到同一个参考节点之前"的方式:多次 write
        // 的先后顺序才正确 —— 若每次都 insertAdjacentHTML("afterend"),
        // 第二次写入会跑到第一次前面(顺序颠倒)。
        const 父 = 目标.parentNode;
        const 参考 = 目标.nextSibling;
        let 最后一个 = 目标;
        while (容器.firstChild) {
          const 节点 = 容器.firstChild;
          父.insertBefore(节点, 参考);
          最后一个 = 节点;
        }
        // 锚点推进到本次写入的最后一个节点,下一次 write 跟在它后面
        锚点 = 最后一个;
      } catch (e) {
        提示("note: document.write 失败: " + ((e && e.message) || e));
      }
    };

    try {
      document.write = write;
      document.writeln = function (html) {
        write((html === undefined || html === null ? "" : String(html)) + "\n");
      };
      // open/close 不清空文档:特性探测代码需要它们存在,但清空语义对抓取只有害处
      document.open = function () { return document; };
      document.close = function () { return document; };
    } catch (e) {
      提示("note: document.write 安装失败: " + ((e && e.message) || e));
    }
  };
})();
