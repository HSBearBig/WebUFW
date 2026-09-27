#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/webufw-install-test.XXXXXXXX")"
trap 'rm -rf -- "$test_dir"' EXIT
export WEBUFW_TEST_DIR="$test_dir"

case "$(uname -m)" in
    x86_64|amd64) asset=webufw-linux-amd64 ;;
    aarch64|arm64) asset=webufw-linux-arm64 ;;
    *) echo 'unsupported test architecture' >&2; exit 1 ;;
esac
cat > "$test_dir/$asset" <<'BINARY'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$WEBUFW_TEST_DIR/binary-args"
printf 'preview from release binary\n'
BINARY
chmod 755 "$test_dir/$asset"
printf '%s  %s\n' "$(sha256sum "$test_dir/$asset" | awk '{print $1}')" "$asset" > "$test_dir/SHA256SUMS"
cat > "$test_dir/curl" <<'MOCK_CURL'
#!/usr/bin/env bash
set -euo pipefail
url=''
output=''
while (($#)); do
    case "$1" in
        -o) output="$2"; shift ;;
        https://*) url="$1" ;;
    esac
    shift
done
printf '%s\n' "$url" >> "$WEBUFW_TEST_DIR/curl-urls"
case "$url" in
    */releases/latest)
        [[ "${WEBUFW_NO_RELEASE:-}" != 1 ]] || exit 22
        printf '{\n  "tag_name": "v0.1.0"\n}\n' > "$output"
        ;;
    */SHA256SUMS)
        cp "$WEBUFW_TEST_DIR/SHA256SUMS" "$output"
        if [[ "${WEBUFW_BAD_SHA:-}" == 1 ]]; then sed -i 's/^[0-9a-f]*/0000000000000000000000000000000000000000000000000000000000000000/' "$output"; fi
        ;;
    */webufw-linux-*) cp "$WEBUFW_TEST_DIR/$(basename "$url")" "$output" ;;
    *) exit 22 ;;
esac
MOCK_CURL
cat > "$test_dir/sudo" <<'MOCK_SUDO'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$WEBUFW_TEST_DIR/sudo-args"
[[ "$1" == -- && -x "$2" && "$3" == install ]]
MOCK_SUDO
chmod 755 "$test_dir/curl" "$test_dir/sudo"
export PATH="$test_dir:$PATH"

"$project_dir/install.sh" --help > "$test_dir/help"
grep -q -- '--version' "$test_dir/help"
if "$project_dir/install.sh" --version bad > /dev/null 2>&1; then echo 'invalid version accepted' >&2; exit 1; fi
if "$project_dir/install.sh" --password-file > /dev/null 2>&1; then echo 'missing password file accepted' >&2; exit 1; fi

cat "$project_dir/install.sh" | bash -s -- --dry-run --no-start > "$test_dir/dry-run"
grep -q 'Release：v0.1.0' "$test_dir/dry-run"
grep -q 'preview from release binary' "$test_dir/dry-run"
grep -q '/releases/download/v0.1.0/' "$test_dir/curl-urls"
[[ ! -e "$test_dir/sudo-args" ]]
mapfile -t args < "$test_dir/binary-args"
[[ "${args[*]}" == 'install --dry-run --no-start' ]]

"$project_dir/install.sh" --version v0.2.0 --no-start --password-file "$test_dir/password path" > "$test_dir/install"
grep -q '/releases/download/v0.2.0/' "$test_dir/curl-urls"
mapfile -t args < "$test_dir/sudo-args"
[[ "${#args[@]}" == 6 && "${args[0]}" == -- && "${args[2]}" == install ]]
[[ "${args[3]}" == --no-start && "${args[4]}" == --password-file && "${args[5]}" == "$test_dir/password path" ]]

if WEBUFW_BAD_SHA=1 "$project_dir/install.sh" --dry-run > /dev/null 2>&1; then echo 'wrong SHA256 accepted' >&2; exit 1; fi
if WEBUFW_NO_RELEASE=1 "$project_dir/install.sh" --dry-run > /dev/null 2>&1; then echo 'missing release accepted' >&2; exit 1; fi
echo 'install.sh tests passed'
