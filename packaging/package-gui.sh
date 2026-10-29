#!/usr/bin/env bash
# Build the wxdl.app desktop bundle (SwiftUI shell + bundled Go CLI + pinned
# engine) and wrap it into a DMG for every supported macOS architecture.
#
#   packaging/package-gui.sh [version] [arch ...]
#
# Defaults: version from git (or WXDL_VERSION), architectures "arm64 x86_64".
# Artifacts land in dist/: wxdl_<version>_darwin_<arch>.dmg + checksums.txt.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$dir/.." && pwd)"
cd "$root"
# shellcheck disable=SC1090,SC1091
. "$dir/upstream.env"

VERSION="${1:-${WXDL_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}}"
shift || true
if [ "$#" -gt 0 ]; then
	ARCHES="$*"
else
	ARCHES="arm64 x86_64"
fi
PLIST_VERSION="${VERSION#v}"

COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
BUILT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
CACHE="$dir/cache"
BUILD="$root/build/gui"
DIST="$root/dist"
APP_NAME="wxdl.app"

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

echo "== icon"
ICON="$root/build/AppIcon.icns"
if [ ! -f "$ICON" ] || [ "$dir/gui/make-icon.swift" -nt "$ICON" ]; then
	swift "$dir/gui/make-icon.swift" "$ICON"
fi

rm -rf "$BUILD"
mkdir -p "$BUILD" "$DIST"

LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$BUILT"
LDFLAGS="$LDFLAGS -X github.com/fuxm0818/wxdl/internal/upstream.PinnedTag=$UPSTREAM_TAG"

for pair in "arm64:arm64" "amd64:x86_64"; do
	arch="${pair%%:*}"
	uparch="${pair##*:}"
	case " $ARCHES " in
	*" $arch "* | *" $uparch "*) ;;
	*) continue ;;
	esac

	# Go calls the Intel architecture amd64, Apple's toolchain calls it x86_64.
	swift_arch="$uparch"
	name="wxdl_${VERSION}_darwin_${uparch}"
	stage="$BUILD/$name"
	app="$stage/$APP_NAME"
	res="$app/Contents/Resources"

	echo "== build $name"
	mkdir -p "$app/Contents/MacOS" "$res/bin" "$res/share/wxdl/upstream"

	CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" \
		go build -trimpath -ldflags "$LDFLAGS" -o "$res/bin/wxdl" .

	# -parse-as-library is required: a single-file swiftc invocation otherwise
	# treats the sources as a script and rejects @main.
	swiftc -O -whole-module-optimization -parse-as-library -swift-version 5 \
		-target "${swift_arch}-apple-macos12.0" \
		-framework SwiftUI -framework AppKit \
		gui/*.swift -o "$app/Contents/MacOS/wxdl-gui"

	sed "s/__VERSION__/$PLIST_VERSION/g" "$root/gui/Info.plist" >"$app/Contents/Info.plist"
	printf 'APPL????' >"$app/Contents/PkgInfo"
	cp "$ICON" "$res/AppIcon.icns"
	cp "$CACHE/${UPSTREAM_ASSET_PREFIX}_${UPSTREAM_TAG}_darwin_${uparch}.zip" "$res/share/wxdl/upstream/"
	cp "$CACHE/${UPSTREAM_ASSET_PREFIX}_${UPSTREAM_TAG}_checksums.txt" "$res/share/wxdl/upstream/"
	cp LICENSE LICENSE-upstream NOTICE-upstream.md "$res/"
	chmod 0755 "$res/bin/wxdl" "$app/Contents/MacOS/wxdl-gui"

	echo "== sign (ad-hoc)"
	codesign --force --deep --sign - "$app" >/dev/null
	codesign --verify --deep --strict "$app"

	dmgstage="$BUILD/$name-dmg"
	mkdir -p "$dmgstage"
	cp -R "$app" "$dmgstage/"
	ln -s /Applications "$dmgstage/应用程序"
	cat >"$dmgstage/使用说明.txt" <<EOF
wxdl 视频号下载 $VERSION

1. 把 wxdl.app 拖到「应用程序」文件夹。
2. 第一次打开：在「应用程序」里右键 wxdl.app，选「打开」，再点一次「打开」。
   这是没有 Apple 开发者签名的常规拦截，只需要绕过一次；也可以在终端执行：
       xattr -dr com.apple.quarantine /Applications/wxdl.app
3. 打开后粘贴视频号链接，点「开始下载」。首次会要求一次管理员密码，
   用于安装本机根证书并暂时接管系统代理（结束时自动恢复）。

仅供学习研究。wxdl 为 MIT 许可，随附的抓取引擎为 MIT + Commons Clause，
因此 wxdl 及含 wxdl 的服务一律不得出售。
EOF

	echo "== dmg"
	hdiutil create -quiet -volname "wxdl $VERSION" -srcfolder "$dmgstage" \
		-ov -format UDZO "$DIST/$name.dmg"
done

(
	cd "$DIST"
	shasum -a 256 "wxdl_${VERSION}"_darwin_*.dmg >"wxdl_${VERSION}_gui_checksums.txt"
)

echo
echo "Artifacts:"
ls -l "$DIST"/*.dmg "$DIST"/*gui_checksums.txt
