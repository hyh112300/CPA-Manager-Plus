#!/usr/bin/env bash
# 构建内嵌 Web 管理面板的 manager-server 二进制。
#
# 用法：
#   bin/release/build-manager-binary.sh                       # 本机平台，产物 dist/cpa-manager-plus
#   GOOS=linux GOARCH=amd64 bin/release/build-manager-binary.sh /tmp/cpa-manager-plus
#
# 说明：internal/httpapi/web/management.html 在仓库里是占位符，
# 直接 `go build` 会打出「管理面板尚未内置」的空壳。本脚本先构建前端，
# 把 dist/index.html 复制进去再编译，结束时自动还原占位符。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WEB_DIR="$ROOT_DIR/apps/web"
SERVER_DIR="$ROOT_DIR/apps/manager-server"
EMBED_DIR="$SERVER_DIR/internal/httpapi/web"
PANEL_FILE="$EMBED_DIR/management.html"

OUTPUT="${1:-$ROOT_DIR/dist/cpa-manager-plus}"
VERSION="${VERSION:-$(cd "$ROOT_DIR" && git describe --tags --always --dirty 2>/dev/null || echo dev)}"
SOURCE_COMMIT="${SOURCE_COMMIT:-$(cd "$ROOT_DIR" && git rev-parse HEAD 2>/dev/null || echo unknown)}"
TARGET_OS="${GOOS:-$(go env GOOS)}"
TARGET_ARCH="${GOARCH:-$(go env GOARCH)}"

LOCAL_GOOS="$(go env GOOS)"
LOCAL_GOARCH="$(go env GOARCH)"
if [[ "$TARGET_OS" != "$LOCAL_GOOS" || "$TARGET_ARCH" != "$LOCAL_GOARCH" ]]; then
  CGO_ENABLED_VALUE=0
else
  CGO_ENABLED_VALUE="${CGO_ENABLED:-0}"
fi

BACKUP_DIR="$(mktemp -d)"
restore_placeholder() {
  if [[ -f "$BACKUP_DIR/management.html" ]]; then
    cp "$BACKUP_DIR/management.html" "$PANEL_FILE"
  fi
  cp "$BACKUP_DIR/favicon.ico" "$EMBED_DIR/favicon.ico" 2>/dev/null || true
  cp "$BACKUP_DIR/apple-touch-icon.png" "$EMBED_DIR/apple-touch-icon.png" 2>/dev/null || true
  rm -rf "$BACKUP_DIR"
}
trap restore_placeholder EXIT

echo "==> 备份仓库内的占位文件"
cp "$PANEL_FILE" "$BACKUP_DIR/management.html"
cp "$EMBED_DIR/favicon.ico" "$BACKUP_DIR/favicon.ico" 2>/dev/null || true
cp "$EMBED_DIR/apple-touch-icon.png" "$BACKUP_DIR/apple-touch-icon.png" 2>/dev/null || true

echo "==> 构建 Web 面板"
( cd "$ROOT_DIR" && VERSION="$VERSION" npm run build )

echo "==> 复制前端产物到 embed 目录"
cp "$WEB_DIR/dist/index.html" "$PANEL_FILE"
cp "$WEB_DIR/dist/favicon.ico" "$EMBED_DIR/favicon.ico"
cp "$WEB_DIR/dist/apple-touch-icon.png" "$EMBED_DIR/apple-touch-icon.png"

echo "==> 编译 $TARGET_OS/$TARGET_ARCH (version=$VERSION)"
mkdir -p "$(dirname "$OUTPUT")"
( cd "$SERVER_DIR" && CGO_ENABLED="$CGO_ENABLED_VALUE" GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" \
  go build -trimpath \
  -ldflags "-s -w \
    -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.Version=$VERSION \
    -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.SourceCommit=$SOURCE_COMMIT" \
  -o "$OUTPUT" ./cmd/cpa-manager-plus )

echo "==> 完成：$OUTPUT"
ls -lh "$OUTPUT"
