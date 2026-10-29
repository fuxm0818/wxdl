#!/usr/bin/env bash
# Build release archives for every supported macOS architecture, each carrying
# the pinned upstream engine so the first run on a fresh machine needs no
# GitHub access.
#
#   packaging/package.sh [version]
#
# Artifacts land in dist/: wxdl_<version>_darwin_<arch>.tar.gz + checksums.txt.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$dir/.." && pwd)"
cd "$root"
# shellcheck disable=SC1090,SC1091
. "$dir/upstream.env"

VERSION="${1:-${WXDL_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
BUILT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
CACHE="$dir/cache"
BUILD="$root/build"
DIST="$root/dist"

echo "== test"
sources="$(find . -name '*.go' -not -path './build/*' -not -path './dist/*')"
unformatted="$(gofmt -l $sources)"
if [ -n "$unformatted" ]; then
	echo "gofmt needed on:" >&2
	echo "$unformatted" >&2
	exit 1
fi
go vet ./...
go test ./...

echo "== fetch upstream $UPSTREAM_TAG"
"$dir/fetch-upstream.sh" "$CACHE"

rm -rf "$BUILD"
mkdir -p "$BUILD" "$DIST"

LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$BUILT"
LDFLAGS="$LDFLAGS -X github.com/fuxm0818/wxdl/internal/upstream.PinnedTag=$UPSTREAM_TAG"

# macOS tar writes AppleDouble sidecars unless COPYFILE_DISABLE is set; those
# clutter the extracted folder and make checksums unstable.
export COPYFILE_DISABLE=1

for pair in "arm64:arm64" "amd64:x86_64"; do
	goarch="${pair%%:*}"
	uparch="${pair##*:}"
	name="wxdl_${VERSION}_darwin_${uparch}"
	stage="$BUILD/$name"

	echo "== build $name"
	CGO_ENABLED=0 GOOS=darwin GOARCH="$goarch" \
		go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/wxdl" .

	mkdir -p "$stage/share/wxdl/upstream"
	cp "$CACHE/${UPSTREAM_ASSET_PREFIX}_${UPSTREAM_TAG}_darwin_${uparch}.zip" "$stage/share/wxdl/upstream/"
	cp "$CACHE/${UPSTREAM_ASSET_PREFIX}_${UPSTREAM_TAG}_checksums.txt" "$stage/share/wxdl/upstream/"
	cp install.sh LICENSE LICENSE-upstream NOTICE-upstream.md README.md "$stage/"
	chmod 0755 "$stage/install.sh" "$stage/bin/wxdl"

	# The zip and checksums file must survive the round trip byte-for-byte, or
	# wxdl's own bundled verification would reject the package on the user's Mac.
	(
		cd "$stage"
		shasum -a 256 bin/wxdl install.sh \
			share/wxdl/upstream/*.zip share/wxdl/upstream/*.txt \
			LICENSE LICENSE-upstream NOTICE-upstream.md README.md >checksums.txt
	)

	tar -czf "$DIST/$name.tar.gz" -C "$BUILD" "$name"
done

(
	cd "$DIST"
	shasum -a 256 "wxdl_${VERSION}"_darwin_*.tar.gz >"wxdl_${VERSION}_checksums.txt"
)

echo
echo "Artifacts:"
ls -l "$DIST"
