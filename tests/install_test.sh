#!/usr/bin/env bash
set -euo pipefail

(( EUID != 0 )) || { echo 'Run installer tests as a non-root user.' >&2; exit 1; }

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/webufw-install-test.XXXXXXXX")"
trap 'rm -rf -- "$test_dir"' EXIT
export WEBUFW_TEST_DIR="$test_dir"

case "$(uname -m)" in
    x86_64|amd64) asset=webufw-linux-amd64 ;;
    aarch64|arm64) asset=webufw-linux-arm64 ;;
    *) echo 'unsupported test architecture' >&2; exit 1 ;;
esac
# Installation must never run the release binary as an installer.
cat > "$test_dir/$asset" <<'BINARY'
#!/usr/bin/env bash
echo 'release binary was executed during installation' >&2
exit 99
BINARY
chmod 755 "$test_dir/$asset"
printf '%s  %s\n' "$(sha256sum "$test_dir/$asset" | awk '{print $1}')" "$asset" > "$test_dir/SHA256SUMS"
cat > "$test_dir/curl" <<'MOCK_CURL'
#!/usr/bin/env bash
set -euo pipefail
url=''
output=''
max_time=''
retry=''
connect_timeout=''
speed_limit=''
speed_time=''
progress=false
silent=false
while (($#)); do
    case "$1" in
        -o) output="$2"; shift ;;
        --max-time) max_time="$2"; shift ;;
        --retry) retry="$2"; shift ;;
        --connect-timeout) connect_timeout="$2"; shift ;;
        --speed-limit) speed_limit="$2"; shift ;;
        --speed-time) speed_time="$2"; shift ;;
        --progress-bar) progress=true ;;
        --silent) silent=true ;;
        https://*) url="$1" ;;
    esac
    shift
done
[[ "$retry" == 3 && "$connect_timeout" == 15 ]]
printf '%s\n' "$url" >> "$WEBUFW_TEST_DIR/curl-urls"
case "$url" in
    */releases/latest)
        [[ "$max_time" == 120 && "$silent" == true ]]
        if [[ "${WEBUFW_NO_RELEASE:-}" == 1 ]]; then printf '404'; exit 22; fi
        printf '{\n  "tag_name": "v0.1.0"\n}\n' > "$output"
        ;;
    */SHA256SUMS)
        [[ "$max_time" == 120 && "$silent" == true ]]
        cp "$WEBUFW_TEST_DIR/SHA256SUMS" "$output"
        if [[ "${WEBUFW_BAD_SHA:-}" == 1 ]]; then sed -i 's/^[0-9a-f]*/0000000000000000000000000000000000000000000000000000000000000000/' "$output"; fi
        ;;
    */webufw-linux-*)
        [[ "$max_time" == 0 && "$progress" == true && "$silent" == false ]]
        [[ "$speed_limit" == 1 && "$speed_time" == 60 ]]
        if [[ -n "${WEBUFW_DOWNLOAD_ERROR:-}" ]]; then
            printf 'partial download' > "$output"
            printf '%s' "${WEBUFW_HTTP_STATUS:-000}"
            exit "$WEBUFW_DOWNLOAD_ERROR"
        fi
        cp "$WEBUFW_TEST_DIR/$(basename "$url")" "$output"
        printf '######## 100.0%%\n' >&2
        ;;
    *) exit 22 ;;
esac
printf '200'
MOCK_CURL
cat > "$test_dir/sudo" <<'MOCK_SUDO'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$WEBUFW_TEST_DIR/sudo-args"
[[ "$1" == -- && "$2" == bash && -f "$3" && -d "$4" ]]
cp "$3" "$WEBUFW_TEST_DIR/install-system.sh"
cp "$4/webufw.service" "$WEBUFW_TEST_DIR/webufw.service"
# Exercise the generated installation script against a disposable filesystem.
python3 - "$3" "$WEBUFW_TEST_DIR/install-sandbox.sh" <<'PY'
import os,sys
from pathlib import Path
s=Path(sys.argv[1]).read_text()
s=s.replace('export PATH=/usr/sbin:/usr/bin:/sbin:/bin', ':')
for prefix in ['/usr/local/', '/etc/', '/var/lib/']:
    s=s.replace(prefix, os.environ['WEBUFW_TEST_DIR']+'/root'+prefix)
