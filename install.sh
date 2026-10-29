#!/usr/bin/env bash
# Install wxdl from a release archive.
#
#   sh install.sh              常规安装（全程不需要管理员密码）
#   sh install.sh --yes        不提问，自动把 PATH 写进 shell 配置
#   sh install.sh --no-path    只装文件，不动任何 shell 配置
#   sh install.sh --link       另外在 /usr/local/bin 建软链（这一步才需要密码）
#
# Copies the command to ~/.wxdl/bin and the pinned engine package to
# ~/.wxdl/vendor, so the first download works without GitHub access.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
home="${HOME:?}"

say() { printf '%s\n' "$*"; }

assume_yes=0
skip_path=0
want_link=0
for a in "$@"; do
	case "$a" in
	--yes) assume_yes=1 ;;
	--no-path) skip_path=1 ;;
	--link) want_link=1 ;;
	-h | --help)
		sed -n '2,10p' "${BASH_SOURCE[0]}"
		exit 0
		;;
	*)
		say "未知参数：$a（sh install.sh --help 看用法）" >&2
		exit 2
		;;
	esac
done

if [ ! -x "$here/bin/wxdl" ] && [ ! -f "$here/bin/wxdl" ]; then
	say "找不到 $here/bin/wxdl —— 请先解压发行包再运行本脚本。" >&2
	exit 1
fi

# A folder unpacked from a browser download inherits com.apple.quarantine, and
# Gatekeeper then refuses to launch the (unsigned but ad-hoc signed) binary with
# "Apple 无法验证 …". Strip it across the whole archive before anything runs, so
# both ./bin/wxdl here and the copy installed below are usable.
if [ -n "$(xattr -l "$here/bin/wxdl" 2>/dev/null | grep com.apple.quarantine || true)" ]; then
	say "清除本机下载隔离属性（com.apple.quarantine）..."
	xattr -dr com.apple.quarantine "$here" 2>/dev/null || true
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
for f in "$home/.wxdl/bin/wxdl" "$home/.wxdl/vendor"/*; do
	[ -e "$f" ] || continue
	xattr -d com.apple.quarantine "$f" 2>/dev/null || true
done

wxdl="$home/.wxdl/bin/wxdl"
"$wxdl" version >/dev/null || {
	say "装好了但这个二进制没能运行，请把上面的报错发给我。" >&2
	exit 1
}

PATH_LINE='export PATH="$HOME/.wxdl/bin:$PATH"'
MARKER='# added by wxdl install.sh'

# The command lives in ~/.wxdl/bin and the engine only needs an absolute path,
# so a /usr/local/bin symlink is convenience, not a requirement — and it is the
# only reason this script would ever ask for a password.
if [ "$want_link" = 1 ]; then
	link_dir="/usr/local/bin"
	if [ -w "$link_dir" ]; then
		ln -sf "$wxdl" "$link_dir/wxdl"
		say "已创建 $link_dir/wxdl"
	else
		say "创建 $link_dir/wxdl 需要管理员权限：接下来会请你输一次本机密码（sudo 提示符）。"
		if sudo ln -sf "$wxdl" "$link_dir/wxdl"; then
			say "已创建 $link_dir/wxdl"
		else
			say "软链创建失败，不影响使用：继续用 ~/.wxdl/bin/wxdl 即可。"
		fi
	fi
fi

shell_rc="$home/.zshrc"
case "${SHELL:-}" in
*bash*) shell_rc="$home/.bash_profile" ;;
esac

if [ "$skip_path" = 0 ]; then
	if found="$(command -v wxdl 2>/dev/null)"; then
		say "终端里已经能直接用 wxdl（$found），不改你的 shell 配置。"
	elif grep -qs "$MARKER" "$shell_rc" 2>/dev/null; then
		say "已在 $shell_rc 里配置过 PATH，跳过。"
	else
		agree=1
		if [ "$assume_yes" = 0 ] && [ -t 0 ]; then
			printf '把 ~/.wxdl/bin 加进 PATH，终端里才能直接敲 wxdl（只往 %s 追加两行，随时可删）。同意吗？[Y/n] ' "$shell_rc"
			read -r ans || ans=y
			case "$ans" in
			n | N*) agree=0 ;;
			esac
		fi
		if [ "$agree" = 1 ]; then
			printf '\n%s\n%s\n' "$MARKER" "$PATH_LINE" >>"$shell_rc"
			say "已写入 $shell_rc —— 重开终端，或在当前终端执行："
			say "  $PATH_LINE"
		else
			say "没有改你的 shell 配置。想直接用 wxdl 这个命令，自己把这行加进 $shell_rc ："
			say "  $PATH_LINE"
			say "或者每次都写全路径：~/.wxdl/bin/wxdl"
		fi
	fi
fi

say ""
say "创建桌面双击入口 ..."
"$wxdl" install-shortcut || say "（创建桌面入口失败，可直接用命令行。）"

say ""
say "完成。接下来："
say "  1. 在微信里复制一条视频号链接"
say "  2. 运行  wxdl            —— 自动读剪贴板并开始下载"
say "     （改过 PATH 就先重开终端；否则用 ~/.wxdl/bin/wxdl）"
say "  批量：  wxdl get --file links.txt"
say "  整账号：wxdl user \"账号昵称\" --limit 50"
say "  自检：  wxdl doctor"
