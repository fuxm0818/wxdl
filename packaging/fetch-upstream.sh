#!/usr/bin/env bash
# Fetch the pinned upstream engine archives and verify them against the hashes
# recorded in upstream.env. The direct release-download URL is used instead of
# the GitHub API because CI runners share one IP and the API 403s once the
# unauthenticated hour is spent.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$dir/.." && pwd)"
# shellcheck disable=SC1090,SC1091
. "$dir/upstream.env"

out="${1:-$root/packaging/cache}"
mkdir -p "$out"

sha_of() { shasum -a 256 "$1" | awk '{print $1}'; }

fetch_asset() {
	name="$1"
	dest="$out/$name"
	url="https://github.com/${UPSTREAM_REPO}/releases/download/${UPSTREAM_TAG}/${name}"
	if [ ! -s "$dest" ]; then
		# Progress goes to stderr: callers capture the path from stdout.
		echo "  GET $url" >&2
		curl -fsSL --retry 3 --retry-delay 2 -o "$dest" "$url"
	fi
	printf '%s\n' "$dest"
}

sums="$(fetch_asset "${UPSTREAM_ASSET_PREFIX}_${UPSTREAM_TAG}_checksums.txt")"

for arch in arm64 x86_64; do
	asset="${UPSTREAM_ASSET_PREFIX}_${UPSTREAM_TAG}_darwin_${arch}.zip"
	want_var="UPSTREAM_SHA_darwin_${arch}"
	want="${!want_var}"

	# upstream.env and the release's own checksums file must agree before we
	# ship anything; a silent rename upstream shows up here as a hard failure.
	if ! grep -qxF "${want}  ${asset}" "$sums"; then
		echo "ERROR: upstream checksums.txt has no entry matching upstream.env for $arch" >&2
		echo "  looking for: ${want}  ${asset}" >&2
		exit 1
	fi

	dest="$(fetch_asset "$asset")"
	got="$(sha_of "$dest")"
	if [ "$got" != "$want" ]; then
		echo "ERROR: $asset sha256 mismatch" >&2
		echo "  expected $want" >&2
		echo "  actual   $got" >&2
		rm -f "$dest"
		exit 1
	fi
	echo "  OK $asset"
done

echo "Upstream assets ready in $out"