s=s.replace('-o root -g root', '')
Path(sys.argv[2]).write_text(s)
PY
bash "$WEBUFW_TEST_DIR/install-sandbox.sh" "$4" "$5"
MOCK_SUDO
cat > "$test_dir/systemctl" <<'MOCK_SYSTEMCTL'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$WEBUFW_TEST_DIR/systemctl-calls"
case "$1" in
    show) printf '0123456789abcdef0123456789abcdef\n' ;;
    is-active) [[ "${WEBUFW_FAIL_START:-}" != 1 && "${WEBUFW_INACTIVE:-}" != 1 ]] ;;
    restart) [[ "${WEBUFW_LEGACY:-}" != 1 || -f "$WEBUFW_TEST_DIR/root/usr/local/bin/webufw" ]] ;;
esac
MOCK_SYSTEMCTL
cat > "$test_dir/journalctl" <<'MOCK_JOURNALCTL'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$WEBUFW_TEST_DIR/journal-calls"
if [[ "${WEBUFW_FAIL_START:-}" == 1 ]]; then
    printf 'bind: address already in use\n'
else
    [[ "${WEBUFW_UPGRADE:-}" == 1 ]] || printf 'WebUFW 初始密碼：admin / example-initial-password\n'
    printf 'WebUFW ready: v0.1.0；監聽 127.0.0.1:8088\n'
fi
MOCK_JOURNALCTL
cat > "$test_dir/getent" <<'MOCK_GETENT'
#!/usr/bin/env bash
exit 0
MOCK_GETENT
cat > "$test_dir/id" <<'MOCK_ID'
#!/usr/bin/env bash
printf '997\n'
MOCK_ID
chmod 755 "$test_dir/curl" "$test_dir/sudo" "$test_dir/systemctl" "$test_dir/journalctl" "$test_dir/getent" "$test_dir/id"
export PATH="$test_dir:$PATH"
mkdir -p "$test_dir/root/etc/systemd/system" "$test_dir/root/usr/local/bin" "$test_dir/root/etc/webufw" "$test_dir/root/var/lib/webufw"
printf 'old CLI\n' > "$test_dir/root/usr/local/bin/webufw"
printf 'existing config\n' > "$test_dir/root/etc/webufw/config.json"
printf 'existing pending\n' > "$test_dir/root/var/lib/webufw/pending.json"

"$project_dir/install.sh" --help > "$test_dir/help"
grep -q -- '--version' "$test_dir/help"
if "$project_dir/install.sh" --version bad > /dev/null 2>&1; then echo 'invalid version accepted' >&2; exit 1; fi
if "$project_dir/install.sh" --password-file > /dev/null 2>&1; then echo 'removed password option accepted' >&2; exit 1; fi

cat "$project_dir/install.sh" | bash -s -- --dry-run --no-start > "$test_dir/dry-run" 2> "$test_dir/progress"
grep -q 'Release：v0.1.0' "$test_dir/dry-run"
grep -q '/usr/local/libexec/webufw/webufw' "$test_dir/dry-run"
grep -q '/releases/download/v0.1.0/' "$test_dir/curl-urls"
grep -q '100.0%' "$test_dir/progress"
[[ ! -e "$test_dir/sudo-args" ]]

