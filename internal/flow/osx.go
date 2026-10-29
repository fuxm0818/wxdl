package flow

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fuxm0818/wxdl/internal/dialog"
)

// run executes a short macOS helper command and discards its output.
func run(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func notify(title, msg string) { dialog.Notify(title, msg) }

// ffprobeDuration returns a human duration when ffprobe is installed.
func ffprobeDuration(path string) string {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || secs <= 0 {
		return ""
	}
	total := int(secs)
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, (total%3600)/60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func selectorLabel(sel map[string]any) string {
	if u := asString(sel["url"]); u != "" {
		return u
	}
	return fmt.Sprintf("oid=%s nid=%s eid=%s", asString(sel["oid"]), asString(sel["nid"]), asString(sel["eid"]))
}

// chooseAccount lets the user pick between the top search hits.
func chooseAccount(cands []map[string]any) (string, bool) {
	byLabel := map[string]string{}
	var labels []string
	for _, c := range cands {
		user := asString(c["username"])
		nick := asString(c["nickname"])
		if nick == "" {
			nick = user
		}
		if _, dup := byLabel[nick]; dup {
			continue
		}
		byLabel[nick] = user
		labels = append(labels, nick)
	}
	if len(labels) == 0 {
		return "", true
	}
	if len(labels) > 3 {
		labels = labels[:3]
	}
	pick, cancelled := dialog.Choice("找到多个视频号账号", "请选择要下载的账号：", labels)
	if cancelled {
		return "", true
	}
	if user, ok := byLabel[pick]; ok {
		return user, false
	}
	return "", true
}

// PromptLink asks for a link, pre-filled from the pasteboard when it holds one.
func PromptLink() string {
	links := ClipboardLinks()
	if len(links) > 1 {
		ok, cancelled := dialog.Confirm("发现多个链接",
			fmt.Sprintf("剪贴板里有 %d 个视频号链接，全部下载吗？（选“一个一个下”只处理第一个）", len(links)),
			"全部下载", "只下第一个")
		if cancelled {
			return ""
		}
		if !ok {
			return links[0]
		}
		return strings.Join(links, "\n")
	}
	def := ""
	if len(links) == 1 {
		def = links[0]
	}
	text, cancelled := dialog.Ask("粘贴视频号链接",
		"在微信里点视频「分享-复制链接」，然后回到这里（可以直接粘贴多个链接，每行一个）", def)
	if cancelled {
		return ""
	}
	return text
}

// ReadLinesFromStdin reads pasted multi-line input when there is no GUI.
func ReadLinesFromStdin(prompt string) string {
	fmt.Println(prompt + "（每行一个链接，回车两次结束）")
	reader := bufio.NewReader(os.Stdin)
	var b strings.Builder
	for {
		line, err := reader.ReadString('\n')
		b.WriteString(line)
		if strings.TrimSpace(line) == "" || err != nil {
			break
		}
	}
	return b.String()
}

// shSingleQuote quotes a path for bash. Go's %q emits double quotes, where a "$"
// or backtick inside an install path would be expanded by the shell.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// InstallShortcut drops a double-clickable .command file on the Desktop that
// runs `wxdl get` in Terminal, so non-technical users never type a command.
func InstallShortcut() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "Desktop", "批量下载视频号视频.command")
	// A user-owned Mac has no Desktop folder whenever it was deleted or the
	// account is brand new; creating it is cheaper than failing the install.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		path = filepath.Join(home, ".wxdl", "批量下载视频号视频.command")
	}
	body := fmt.Sprintf(`#!/bin/bash
# 双击即可批量下载：先在微信里复制视频号链接，再双击本文件。
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"
exec %s get
`, shSingleQuote(exe))
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return "", err
	}
	// Finder shows .command files as executable only after this nudge.
	_ = run("xattr", "-d", "com.apple.quarantine", path)
	return path, nil
}
