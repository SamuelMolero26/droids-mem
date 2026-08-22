#!/bin/sh
# droids-mem installer.
#
#   curl -fsSL https://raw.githubusercontent.com/SamuelMolero26/droids-mem/main/install.sh | sh
#
# Downloads the release binary for this platform, verifies it against the
# published .sha256, and installs it. Every release asset is also covered by a
# SLSA provenance attestation:
#
#   gh attestation verify <binary> --repo SamuelMolero26/droids-mem
#
# Environment:
#   DROIDS_MEM_VERSION   tag to install (default: latest)
#   DROIDS_MEM_PREFIX    install directory (default: /usr/local/bin, else ~/.local/bin)

set -eu

REPO="SamuelMolero26/droids-mem"
VERSION="${DROIDS_MEM_VERSION:-latest}"

die() { printf 'install.sh: %s\n' "$1" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

need curl
need uname
need mktemp

case "$(uname -s)" in
	Linux)  os=linux ;;
	Darwin) os=darwin ;;
	*)      die "unsupported OS: $(uname -s) (linux and darwin only)" ;;
esac

case "$(uname -m)" in
	x86_64 | amd64)  arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)               die "unsupported architecture: $(uname -m) (amd64 and arm64 only)" ;;
esac

# sha256sum on Linux, shasum on macOS. Refuse to continue without one rather
# than installing an unverified binary.
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "no sha256 tool found (need sha256sum or shasum)"
fi

# The /releases/latest URL redirects to /releases/tag/<TAG>; the final path
# segment is the tag. Avoids depending on the API (rate-limited unauthenticated).
if [ "$VERSION" = latest ]; then
	resolved=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
		"https://github.com/$REPO/releases/latest") ||
		die "could not resolve the latest release"
	VERSION="${resolved##*/}"
fi
case "$VERSION" in
	v*) ;;
	*)  die "unexpected version '$VERSION' (want a v-prefixed tag)" ;;
esac

asset="droids-mem-$VERSION-$os-$arch"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'droids-mem %s (%s/%s)\n' "$VERSION" "$os" "$arch"

curl -fsSL -o "$tmp/$asset" "$base/$asset" ||
	die "download failed: $base/$asset"
curl -fsSL -o "$tmp/$asset.sha256" "$base/$asset.sha256" ||
	die "checksum download failed: $base/$asset.sha256"

want=$(cut -d' ' -f1 <"$tmp/$asset.sha256")
got=$(sha256 "$tmp/$asset")
[ -n "$want" ] || die "published checksum is empty"
[ "$want" = "$got" ] || die "checksum mismatch: expected $want, got $got"

chmod +x "$tmp/$asset"
"$tmp/$asset" --version >/dev/null 2>&1 ||
	die "the downloaded binary does not run on this machine"

if [ -n "${DROIDS_MEM_PREFIX:-}" ]; then
	prefix="$DROIDS_MEM_PREFIX"
	mkdir -p "$prefix" || die "cannot create $prefix"
elif [ -w /usr/local/bin ]; then
	prefix=/usr/local/bin
else
	prefix="$HOME/.local/bin"
	mkdir -p "$prefix" || die "cannot create $prefix"
fi

mv "$tmp/$asset" "$prefix/droids-mem" ||
	die "cannot install to $prefix (set DROIDS_MEM_PREFIX to choose another directory)"

printf 'installed %s/droids-mem\n' "$prefix"

# shellcheck disable=SC2016 # $PATH in the printf format is literal: it is printed for the user to copy, not expanded.
case ":$PATH:" in
	*":$prefix:"*) ;;
	*) printf '\nwarning: %s is not on your PATH. Add it with:\n  export PATH="%s:$PATH"\n' "$prefix" "$prefix" ;;
esac

printf '\nnext: droids-mem install --all\n'
