package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fuxm0818/wxdl/internal/proxy"
	"github.com/fuxm0818/wxdl/internal/sysinfo"
	"github.com/fuxm0818/wxdl/internal/upstream"
)

// Check is one line of the self-test report.
type Check struct {
	Name   string
	Status string // ok | warn | fail
	Detail string
	Hint   string
}

// Doctor inspects everything wxdl needs without changing the machine.
func (s *Session) Doctor() []Check {
	var out []Check
	add := func(name, status, detail, hint string) {
		out = append(out, Check{Name: name, Status: status, Detail: detail, Hint: hint})
	}

	status := "ok"
	if runtime.GOOS != "darwin" {
		status = "fail"
	}
	add("操作系统", status, fmt.Sprintf("macOS %s / %s", sysinfo.MacOSVersion(), sysinfo.Arch()),
		"wxdl 只支持 macOS，因为视频号的播放地址依赖微信 macOS 客户端的登录状态")

	if sysinfo.MacOSMajor() >= 11 {
		add("系统版本", "ok", fmt.Sprintf("macOS %d", sysinfo.MacOSMajor()), "")
	} else {
		add("系统版本", "warn", "低于 macOS 11", "旧系统上微信客户端可能不再更新，代理与证书行为也可能不同")
	}

	switch {
	case !sysinfo.WeChatInstalled():
		add("微信客户端", "fail", "没有安装", "请先安装并登录 macOS 微信")
	case !sysinfo.WeChatRunning():
		add("微信客户端", "warn", "已安装但未运行", "wxdl 会自动打开微信，你需要在里面登录")
	default:
		add("微信客户端", "ok", "正在运行", "")
	}

	service := s.Service
	if service == "" {
		service = sysinfo.PrimaryService()
	}
	if service == "" {
		add("网络服务", "fail", "识别不到当前上网的服务", "确认已连接 Wi-Fi 或有线网络")
	} else {
		add("网络服务", "ok", service, "")
		if proxy.Active(service, s.Cfg.ProxyPort) {
			add("系统代理", "ok", fmt.Sprintf("已指向 wxdl 127.0.0.1:%d", s.Cfg.ProxyPort), "")
		} else {
			add("系统代理", "warn", proxySummary(service), "wxdl 运行时会自动接管，结束后自动恢复")
		}
		var conflicts []string
		for _, c := range fatalConflicts(sysinfo.Conflicts(service, s.Cfg.ProxyPort)) {
			conflicts = append(conflicts, c.Name)
		}
		if len(conflicts) > 0 {
			add("代理冲突", "fail", strings.Join(conflicts, "、"), "这些程序会抢走微信流量，抓取前需要退出")
		} else {
			add("代理冲突", "ok", "没有发现其它抓包/代理程序", "")
		}
	}

	bin := filepath.Join(s.Paths.Bin, "wx_video_download")
	if fileExists(bin) {
		add("抓取引擎", "ok", bin+" "+engineTag(s), "")
	} else if zip, tag := upstream.Bundled(s.Opt.UpstreamZip); zip != "" {
		add("抓取引擎", "warn", "未安装，已内置安装包 "+tag, "第一次运行会自动解压，无需联网")
	} else {
		add("抓取引擎", "warn", "未安装", "第一次运行需要从 GitHub 下载约 40MB；国内网络不通时用 --zip 指定本地安装包")
	}

	if s.Cfg.CertFile != "" && sysinfo.CertTrusted(s.Cfg.CertFile) {
		add("根证书", "ok", s.Cfg.CAName+" 已受信任", "")
	} else if sysinfo.CertPresent(s.Cfg.CAName) {
		add("根证书", "warn", s.Cfg.CAName+" 在钥匙串中但未受信任", "wxdl 下次运行会重新请求授权")
	} else {
		add("根证书", "warn", "未安装", "wxdl 会为这台电脑生成专用根证书，需要一次管理员授权")
	}

	if sysinfo.PortFree(s.Cfg.APIPort) || engineAlive(s) {
		add("API 端口", "ok", fmt.Sprintf("127.0.0.1:%d 可用", s.Cfg.APIPort), "")
	} else {
		add("API 端口", "fail", fmt.Sprintf("%d 被占用", s.Cfg.APIPort), "改 ~/.wxdl/config.json 里的 api_port")
	}
	if sysinfo.PortFree(s.Cfg.ProxyPort) || proxy.Active(service, s.Cfg.ProxyPort) {
		add("代理端口", "ok", fmt.Sprintf("127.0.0.1:%d 可用", s.Cfg.ProxyPort), "")
	} else {
		add("代理端口", "fail", fmt.Sprintf("%d 被占用", s.Cfg.ProxyPort), "改 ~/.wxdl/config.json 里的 proxy_port")
	}

	if engineAlive(s) {
		if s.cli.ChannelsAvailable() {
			add("视频号页面", "ok", "已连接", "")
		} else {
			add("视频号页面", "warn", "未连接", "在微信里打开视频号页面后即可自动连接")
		}
	}

	if sysinfo.FFprobeAvailable() {
		add("ffprobe", "ok", "可校验成片", "")
	} else {
		add("ffprobe", "warn", "未安装（可选）", "brew install ffmpeg 后 wxdl 会校验时长和分辨率")
	}

	add("下载目录", "ok", s.Cfg.DownloadDir, "")
	return out
}

