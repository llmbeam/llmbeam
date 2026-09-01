#!/bin/sh

set -eu

repo="${LLMBEAM_REPOSITORY:-shao-hua-li/llmbeam}"
install_dir="${LLMBEAM_INSTALL_DIR:-$HOME/.local/bin}"
api_url="https://api.github.com/repos/${repo}/releases/latest"

fail() {
	printf '%s\n' "llmbeam install: $*" >&2
	exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

[ "$(uname -s)" = "Linux" ] || fail "this installer supports Linux only"

case "$(uname -m)" in
	x86_64|amd64) arch="amd64" ;;
	aarch64|arm64) arch="arm64" ;;
	*) fail "unsupported CPU architecture: $(uname -m)" ;;
esac

tag="$(curl -fsSL --retry 3 -H 'Accept: application/vnd.github+json' "$api_url" |
	sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
[ -n "$tag" ] || fail "could not determine the latest release"

case "$tag" in
	v[0-9]*) ;;
	*) fail "latest release has an invalid tag: $tag" ;;
esac

version=${tag#v}
archive="llmbeam_${version}_linux_${arch}.tar.gz"
base_url="https://github.com/${repo}/releases/download/${tag}"
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/llmbeam-install.XXXXXX")"
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

curl -fL --retry 3 "$base_url/$archive" -o "$temp_dir/$archive" ||
	fail "could not download $archive"
curl -fL --retry 3 "$base_url/checksums.txt" -o "$temp_dir/checksums.txt" ||
	fail "could not download checksums.txt"

expected="$(awk -v target="$archive" '$2 == target || $2 == "*" target { print $1; exit }' "$temp_dir/checksums.txt")"
[ -n "$expected" ] || fail "checksum entry for $archive was not found"

if command -v sha256sum >/dev/null 2>&1; then
	actual="$(sha256sum "$temp_dir/$archive" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
	actual="$(shasum -a 256 "$temp_dir/$archive" | awk '{print $1}')"
else
	fail "sha256sum or shasum is required"
fi
[ "$actual" = "$expected" ] || fail "checksum verification failed"

tar -xzf "$temp_dir/$archive" -C "$temp_dir"
[ -f "$temp_dir/llmbeam" ] || fail "release archive does not contain llmbeam"

mkdir -p "$install_dir"
install -m 0755 "$temp_dir/llmbeam" "$install_dir/llmbeam"

printf 'Installed llmbeam %s to %s/llmbeam\n' "$tag" "$install_dir"
case ":${PATH}:" in
*":${install_dir}:"*) ;;
*) printf 'Add %s to PATH to run llmbeam directly.\n' "$install_dir" ;;
esac
