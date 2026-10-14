// canvas 的 stub:linkedom 把它列为可选依赖(用于渲染 canvas 内容到图片),
// 我们只做 HTML 解析,不需要它。esbuild --alias:canvas=./canvas_stub.js 会用它顶上。
module.exports = {};
