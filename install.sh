#!/bin/sh
# Install a released contextctl binary on macOS or Linux.
set -eu

REPOSITORY="rossoctl/context-service"
INSTALL_DIR="${CONTEXTCTL_INSTALL_DIR:-${HOME}/.local/bin}"
VERSION="${CONTEXTCTL_VERSION:-}"
RELEASE_BASE_URL="${CONTEXTCTL_RELEASE_BASE_URL:-https://github.com/${REPOSITORY}/releases/download}"

info() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'USAGE'
Install contextctl on macOS or Linux.

Usage:
  curl -fsSL https://raw.githubusercontent.com/rossoctl/context-service/main/install.sh | sh
  curl -fsSL .../install.sh | sh -s -- [options]

Options:
  --version VERSION  Install a specific release, such as v0.1.0
  --bin-dir PATH     Install directory (default ~/.local/bin)
  -h, --help         Show this help

Environment:
  CONTEXTCTL_VERSION      Release to install
  CONTEXTCTL_INSTALL_DIR  Install directory
USAGE
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--version)
			[ "$#" -ge 2 ] || die "--version requires a value"
			VERSION="$2"
			shift 2
			;;
		--version=*)
			VERSION="${1#*=}"
			shift
			;;
		--bin-dir)
			[ "$#" -ge 2 ] || die "--bin-dir requires a path"
			INSTALL_DIR="$2"
			shift 2
			;;
		--bin-dir=*)
			INSTALL_DIR="${1#*=}"
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "unknown option: $1" ;;
	esac
done

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v install >/dev/null 2>&1 || die "install is required"

case "$(uname -s)" in
	Darwin) os="darwin" ;;
	Linux) os="linux" ;;
	*) die "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
	x86_64 | amd64) arch="amd64" ;;
	arm64 | aarch64) arch="arm64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
	if ! release_json=$(curl -fsSL "https://api.github.com/repos/${REPOSITORY}/releases/latest"); then
		die "could not determine the latest contextctl release"
	fi
	VERSION=$(printf '%s' "$release_json" | tr ',{}' '\n' | awk -F'"' '/^[[:space:]]*"tag_name"[[:space:]]*:/ { print $4; exit }')
fi

case "$VERSION" in
	v[0-9]*) ;;
	*) die "invalid release version: $VERSION" ;;
esac
case "$VERSION" in
	*[!0-9A-Za-z.+-]*) die "invalid release version: $VERSION" ;;
esac

archive_version="${VERSION#v}"
archive="contextctl_${archive_version}_${os}_${arch}.tar.gz"
temporary=$(mktemp -d "${TMPDIR:-/tmp}/contextctl-install.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

info "Downloading contextctl ${VERSION} for ${os}/${arch}..."
curl -fsSL "${RELEASE_BASE_URL}/${VERSION}/${archive}" -o "${temporary}/${archive}"
curl -fsSL "${RELEASE_BASE_URL}/${VERSION}/checksums.txt" -o "${temporary}/checksums.txt"

if ! checksum_line=$(awk -v file="$archive" '$2 == file { print; matches++ } END { if (matches != 1) exit 1 }' "${temporary}/checksums.txt"); then
	die "checksums.txt does not contain exactly one entry for ${archive}"
fi
printf '%s\n' "$checksum_line" > "${temporary}/expected-checksum.txt"

if command -v shasum >/dev/null 2>&1; then
	(cd "$temporary" && shasum -a 256 -c expected-checksum.txt >/dev/null)
elif command -v sha256sum >/dev/null 2>&1; then
	(cd "$temporary" && sha256sum -c expected-checksum.txt >/dev/null)
else
	die "shasum or sha256sum is required to verify the download"
fi

mkdir -p "${temporary}/archive"
tar -xzf "${temporary}/${archive}" -C "${temporary}/archive"
[ -f "${temporary}/archive/contextctl" ] || die "release archive does not contain contextctl"

mkdir -p "$INSTALL_DIR"
install -m 0755 "${temporary}/archive/contextctl" "${INSTALL_DIR}/contextctl"

installed_version=$("${INSTALL_DIR}/contextctl" --version 2>/dev/null || true)
info "Installed ${installed_version:-contextctl} to ${INSTALL_DIR}/contextctl"

case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) ;;
	*)
		info ""
		info "Add contextctl to your PATH:"
		info "  export PATH=\"${INSTALL_DIR}:\$PATH\""
		;;
esac