# Failed downloads must stop before sudo, and preserve the real failure reason.
if WEBUFW_DOWNLOAD_ERROR=28 "$project_dir/install.sh" > "$test_dir/timeout" 2>&1; then echo 'timeout accepted' >&2; exit 1; fi
grep -q '下載逾時或傳輸停滯（curl 28）' "$test_dir/timeout"
if grep -q '找不到' "$test_dir/timeout"; then echo 'timeout reported as missing release' >&2; exit 1; fi
if WEBUFW_DOWNLOAD_ERROR=22 WEBUFW_HTTP_STATUS=404 "$project_dir/install.sh" > "$test_dir/not-found" 2>&1; then echo '404 accepted' >&2; exit 1; fi
grep -q "找不到 $asset（HTTP 404）" "$test_dir/not-found"
if WEBUFW_DOWNLOAD_ERROR=22 WEBUFW_HTTP_STATUS=503 "$project_dir/install.sh" > "$test_dir/server-error" 2>&1; then echo '503 accepted' >&2; exit 1; fi
grep -q '下載失敗（HTTP 503）' "$test_dir/server-error"
if WEBUFW_DOWNLOAD_ERROR=6 "$project_dir/install.sh" > "$test_dir/dns-error" 2>&1; then echo 'DNS failure accepted' >&2; exit 1; fi
grep -q 'curl 6' "$test_dir/dns-error"
[[ ! -e "$test_dir/sudo-args" ]]

if "$project_dir/install.sh" --no-start > /dev/null 2>&1; then echo 'removed the executable of an active legacy service' >&2; exit 1; fi
[[ -e "$test_dir/root/usr/local/bin/webufw" ]]
cat "$project_dir/install.sh" | WEBUFW_INACTIVE=1 bash -s -- --version v0.2.0 --no-start > "$test_dir/install"
grep -q '/releases/download/v0.2.0/' "$test_dir/curl-urls"
mapfile -t args < "$test_dir/sudo-args"
[[ "${#args[@]}" == 5 && "${args[0]}" == -- && "${args[1]}" == bash && "${args[4]}" == true ]]
grep -q '^ExecStart=/usr/local/libexec/webufw/webufw$' "$test_dir/webufw.service"
[[ ! -e "$test_dir/root/usr/local/bin/webufw" ]]
[[ -x "$test_dir/root/usr/local/libexec/webufw/webufw" ]]
[[ "$(cat "$test_dir/root/etc/webufw/config.json")" == 'existing config' ]]
[[ "$(cat "$test_dir/root/var/lib/webufw/pending.json")" == 'existing pending' ]]
if grep -Eq '^(enable|restart) ' "$test_dir/systemctl-calls"; then echo '--no-start started service' >&2; exit 1; fi

"$project_dir/install.sh" > "$test_dir/started"
grep -q 'example-initial-password' "$test_dir/started"
grep -q '^restart webufw.service$' "$test_dir/systemctl-calls"
grep -q '_SYSTEMD_INVOCATION_ID=0123456789abcdef0123456789abcdef' "$test_dir/journal-calls"
printf 'old CLI\n' > "$test_dir/root/usr/local/bin/webufw"
WEBUFW_LEGACY=1 WEBUFW_UPGRADE=1 "$project_dir/install.sh" > "$test_dir/upgrade"
[[ ! -e "$test_dir/root/usr/local/bin/webufw" ]]
grep -q '沿用既有管理者密碼' "$test_dir/upgrade"
if grep -q 'example-initial-password' "$test_dir/upgrade"; then echo 'old initial password printed during upgrade' >&2; exit 1; fi
if WEBUFW_FAIL_START=1 "$project_dir/install.sh" > "$test_dir/start-failed" 2>&1; then echo 'failed startup reported as success' >&2; exit 1; fi
grep -q 'address already in use' "$test_dir/start-failed"

if WEBUFW_BAD_SHA=1 "$project_dir/install.sh" --dry-run > /dev/null 2>&1; then echo 'wrong SHA256 accepted' >&2; exit 1; fi
if WEBUFW_NO_RELEASE=1 "$project_dir/install.sh" --dry-run > /dev/null 2>&1; then echo 'missing release accepted' >&2; exit 1; fi
echo 'install.sh tests passed'
