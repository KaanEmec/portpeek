#!/bin/sh
# Port Peek installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/kaanemec/portpeek/main/install.sh | sh
#
# Downloads a release archive from GitHub, verifies it against the release's
# checksums.txt (SHA-256), and installs the portpeek binary. It never uses sudo.
#
# Environment:
#   PORTPEEK_VERSION      release tag to install, e.g. v1.2.2 (default: latest)
#   PORTPEEK_INSTALL_DIR  where to put the binary (default: /usr/local/bin when
#                         writable, otherwise $HOME/.local/bin)

set -eu

REPO="kaanemec/portpeek"
RELEASES="https://github.com/$REPO/releases"
API_LATEST="https://api.github.com/repos/$REPO/releases/latest"

say() {
	printf 'portpeek-install: %s\n' "$*"
}

die() {
	printf 'portpeek-install: error: %s\n' "$*" >&2
	exit 1
}

other_platforms() {
	cat >&2 <<EOF
portpeek-install: this installer supports macOS and Linux only (found: $1).

On Windows, download portpeek_<version>_windows_amd64.zip from
  $RELEASES/latest
unpack it and put portpeek.exe on your PATH.

With Go installed, on any platform:
  go install github.com/$REPO/cmd/portpeek@latest
EOF
	exit 1
}

has() {
	command -v "$1" >/dev/null 2>&1
}

# fetch URL DEST downloads URL to DEST ("-" for standard output).
fetch() {
	if has curl; then
		curl -fsSL "$1" -o "$2"
	elif has wget; then
		wget -qO "$2" "$1"
	else
		die "neither curl nor wget is installed"
	fi
}

detect_os() {
	os=$(uname -s)
	case "$os" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	*) other_platforms "$os" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64)
		# A shell running under Rosetta reports x86_64 on Apple silicon.
		if [ "$1" = darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
			echo arm64
		else
			echo amd64
		fi
		;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported architecture $arch; try: go install github.com/$REPO/cmd/portpeek@latest" ;;
	esac
}

latest_tag() {
	tag=$(fetch "$API_LATEST" - |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
		head -n 1) || true
	[ -n "$tag" ] || die "could not read the latest release from $API_LATEST; set PORTPEEK_VERSION (for example PORTPEEK_VERSION=v1.2.2) and retry"
	echo "$tag"
}

sha256() {
	if has sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif has shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "neither sha256sum nor shasum is installed, so the download cannot be verified"
	fi
}

pick_dir() {
	if [ -n "${PORTPEEK_INSTALL_DIR:-}" ]; then
		echo "$PORTPEEK_INSTALL_DIR"
	elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		echo /usr/local/bin
	else
		echo "$HOME/.local/bin"
	fi
}

main() {
	os=$(detect_os)
	arch=$(detect_arch "$os")
	has sha256sum || has shasum ||
		die "neither sha256sum nor shasum is installed, so a download cannot be verified"

	if [ -n "${PORTPEEK_VERSION:-}" ]; then
		tag="v${PORTPEEK_VERSION#v}"
	else
		tag=$(latest_tag)
	fi
	version=${tag#v}
	archive="portpeek_${version}_${os}_${arch}.tar.gz"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t portpeek)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	say "downloading $archive ($tag)"
	fetch "$RELEASES/download/$tag/$archive" "$tmp/$archive" ||
		die "download failed: $RELEASES/download/$tag/$archive"
	fetch "$RELEASES/download/$tag/checksums.txt" "$tmp/checksums.txt" ||
		die "download failed: $RELEASES/download/$tag/checksums.txt"

	want=$(awk -v f="$archive" '$2 == f || $2 == "*" f {print $1; exit}' "$tmp/checksums.txt")
	[ -n "$want" ] || die "$archive is not listed in checksums.txt for $tag"
	got=$(sha256 "$tmp/$archive")
	if [ "$got" != "$want" ]; then
		die "checksum mismatch for $archive (expected $want, got $got); nothing was installed"
	fi
	say "checksum verified (sha256 $got)"

	tar -xzf "$tmp/$archive" -C "$tmp" portpeek || die "could not unpack $archive"
	[ -f "$tmp/portpeek" ] || die "$archive does not contain a portpeek binary"

	dir=$(pick_dir)
	mkdir -p "$dir" 2>/dev/null || true
	if [ ! -d "$dir" ] || [ ! -w "$dir" ]; then
		die "cannot write to $dir; re-run with sudo (curl -fsSL ... | sudo sh) or choose another directory with PORTPEEK_INSTALL_DIR=<dir>"
	fi
	cp "$tmp/portpeek" "$dir/.portpeek.$$"
	chmod 755 "$dir/.portpeek.$$"
	mv -f "$dir/.portpeek.$$" "$dir/portpeek"

	say "installed $dir/portpeek"
	"$dir/portpeek" --version

	case ":$PATH:" in
	*":$dir:"*)
		found=$(command -v portpeek 2>/dev/null || true)
		if [ -n "$found" ] && [ "$found" != "$dir/portpeek" ]; then
			say "note: $found comes first on your PATH and will run instead"
		fi
		;;
	*)
		say "$dir is not on your PATH; add it, for example:"
		say "  export PATH=\"$dir:\$PATH\""
		;;
	esac
}

main "$@"
