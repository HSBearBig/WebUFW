#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<'HELP'
WebUFW 安裝入口（下載已發布的 Linux 執行檔）

用法：./install.sh [--version vX.Y.Z] [--no-start] [--dry-run]

  --version VERSION    指定 GitHub Release；預設為最新正式版
  --no-start           只安裝檔案，不啟用或啟動服務
  --dry-run            下載並校驗執行檔，預覽安裝內容，不修改系統
  -h, --help           顯示此說明

一般使用者執行時，腳本只在安裝階段呼叫 sudo；
不會自動安裝 UFW、Docker、ufw-docker 或改動防火牆規則。
HELP
}

fail() {
    printf 'WebUFW 安裝：%s\n' "$*" >&2
    exit 1
}

version=""
no_start=false
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
            no_start=true
            ;;
        --dry-run)
            dry_run=true
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
if [[ "$dry_run" == false ]] && (( EUID != 0 )); then
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

# All installation work lives here. The downloaded binary is only a service.
install_system() {
    set -euo pipefail
    export PATH=/usr/sbin:/usr/bin:/sbin:/bin
    stage="$1"
    no_start="$2"
    for command_name in systemctl journalctl install getent useradd; do
        command -v "$command_name" >/dev/null 2>&1 || { printf '找不到 %s\n' "$command_name" >&2; return 1; }
    done
    if [[ "$no_start" == true && -e /usr/local/bin/webufw ]] && systemctl is-active --quiet webufw.service; then
        printf '舊版服務仍在執行；請先 sudo systemctl stop webufw 再使用 --no-start，或使用預設安裝流程自動重啟。\n' >&2
        return 1
    fi
    if getent passwd webufw >/dev/null; then
        [[ "$(id -u webufw)" != 0 && "$(id -g webufw)" != 0 ]] || { printf 'webufw 服務帳號不可為 root\n' >&2; return 1; }
    else
        useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin webufw
    fi
    install -d -o root -g root -m 0700 /etc/webufw /var/lib/webufw
    install -d -o root -g root -m 0755 /usr/local/libexec/webufw
    # Rename on the same filesystem so an upgrade never truncates a running binary.
    binary_tmp="$(mktemp /usr/local/libexec/webufw/.webufw.XXXXXXXX)"
    unit_tmp="$(mktemp /etc/systemd/system/.webufw.XXXXXXXX)"
    trap 'rm -f -- "$binary_tmp" "$unit_tmp"' EXIT
    install -o root -g root -m 0755 "$stage/webufw" "$binary_tmp"
    install -o root -g root -m 0644 "$stage/webufw.service" "$unit_tmp"
    for target in /usr/local/libexec/webufw/webufw /etc/systemd/system/webufw.service /usr/local/bin/webufw; do
        if [[ -e "$target" || -L "$target" ]]; then
            [[ -f "$target" && ! -L "$target" ]] || { printf '拒絕取代非一般檔案：%s\n' "$target" >&2; return 1; }
        fi
    done
    mv -f -- "$binary_tmp" /usr/local/libexec/webufw/webufw
    mv -f -- "$unit_tmp" /etc/systemd/system/webufw.service
    systemctl daemon-reload
    if [[ "$no_start" == true ]]; then
        rm -f -- /usr/local/bin/webufw
        printf 'WebUFW 已安裝；本次未啟用或啟動服務。\n啟動：sudo systemctl enable --now webufw\n初始密碼將在首次啟動時寫入服務日誌。\n'
        return
    fi
    systemctl enable webufw.service
    systemctl restart webufw.service
    invocation="$(systemctl show webufw.service -p InvocationID --value)"
    [[ "$invocation" =~ ^[0-9a-f]{32}$ ]] || { printf '無法取得服務啟動識別碼\n' >&2; return 1; }
    # Wait for the HTTP socket to bind, not merely for systemd to launch a PID.
    ready=false
    for ((attempt=0; attempt<120; attempt++)); do
        logs="$(journalctl -u webufw.service "_SYSTEMD_INVOCATION_ID=$invocation" --no-pager -o cat)"
        if [[ "$logs" == *'WebUFW ready:'* ]] && systemctl is-active --quiet webufw.service; then
            ready=true
            break
        fi
        current="$(systemctl show webufw.service -p InvocationID --value)"
        [[ "$current" == "$invocation" ]] && systemctl is-active --quiet webufw.service || break
        sleep 1
    done
    if [[ "$ready" != true ]]; then
        printf '%s\n' "$logs" >&2
        printf 'WebUFW 尚未成功啟動；請執行 sudo journalctl -u webufw -n 50 --no-pager 檢查。\n' >&2
        return 1
    fi
    # The old agent may need its executable to recover pending changes during
    # shutdown. Remove its public entry point only after the restart succeeds.
    rm -f -- /usr/local/bin/webufw
    printf '\nWebUFW 已安裝並啟用 systemd 服務。\n%s\n' "$logs"
    if [[ "$logs" != *'WebUFW 初始密碼：'* ]]; then
        printf '沿用既有管理者密碼；重新安裝不會重設密碼。\n'
    fi
    printf '預設網址：http://127.0.0.1:8088（僅本機；升級保留既有監聽設定）\n服務狀態：sudo systemctl status webufw --no-pager -l\n查閱初始密碼：sudo journalctl -u webufw --no-pager --grep="WebUFW 初始密碼"\n安裝未修改任何 UFW 規則。\n'
}

cat > "$build_dir/webufw.service" <<'UNIT'
[Unit]
Description=WebUFW firewall management
After=network.target ufw.service docker.service

[Service]
Type=simple
ExecStart=/usr/local/libexec/webufw/webufw
Restart=on-failure
RestartSec=3
KillMode=mixed
TimeoutStopSec=180
UMask=0077
RuntimeDirectory=webufw
RuntimeDirectoryMode=0750
StateDirectory=webufw
StateDirectoryMode=0700
Environment=GOMEMLIMIT=24MiB
Environment=GOGC=80
StandardOutput=journal
StandardError=journal
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=-/etc/ufw -/etc/default/ufw /etc/webufw
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK

[Install]
WantedBy=multi-user.target
UNIT

if [[ "$dry_run" == true ]]; then
    printf '將安裝 /usr/local/libexec/webufw/webufw、webufw 服務帳號與 webufw.service；移除舊版 /usr/local/bin/webufw 指令。\n'
    if [[ "$no_start" == true ]]; then
        printf '本次不啟用或啟動服務。\n'
    else
        printf '安裝後啟用並啟動服務；初始密碼由服務產生並寫入日誌，既有密碼保持不變。\n'
    fi
    printf '不會安裝 UFW、Docker 或 ufw-docker，也不會修改防火牆規則。\n'
else
    # Use a file rather than bash -s: stdin may still be the curl pipeline.
    { declare -f install_system; printf 'install_system "$@"\n'; } > "$build_dir/install-system.sh"
    printf '校驗完成；現在安裝並設定 systemd。\n'
    if (( EUID == 0 )); then
        bash "$build_dir/install-system.sh" "$build_dir" "$no_start"
    else
        sudo -- bash "$build_dir/install-system.sh" "$build_dir" "$no_start"
    fi
fi
