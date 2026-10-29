#!/bin/sh
# Install the latest SlopCop release on macOS and Linux.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/BrokkAi/slopcop/master/install.sh | sh
#
# Environment:
#   SLOPCOP_INSTALL_DIR      Install directory. Defaults to $HOME/.local/bin.
#   SLOPCOP_VERSION          Optional release tag to install, for example v0.1.0.
#   SLOPCOP_GITHUB_OWNER     GitHub owner to download from. Defaults to BrokkAi.
#   SLOPCOP_GITHUB_API       GitHub API base URL. Defaults to https://api.github.com.
#   SLOPCOP_NO_MODIFY_PATH   Set to leave the shell profile unchanged.
#   GITHUB_TOKEN             Optional token for GitHub API rate limits.
#
# The script reports failures on stderr and exits non-zero.

set -eu

ScriptVersion='1.0.0'
Repository='slopcop'
# An interrupted run can leave a staged copy behind.
StagedPrefix='.slopcop-install-'

step() {
	echo "slopcop-installer: $*" >&2
}

fail() {
	echo "slopcop-installer: $*" >&2
	exit 1
}

owner="${SLOPCOP_GITHUB_OWNER:-BrokkAi}"
api="${SLOPCOP_GITHUB_API:-https://api.github.com}"
api="${api%/}"
version="${SLOPCOP_VERSION:-}"
install_dir="${SLOPCOP_INSTALL_DIR:-$HOME/.local/bin}"
no_modify_path="${SLOPCOP_NO_MODIFY_PATH:-}"
token="${GITHUB_TOKEN:-}"

if [ -n "$version" ]; then
	case "$version" in
		v*) ;;
		*) version="v$version" ;;
	esac
fi

# Detect the target.
uname_s=$(uname -s)
case "$uname_s" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported operating system: $uname_s (the release supports macOS and Linux)" ;;
esac

uname_m=$(uname -m)
case "$uname_m" in
	x86_64|amd64) target=amd64 ;;
	aarch64|arm64) target=arm64 ;;
	*) fail "unsupported architecture: $uname_m (the release supports amd64 and arm64)" ;;
esac

# fetch URL OUTPUT [api]
#
# Downloads with curl or wget. The "api" mode adds the GitHub headers the REST
# API needs, including the token when one is set.
fetch() {
	fetch_url="$1"
	fetch_output="$2"
	fetch_api="${3:-}"

	if command -v curl >/dev/null 2>&1; then
		set -- curl -fsSL
		if [ "$fetch_api" = "api" ]; then
			set -- "$@" -H "User-Agent: slopcop-install-sh/$ScriptVersion" -H "Accept: application/vnd.github+json"
			if [ -n "$token" ]; then
				set -- "$@" -H "Authorization: Bearer $token"
			fi
		fi
		set -- "$@" -o "$fetch_output" "$fetch_url"
		if ! "$@"; then
			fail "cannot download $fetch_url"
		fi
	elif command -v wget >/dev/null 2>&1; then
		set -- wget -q
		if [ "$fetch_api" = "api" ]; then
			set -- "$@" --header="User-Agent: slopcop-install-sh/$ScriptVersion" --header="Accept: application/vnd.github+json"
			if [ -n "$token" ]; then
				set -- "$@" --header="Authorization: Bearer $token"
			fi
		fi
		set -- "$@" -O "$fetch_output" "$fetch_url"
		if ! "$@"; then
			fail "cannot download $fetch_url"
		fi
	else
		fail "curl or wget is required to download"
	fi
}

work=$(mktemp -d "${TMPDIR:-/tmp}/slopcop-install.XXXXXX") || fail "cannot create a temporary directory"
staged=""
cleanup() {
	rm -rf "$work"
	if [ -n "$staged" ]; then
		rm -f "$staged" || true
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ -n "$version" ]; then
	endpoint="$api/repos/$owner/$Repository/releases/tags/$version"
else
	endpoint="$api/repos/$owner/$Repository/releases/latest"
fi

release_json="$work/release.json"
fetch "$endpoint" "$release_json" api

tag=$(grep -o '"tag_name":[[:space:]]*"[^"]*"' "$release_json" | head -n 1 | sed 's/"tag_name":[[:space:]]*"//; s/"$//')
if [ -z "$tag" ]; then
	fail "the release has no tag name"
fi