// PrintDoctor renders the checks and returns the number of failures.
func (s *Session) PrintDoctor() int {
	checks := s.Doctor()
	width := 0
	for _, c := range checks {
		if n := len([]rune(c.Name)); n > width {
			width = n
		}
	}
	fails := 0
	for _, c := range checks {
		icon := map[string]string{"ok": "✅", "warn": "⚠️ ", "fail": "❌"}[c.Status]
		if c.Status == "fail" {
			fails++
		}
		pad := width - len([]rune(c.Name))
		if pad < 1 {
			pad = 1
		}
		fmt.Printf("%s %s%s %s\n", icon, c.Name, strings.Repeat(" ", pad), c.Detail)
		if c.Status != "ok" && c.Hint != "" {
			fmt.Printf("   - %s\n", c.Hint)
		}
	}
	return fails
}

// Clean removes the downloaded engine and its cache, keeping config and files.
func (s *Session) Clean() error {
	var removed []string
	for _, p := range []string{s.Paths.Cache, filepath.Join(s.Paths.Bin, "wx_video_download"),
		filepath.Join(s.Paths.Bin, ".upstream_tag")} {
		if _, err := os.Stat(p); err == nil {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			removed = append(removed, p)
		}
	}
	if len(removed) == 0 {
		s.Say("没有需要清理的抓取引擎文件。")
		return nil
	}
	s.Say("已清理：\n  %s", strings.Join(removed, "\n  "))
	s.Say("下载的视频和 ~/.wxdl/config.json 未改动。")
	return nil
}

// Update re-provisions the engine. follow=true moves to the newest upstream
// release and records it; otherwise the pinned tag is used.
func (s *Session) Update(tag string, follow bool) error {
	if follow {
		latest, err := upstream.LatestTag(s.Cfg.GitHubToken)
		if err != nil {
			return err
		}
		tag = latest
	}
	if tag != "" {
		s.Cfg.UpstreamTag = tag
	}
	bin, used, err := upstream.Ensure(s.Paths.Cache, s.Paths.Bin, s.Cfg.UpstreamTag,
		s.Cfg.GitHubToken, s.Opt.UpstreamZip, true)
	if err != nil {
		return err
	}
	if err := s.Cfg.Save(); err != nil {
		return err
	}
	s.Engine, s.Tag = bin, used
	s.Say("抓取引擎已更新：%s (%s)", bin, used)
	if v := upstream.Version(bin); v != "" {
		s.Say("版本信息：%s", v)
	}
	return nil
}

func engineTag(s *Session) string {
	raw, err := os.ReadFile(filepath.Join(s.Paths.Bin, ".upstream_tag"))
	if err == nil && len(raw) > 0 {
		return strings.TrimSpace(string(raw))
	}
	return ""
}

func engineAlive(s *Session) bool { return s.cli.Alive() }

func proxySummary(service string) string {
	parts := make([]string, 0, 3)
	for _, p := range sysinfo.Proxies(service) {
		if p.Enabled {
			parts = append(parts, p.Kind+"="+p.Host+":"+fmt.Sprint(p.Port))
		}
	}
	if len(parts) == 0 {
		return "未开启"
	}
	return strings.Join(parts, " ")
}
