package flow

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var anyURL = regexp.MustCompile(`https?://[A-Za-z0-9./?=_%~+&@#$!();:,~\-\[\]]+`)

// channelHosts are the hosts a Channels share link can use.
var channelHosts = []string{"weixin.qq.com", "channels.weixin.qq.com", "finder.video.qq.com"}

// IsChannelsLink reports whether the URL is a WeChat Channels share link.
func IsChannelsLink(u string) bool {
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	for _, h := range channelHosts {
		if strings.Contains(lower, "://"+h+"/") || strings.Contains(lower, "://"+h) {
			return true
		}
	}
	return false
}

// ExtractLinks pulls every Channels link out of arbitrary text (chat logs,
// multi-line clipboard content) in order, de-duplicated.
func ExtractLinks(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range anyURL.FindAllString(text, -1) {
		u := strings.TrimRight(raw, ".,;:!。，；：！、")
		if !IsChannelsLink(u) || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// ClipboardLinks reads the pasteboard and returns any Channels links on it.
func ClipboardLinks() []string {
	cmd := exec.Command("pbpaste")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return ExtractLinks(string(out))
}

// LinksFrom collects links from command-line words and a --file argument.
// "-" means read the text from stdin.
func LinksFrom(words []string, file string) ([]string, error) {
	var texts []string
	for _, w := range words {
		if w == "-" && file == "" {
			raw, err := readStdin()
			if err != nil {
				return nil, err
			}
			texts = append(texts, raw)
			continue
		}
		texts = append(texts, w)
	}
	if file != "" && file != "-" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("读取链接文件失败: %w", err)
		}
		texts = append(texts, string(raw))
	} else if file == "-" {
		raw, err := readStdin()
		if err != nil {
			return nil, err
		}
		texts = append(texts, raw)
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, u := range ExtractLinks(t) {
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有从输入里找到视频号链接（支持 https://weixin.qq.com/sph/… 或 https://channels.weixin.qq.com/…）")
	}
	return out, nil
}

func readStdin() (string, error) {
	raw, err := os.ReadFile("/dev/stdin")
	if err != nil {
		return "", fmt.Errorf("读取标准输入失败: %w", err)
	}
	return string(raw), nil
}