# The archive name carries the tag, but match loosely so a filename format
# change does not break the installer outright. The trailing quote anchors to
# the end of the URL so a .sig sibling does not match.
archive_url=$(grep -o '"browser_download_url":[[:space:]]*"[^"]*"' "$release_json" | grep -E "slopcop_.*_${os}_${target}\.tar\.gz\"" | head -n 1 | sed 's/"browser_download_url":[[:space:]]*"//; s/"$//')
if [ -z "$archive_url" ]; then
	names=$(grep -o '"name":[[:space:]]*"[^"]*"' "$release_json" | sed 's/"name":[[:space:]]*"//; s/"$//' | tr '\n' ',' | sed 's/,$//')
	fail "release $tag has no ${os}-${target} archive; assets: $names"
fi
expected_name=$(basename "$archive_url")

sums_url=$(grep -o '"browser_download_url":[[:space:]]*"[^"]*"' "$release_json" | grep -F 'checksums.txt' | head -n 1 | sed 's/"browser_download_url":[[:space:]]*"//; s/"$//')
if [ -z "$sums_url" ]; then
	fail "release $tag is missing checksums.txt"
fi

step "downloading $expected_name"
fetch "$archive_url" "$work/$expected_name"
fetch "$sums_url" "$work/checksums.txt"

# checksums.txt lists every release file as "<hash>  <name>"; the name may
# carry a leading * for binary mode.
expected_sum=$(awk -v name="$expected_name" '
	{
		gsub(/^\*/, "", $2)
		if ($2 == name) { print tolower($1); exit }
	}
' "$work/checksums.txt")
if [ -z "$expected_sum" ]; then
	fail "checksums.txt has no entry for $expected_name"
fi
case "$expected_sum" in
	*[!0-9a-f]*) fail "the checksum for $expected_name is malformed" ;;
esac
if [ "${#expected_sum}" -ne 64 ]; then
	fail "the checksum for $expected_name is malformed"
fi

if command -v sha256sum >/dev/null 2>&1; then
	actual_sum=$(sha256sum "$work/$expected_name" | awk '{print tolower($1)}')
elif command -v shasum >/dev/null 2>&1; then
	actual_sum=$(shasum -a 256 "$work/$expected_name" | awk '{print tolower($1)}')
else
	fail "sha256sum or shasum is required to verify the download"
fi
if [ "$expected_sum" != "$actual_sum" ]; then
	fail "checksum mismatch for $expected_name: expected $expected_sum, got $actual_sum"
fi

extracted="$work/extracted"
mkdir -p "$extracted"
tar -xzf "$work/$expected_name" -C "$extracted"

binary_list=$(find "$extracted" -type f -name slopcop)
if [ -z "$binary_list" ]; then
	fail "$expected_name must contain exactly one slopcop"
fi
binary_count=$(printf '%s\n' "$binary_list" | grep -c .)
if [ "$binary_count" -ne 1 ]; then
	fail "$expected_name must contain exactly one slopcop"
fi
binary="$binary_list"

if ! reported=$("$binary" version); then
	fail "the downloaded slopcop failed to run"
fi

mkdir -p "$install_dir" || fail "cannot create $install_dir"
for f in "$install_dir/$StagedPrefix"*; do
	[ -f "$f" ] && rm -f "$f" || true
done

# Stage in the install directory so the final rename stays on one volume.
staged="$install_dir/${StagedPrefix}$$"
cp "$binary" "$staged"
chmod 755 "$staged"
mv "$staged" "$install_dir/slopcop"
staged=""
step "installed $reported to $install_dir"

if [ -n "$no_modify_path" ]; then
	exit 0
fi

modified=0
for rc in "$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.profile"; do
	if [ -f "$rc" ] && grep -qF "$install_dir" "$rc" 2>/dev/null; then
		continue
	fi
	if [ ! -f "$rc" ]; then
		case "$rc" in
			*/.bashrc|*/.zshrc) continue ;;
		esac
	fi
	if ! printf '\n# Added by slopcop installer\nexport PATH="%s:$PATH"\n' "$install_dir" >>"$rc"; then
		fail "cannot write to $rc"
	fi
	modified=1
done
if [ "$modified" -eq 1 ]; then
	step "added $install_dir to your PATH; open a new terminal, then run slopcop"
else
	step "run slopcop to start"
fi
