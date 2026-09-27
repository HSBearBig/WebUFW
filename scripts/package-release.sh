#!/usr/bin/env bash
set -euo pipefail

if (($# != 1)); then
    printf '用法：%s vX.Y.Z\n' "$0" >&2
    exit 1
fi
version="$1"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    printf '版本格式需為 vX.Y.Z\n' >&2
    exit 1
}

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
go_cmd="${GO_BIN:-go}"
command -v "$go_cmd" >/dev/null 2>&1 || {
    printf '找不到 Go；請先安裝 Go 1.26 以上，或設定 GO_BIN\n' >&2
    exit 1
}
command -v sha256sum >/dev/null 2>&1 || {
    printf '找不到 sha256sum\n' >&2
    exit 1
}

out_dir="$project_dir/dist/$version"
if [[ -e "$out_dir" ]]; then
    printf '發版目錄已存在：%s\n' "$out_dir" >&2
    exit 1
fi
mkdir -p "$out_dir"
(
    cd -- "$project_dir"
    "$go_cmd" test ./...
    for arch in amd64 arm64; do
        CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$go_cmd" build -mod=readonly -trimpath \
            -ldflags="-s -w -X webufw/internal/webufw.Version=$version" \
            -o "$out_dir/webufw-linux-$arch" ./cmd/webufw
    done
)
(
    cd -- "$out_dir"
    sha256sum webufw-linux-amd64 webufw-linux-arm64 > SHA256SUMS
)
printf 'Release 資產已建立：%s（%s）\n' "$out_dir" "$version"
