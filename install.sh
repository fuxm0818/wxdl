#!/usr/bin/env bash
# Install wxdl from a release archive.
#
#   sh install.sh
#
# Copies the command to ~/.wxdl/bin, the pinned engine package to ~/.wxdl/vendor
# (so the first download works without GitHub access), links /usr/local/bin/wxdl
# and creates the desktop double-click entry.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
home="${HOME:?}"

say() { printf '%s\n' "$*"; }

if [ ! -x "$here/bin/wxdl" ] && [ ! -f "$here/bin/wxdl" ]; then
	say "找不到 $here/bin/wxdl —— 请先解压发行包再运行本脚本。" >&2
	exit 1
fi

if [ -f "$here/checksums.txt" ] && command -v shasum >/dev/null 2>&1; then
	say "校验文件完整性 ..."
	( cd "$here" && shasum -a 256 -c checksums.txt --quiet )
fi

mac_arch="$(uname -m)"
case "$mac_arch" in
arm64) want=arm64 ;;
x86_64) want=x86_64 ;;
*)
	say "本机架构 $mac_arch 不是 macOS 受支持架构，继续安装可能无法运行。" >&2
	want=any
	;;
esac
if [ "$want" != "any" ] && ! ls "$here"/share/wxdl/upstream/*"_darwin_${want}.zip" >/dev/null 2>&1; then
	say "警告：这个发行包不含 darwin_${want} 的引擎包，第一次下载会联网获取。" >&2
fi

say "安装到 $home/.wxdl ..."
mkdir -p "$home/.wxdl/bin" "$home/.wxdl/vendor"
install -m 0755 "$here/bin/wxdl" "$home/.wxdl/bin/wxdl"
for f in "$here"/share/wxdl/upstream/*; do
	[ -e "$f" ] || continue
	install -m 0644 "$f" "$home/.wxdl/vendor/$(basename "$f")"
done
# A copy downloaded by a browser carries the quarantine flag, which makes the
# engine fail to launch with an unhelpful Gatekeeper error.
xattr -d com.apple.quarantine "$home/.wxdl/bin/wxdl" 2>/dev/null || true

wxdl="$home/.wxdl/bin/wxdl"

link_dir="/usr/local/bin"
if [ ! -d "$link_dir" ]; then
	mkdir -p "$link_dir" 2>/dev/null || true
fi
if [ -w "$link_dir" ]; then
	ln -sf "$wxdl" "$link_dir/wxdl"
	say "已创建 $link_dir/wxdl"
else
	say "需要一次管理员授权来创建 $link_dir/wxdl 软链。"
	if sudo -p '' ln -sf "$wxdl" "$link_dir/wxdl" >/dev/null 2>&1; then
		say "已创建 $link_dir/wxdl"
	else
		say "跳过软链。请把这行加进 ~/.zshrc 后重开终端："
		say "  export PATH=\"\$HOME/.wxdl/bin:\$PATH\""
	fi
fi

say ""
say "创建桌面双击入口 ..."
"$wxdl" install-shortcut || say "（创建桌面入口失败，可直接用命令行。）"

say ""
say "完成。接下来："
say "  1. 在微信里复制一条视频号链接"
say "  2. 运行  wxdl            —— 自动读剪贴板并开始下载"
say "  批量：  wxdl get --file links.txt"
say "  整账号：wxdl user \"账号昵称\" --limit 50"
say "  自检：  wxdl doctor"
