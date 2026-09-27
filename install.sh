#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<'HELP'
WebUFW 安裝入口（下載已發布的 Linux 執行檔）

用法：./install.sh [--version vX.Y.Z] [--no-start] [--password-file PATH] [--dry-run]

  --version VERSION    指定 GitHub Release；預設為最新正式版
  --no-start           只安裝檔案，不啟用或啟動服務
  --password-file PATH 首次安裝時使用權限 0600 的密碼檔
  --dry-run            下載並校驗執行檔，預覽安裝內容，不修改系統
  -h, --help           顯示此說明

請以一般使用者執行。腳本只在安裝階段呼叫 sudo；
不會自動安裝 UFW、Docker、ufw-docker 或改動防火牆規則。
HELP
}

fail() {
    printf 'WebUFW 安裝：%s\n' "$*" >&2
    exit 1
}

version=""
args=()
dry_run=false
while (($#)); do
    case "$1" in
        --version)
            (($# >= 2)) || fail '--version 需要版本號'
            version="$2"
            [[ -n "$version" ]] || fail '--version 需要版本號'
            shift
            ;;
        --version=*)
            version="${1#*=}"
            [[ -n "$version" ]] || fail '--version 需要版本號'
            ;;
        --no-start)
            args+=("$1")
            ;;
        --dry-run)
            dry_run=true
            ;;
        --password-file)
            (($# >= 2)) || fail '--password-file 需要檔案路徑'
            args+=("$1" "$2")
            shift
            ;;
        --password-file=*)
            [[ "${1#*=}" != "" ]] || fail '--password-file 需要檔案路徑'
            args+=("$1")
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            fail "不支援的參數：$1（使用 --help 查看用法）"
            ;;
    esac
    shift
done

[[ "$(uname -s)" == Linux ]] || fail '目前只支援 Linux'
(( EUID != 0 )) || fail '請以一般使用者執行 ./install.sh；腳本會在安裝階段呼叫 sudo'
case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "不支援的 CPU 架構：$(uname -m)" ;;
esac
if [[ -n "$version" && ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    fail '版本格式需為 vX.Y.Z，例如 v0.1.0'
fi
for command_name in curl sha256sum mktemp; do
    command -v "$command_name" >/dev/null 2>&1 || fail "找不到 $command_name"
done
if [[ "$dry_run" == false ]]; then
    command -v sudo >/dev/null 2>&1 || fail '找不到 sudo；請先安裝 sudo'
fi

asset="webufw-linux-$arch"
build_dir="$(mktemp -d "${TMPDIR:-/tmp}/webufw-install.XXXXXXXX")" || fail '無法建立暫存目錄'
trap 'rm -rf -- "$build_dir"' EXIT

download() {
    curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 120 "$1" -o "$2"
}

if [[ -z "$version" ]]; then
    metadata_url="https://api.github.com/repos/HSBearBig/WebUFW/releases/latest"
    if ! download "$metadata_url" "$build_dir/release.json"; then
        fail '找不到最新正式版 Release；請確認第一版已發布'
    fi
    mapfile -t release_tags < <(sed -nE 's/^[[:space:]]*"tag_name":[[:space:]]*"([^"]+)".*/\1/p' "$build_dir/release.json")
    if [[ "${#release_tags[@]}" != 1 || ! "${release_tags[0]}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        fail '無法從 GitHub Release metadata 解析有效版本號'
    fi
    version="${release_tags[0]}"
fi
base_url="https://github.com/HSBearBig/WebUFW/releases/download/$version"
printf '正在下載 WebUFW Release：%s\n' "$version"
if ! download "$base_url/SHA256SUMS" "$build_dir/SHA256SUMS"; then
    fail '找不到 Release 校驗檔；請確認該版本已發布'
fi
if ! download "$base_url/$asset" "$build_dir/webufw"; then
    fail "找不到 $asset；請確認該版本已發布"
fi
mapfile -t matches < <(awk -v name="$asset" '$2 == name { print $1 }' "$build_dir/SHA256SUMS")
if [[ "${#matches[@]}" != 1 || ! "${matches[0]}" =~ ^[0-9a-f]{64}$ ]]; then
    fail "Release 校驗檔缺少唯一有效的 $asset SHA256"
fi
actual="$(sha256sum "$build_dir/webufw")"
actual="${actual%% *}"
[[ "$actual" == "${matches[0]}" ]] || fail '下載檔案的 SHA256 不符合 Release 校驗檔'
chmod 700 "$build_dir/webufw"
printf 'SHA256 已確認：%s\n' "$actual"

if [[ "$dry_run" == true ]]; then
    "$build_dir/webufw" install --dry-run "${args[@]}"
else
    printf '校驗完成；現在透過 sudo 安裝並設定 systemd。\n'
    sudo -- "$build_dir/webufw" install "${args[@]}"
fi
