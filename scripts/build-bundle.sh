#!/usr/bin/env bash
# 重建 internal/renderer/assets/linkedom.core.js
#
# 产物 = host_shims.js(纯 JS 宿主 shim)+ linkedom 的 esbuild IIFE bundle
#
# 依赖:bun(或 node + npx)。linkedom 会临时安装到临时目录,不污染仓库。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/internal/renderer/assets/linkedom.core.js"
SHIMS="$ROOT/internal/renderer/js/host_shims.js"
STUB="$ROOT/internal/renderer/js/canvas_stub.js"

if command -v bunx >/dev/null 2>&1; then
  ESBUILD=(bunx --yes esbuild)
elif command -v npx >/dev/null 2>&1; then
  ESBUILD=(npx --yes esbuild)
else
  echo "需要 bun 或 node(npx)来运行 esbuild" >&2
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$TMP"

echo "[1/3] 安装 linkedom ..."
if command -v bun >/dev/null 2>&1; then
  echo '{"name":"bundle-builder","module":"index.js"}' > package.json
  bun add linkedom >/dev/null 2>&1
else
  npm init -y >/dev/null 2>&1
  npm install linkedom >/dev/null 2>&1
fi

echo "[2/3] esbuild 打包 ..."
"${ESBUILD[@]}" node_modules/linkedom/esm/index.js \
  --bundle \
  --format=iife \
  --global-name=Linkedom \
  --target=es2020 \
  --alias:canvas="$STUB" \
  --outfile="$TMP/linkedom.core.js" \
  --log-level=warning

echo "[3/3] 前置 host_shims.js 并写入 ..."
{
  echo "// ==== qjsdom: 生成的 bundle,请勿手工编辑 ===="
  echo "// 由 scripts/build-bundle.sh 产出 = host_shims.js + linkedom(esbuild iife)"
  echo "// linkedom 版本见生成时间:$(date -Iseconds)"
  echo
  cat "$SHIMS"
  echo
  cat "$TMP/linkedom.core.js"
} > "$OUT"

SIZE=$(du -h "$OUT" | cut -f1)
echo
echo "✅ 已生成 $OUT ($SIZE)"
