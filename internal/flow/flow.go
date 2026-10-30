// Package flow drives the whole capture session: self-check, engine
// provisioning, certificate trust, system proxy, page connection and downloads.
package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fuxm0818/wxdl/internal/cfg"
	"github.com/fuxm0818/wxdl/internal/dialog"
	"github.com/fuxm0818/wxdl/internal/elevate"
	"github.com/fuxm0818/wxdl/internal/events"
	"github.com/fuxm0818/wxdl/internal/proxy"
	"github.com/fuxm0818/wxdl/internal/server"
	"github.com/fuxm0818/wxdl/internal/sysinfo"
	"github.com/fuxm0818/wxdl/internal/upstream"
)

// Options carries the per-run knobs exposed by the CLI flags.
type Options struct {
	DownloadDir string
	Filename    string
	Keep        bool
	NoDialog    bool
	UpstreamZip string
	TaskTimeout time.Duration
	// Spec is passed to the engine as video_variant_spec; "original" asks for
	// the untouched source file instead of the transcoded variant.
	Spec string
	// JSON switches stdout to the NDJSON event stream.
	JSON bool
	// Command is the subcommand name reported in run.start.
	Command string
	// WaitPageForever keeps waiting for the Channels page instead of failing on
	// the per-stage timeouts. The GUI uses it; ordinary CLI runs do not.
	WaitPageForever bool
}

// Session owns one wxdl run: engine process, proxy state and API client.
type Session struct {
	Cfg     *cfg.Config
	Paths   cfg.Paths
	Opt     Options
	Engine  string
	Tag     string
	Service string
	State   *proxy.State

	cli  *server.Client
	proc *server.Process
	em   *events.Emitter

	// unlock releases the single-instance lock taken by Prepare.
	unlock func()
	// mu serialises rollback between the run, --keep and a Ctrl-C handler.
	mu sync.Mutex
}

// New loads configuration and creates ~/.wxdl. It deliberately does not touch
// the download directory: read-only commands (doctor, status, undo) must not
// leave an empty ~/Downloads/wxdl behind next to the folder the user unpacked.
func New(opt Options) (*Session, error) {
	c, err := cfg.Load()
	if err != nil {
		return nil, err
	}
	p, err := cfg.Layout()
	if err != nil {
		return nil, err
	}
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	if opt.DownloadDir != "" {
		c.DownloadDir = opt.DownloadDir
	}
	if abs, err := filepath.Abs(expandHome(c.DownloadDir)); err == nil {
		c.DownloadDir = abs
	}
	st, err := proxy.Load()
	if err != nil {
		return nil, err
	}
	return &Session{
		Cfg:   c,
		Paths: p,
		Opt:   opt,
		cli:   server.New(c.APIPort),
		State: st,
		em:    events.New(os.Stdout, opt.JSON),
	}, nil
}

func (s *Session) failDialog(title, msg string) {
	if s.Opt.NoDialog {
		return
	}
	dialog.Error(title, msg)
}

// Say prints a progress line, or a log event in JSON mode.
func (s *Session) Say(format string, a ...any) {
	if s.em != nil {
		s.em.Say(format, a...)
		return
	}
	fmt.Printf(format+"\n", a...)
}

// Raw writes terminal decoration that a JSON consumer never wants.
func (s *Session) Raw(format string, a ...any) {
	if s.em != nil {
		s.em.Raw(format, a...)
		return
	}
	fmt.Printf(format, a...)
}

// emit publishes one typed event. Outside JSON mode this does nothing.
func (s *Session) emit(t string, fields map[string]any) {
	if s.em != nil {
		s.em.Event(t, fields)
	}
}

func (s *Session) check(name, status, detail, hint string) {
	s.emit("env.check", map[string]any{
		"name": name, "status": status, "detail": detail, "hint": hint,
	})
}

// Ask opens a native dialog unless --yes was given.
func (s *Session) Ask(title, msg, def string) (string, bool) {
	if s.Opt.NoDialog {
		return def, true
	}
	return dialog.Ask(title, msg, def)
}

// Prepare gets the machine ready to capture. It is the long part of every run
// and the one that touches the system, so it stops at the first hard blocker.
func (s *Session) Prepare() error {
	if err := s.prepareMachine(); err != nil {
		return err
	}
	timeout := 2 * time.Minute
	if s.Opt.WaitPageForever {
		timeout = 0 // 0 means "no deadline"
	}
	return s.waitChannels(timeout)
}

// prepareMachine brings the engine, certificate and system proxy up but does
// not require the 视频号 page yet. `wxdl on` stops here and keeps them running.
func (s *Session) prepareMachine() error {
	if runtime.GOOS != "darwin" {
		return coded(CodeInternal, "wxdl 目前只支持 macOS：视频号的播放地址需要微信 macOS 客户端的登录状态",
			"换一台 Mac 再试")
	}
	release, err := s.lockInstance()
	if err != nil {
		return err
	}
	// The lock is kept for the whole capture session and released by Restore, so
	// two simultaneous runs cannot fight over the system proxy or kill each
	// other's engine. A process that dies still releases it.
	s.unlock = release
	if err := os.MkdirAll(s.Cfg.DownloadDir, 0o755); err != nil {
		s.releaseLock()
		return codedWrap(CodeDownloadFailed, "无法创建下载目录", "换一个能写入的下载目录，或检查磁盘空间",
			fmt.Errorf("%s: %w", s.Cfg.DownloadDir, err))
	}
	s.Service = sysinfo.PrimaryService()
	if s.Service == "" {
		s.check("网络服务", "fail", "识别不到当前上网的服务", "确认已连接 Wi-Fi 或有线网络")
		return coded(CodeNetworkUnavailable, "没有识别到正在使用的网络服务", "连接 Wi-Fi 或有线网络后重试")
	}
	s.check("网络服务", "ok", s.Service, "")
	if !sysinfo.WeChatInstalled() {
		s.check("微信客户端", "fail", "没有安装", "请先安装并登录 macOS 微信")
		s.failDialog("需要微信 macOS 客户端",
			"wxdl 要在微信里拦截视频号请求才能拿到加密密钥，请先安装并登录 macOS 微信，再运行一次。")
		return coded(CodeWeChatNotRunning, "没有检测到微信 macOS 客户端", "安装并登录微信后重试")
	}
	s.check("微信客户端", "ok", wechatDetail(), "")

	// Provision the engine while the network is still in its normal state:
	// quitting a proxy app below can break GitHub access.
	if err := s.provision(); err != nil {
		return err
	}

	alreadyOn := s.State != nil && s.State.Applied && proxy.Active(s.Service, s.Cfg.ProxyPort)
	if !alreadyOn {
		if err := s.resolveConflicts(); err != nil {
			return err
		}
	}

	if err := s.startEngine(); err != nil {
		return err
	}
	certFile, err := s.ensureCertificate()
	if err != nil {
		return err
	}
	if !alreadyOn {
		reason := fmt.Sprintf("信任 %s 根证书，并把系统代理临时指向 127.0.0.1:%d", s.Cfg.CAName, s.Cfg.ProxyPort)
		s.emit("auth.request", map[string]any{"reason": reason})
		st, err := proxy.Apply(s.Service, s.Cfg.ProxyPort, certFile, s.Cfg.CAName)
		switch {
		case errors.Is(err, elevate.ErrCancelled):
			s.emit("auth.done", map[string]any{"granted": false, "required": true})
			return coded(CodeAuthCancelled, "你取消了授权窗口，没有改动任何设置", "重新运行并在系统弹窗里输入密码")
		case err != nil:
			s.emit("auth.done", map[string]any{"granted": true, "required": true})
			return err
		}
		s.emit("auth.done", map[string]any{"granted": true, "required": true})
		s.State = st
		if !sysinfo.CertTrusted(certFile) {
			s.check("根证书", "fail", "已安装但系统仍不信任", "在「钥匙串访问」里检查 "+s.Cfg.CAName)
			return coded(CodeAuthCancelled, "证书已安装但系统仍不信任它",
				"在「钥匙串访问」里检查 "+s.Cfg.CAName+"，或运行 wxdl undo 后重试")
		}
		s.check("根证书", "ok", s.Cfg.CAName+"（已受信任）", "")
		s.check("系统代理", "ok", fmt.Sprintf("已指向 wxdl 127.0.0.1:%d", s.Cfg.ProxyPort), "")
	}
	if err := s.persist(); err != nil {
		return err
	}
	return nil
}

// provision resolves the engine binary, downloading it when missing.
func (s *Session) provision() error {
	tag := s.Cfg.UpstreamTag
	if tag == "" {
		tag = upstream.PinnedTag
	}
	s.emit("engine.install", map[string]any{"tag": tag, "phase": "start"})
	bin, resolved, err := upstream.Ensure(s.Paths.Cache, s.Paths.Bin, tag,
		s.Cfg.GitHubToken, s.Opt.UpstreamZip, false)
	if err == nil {
		s.Engine, s.Tag = bin, resolved
		s.emit("engine.install", map[string]any{"tag": resolved, "phase": "done"})
		s.check("抓取引擎", "ok", resolved, "")
		return nil
	}
	s.emit("engine.install", map[string]any{"tag": tag, "phase": "failed"})
	if !s.Opt.NoDialog {
		dialog.Info("第一次准备运行环境",
			"需要下载抓取引擎（约 40MB，来自 GitHub 上的 wx_channels_download）。网络不通时会失败，可以用 VPN 或镜像后重试。")
	}
	return codedWrap(CodeEngineInstallFailed, "抓取引擎没能装好", "检查网络后重试，或用 --zip 指定本地安装包", err)
}

// startEngine writes the engine config and launches it.
func (s *Session) startEngine() error {
	certFile, certKey := s.Cfg.CertFile, s.Cfg.CertKey
	if !fileExists(certFile) || !fileExists(certKey) {
		certFile, certKey = "", ""
	}
	cfgPath, err := upstream.WriteConfig(s.Paths.Work, s.Cfg.DownloadDir, certFile, certKey,
		s.Cfg.CAName, s.Cfg.APIPort, s.Cfg.ProxyPort)
	if err != nil {
		return err
	}
	s.proc = server.NewProcess(s.Engine, s.Paths.Work, cfgPath, s.Paths.Run)
	// An engine already answering on the API port is reused even if wxdl did
	// not start it, so a stale pid file cannot spawn a second one that fails to
	// bind and leaves nothing on the proxy port.
	if s.proc.Running() == 0 && !s.cli.Alive() {
		if !sysinfo.PortFree(s.Cfg.APIPort) {
			return coded(CodePortInUse, fmt.Sprintf("端口 %d 已被其他程序占用", s.Cfg.APIPort),
				"改 ~/.wxdl/config.json 里的 api_port 后重试")
		}
		if !sysinfo.PortFree(s.Cfg.ProxyPort) {
			return coded(CodePortInUse, fmt.Sprintf("代理端口 %d 已被其他程序占用", s.Cfg.ProxyPort),
				"改 ~/.wxdl/config.json 里的 proxy_port 后重试")
		}
		if err := s.proc.Start(s.Cfg.ProxyPort); err != nil {
			return codedWrap(CodeEngineMissing, "抓取引擎没能启动", "运行 wxdl clean 后重试", err)
		}
	}
	if err := s.proc.WaitReady(s.cli, 45*time.Second); err != nil {
		return codedWrap(CodeEngineMissing, "抓取引擎没能启动", "查看日志或运行 wxdl clean 后重试", err)
	}
	if err := s.cli.StartInterceptor(); err != nil {
		return codedWrap(CodeEngineMissing, "抓取引擎的抓包服务没能启动", "运行 wxdl clean 后重试", err)
	}
	// The system proxy is about to point at this port. If the engine's capture
	// listener is not actually up, taking over the proxy would cut the whole
	// machine off, so refuse before any privileged step.
	if !s.waitProxyPort(8 * time.Second) {
		return coded(CodeEngineMissing,
			fmt.Sprintf("抓取引擎没有在 127.0.0.1:%d 开始监听，已停止（未改动系统代理）", s.Cfg.ProxyPort),
			"运行 wxdl clean 后重试，或把 ~/.wxdl/run/engine.log 发给开发者")
	}
	return nil
}

// waitProxyPort polls until the engine reports its own capture listener up. The
// answer comes from the engine's /api/status, so a stranger already sitting on
// that port cannot be mistaken for our listener.
func (s *Session) waitProxyPort(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if s.cli.CaptureListener(s.Cfg.ProxyPort) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// restartEngine reloads the engine so a freshly generated CA is picked up.
func (s *Session) restartEngine() error {
	if s.proc == nil {
		return s.startEngine()
	}
	s.proc.Stop()
	s.proc = nil
	return s.startEngine()
}

// ensureCertificate guarantees a machine-specific root CA exists on disk and
// returns its path. Trusting it is deferred to proxy.Apply, which batches the
// privileged steps into one authorisation prompt.
func (s *Session) ensureCertificate() (string, error) {
	if fileExists(s.Cfg.CertFile) {
		s.check("根证书", "ok", s.Cfg.CAName+"（已存在）", "")
		return s.Cfg.CertFile, nil
	}
	s.Say("生成本机专用根证书 %s ...", s.Cfg.CAName)
	certFile, keyFile, err := s.cli.GenerateLocalCA(s.Cfg.CAName)
	if err != nil {
		return "", codedWrap(CodeEngineMissing, "没能生成本机根证书", "重试；若仍失败请查看日志", err)
	}
	if err := os.Chmod(keyFile, 0o600); err == nil {
		_ = os.Chown(keyFile, os.Getuid(), os.Getgid())
	}
	s.Cfg.CertFile, s.Cfg.CertKey = certFile, keyFile
	if err := s.Cfg.Save(); err != nil {
		return "", err
	}
	if err := s.restartEngine(); err != nil {
		return "", err
	}
	return certFile, nil
}

// resolveConflicts clears proxies that would steal the traffic wxdl needs.
func (s *Session) resolveConflicts() error {
	conflicts := fatalConflicts(sysinfo.Conflicts(s.Service, s.Cfg.ProxyPort))
	if len(conflicts) == 0 {
		s.check("代理冲突", "ok", "没有发现其它抓包/代理程序", "")
		return nil
	}
	names := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		names = append(names, c.Name)
	}
	s.check("代理冲突", "fail", strings.Join(names, "、"), "完全退出这些程序后重试")
	s.Say("检测到会抢走视频号流量的程序: %s", strings.Join(names, "、"))
	if s.Opt.NoDialog {
		return coded(CodeProxyConflict, "有程序会抢走视频号流量", "完全退出这些程序后重试；非交互模式不会替你退出应用")
	}
	quitNow, cancelled := dialog.Confirm("检测到代理冲突",
		"这些程序会把微信的流量改道，导致视频号抓取失败：\n"+strings.Join(names, "、")+
			"\n\nwxdl 可以帮你退出它们。注意：下载结束后不会自动帮你重新打开，需要你手动再开。",
		"帮我退出并继续", "我自己处理")
	if cancelled || !quitNow {
		return coded(CodeProxyConflict, "已取消：请先手动退出上述代理程序", "退出代理程序后重新开始")
	}
	for _, c := range conflicts {
		if !c.Quitable {
			continue
		}
		if err := quitApp(c.AppName); err != nil {
			s.Say("  %s 没能自动退出: %v", c.AppName, err)
		}
	}
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if len(fatalConflicts(sysinfo.Conflicts(s.Service, s.Cfg.ProxyPort))) == 0 {
			s.Say("冲突已清除，继续。")
			s.check("代理冲突", "ok", "冲突已清除", "")
			return nil
		}
	}
	return coded(CodeProxyConflict, "这些程序仍在运行", "手动完全退出这些程序后重试")
}

// pageHint is the one action that fixes a missing Channels page on first run.
const pageHint = "在微信里按 ⌘Q 完全退出后重新打开，再进入「视频号」并点开一条视频"

// waitChannels blocks until a WeChat Channels page has connected through the
// proxy; every Channels API call is served by that page.
func (s *Session) waitChannels(timeout time.Duration) error {
	asked := false
	for {
		if s.waitForPage(timeout) {
			return nil
		}
		if asked || s.Opt.NoDialog || timeout == 0 {
			return coded(CodePageNotConnected, "没有检测到视频号页面", pageHint)
		}
		// First-run injection usually needs a WeChat restart: the webview keeps the
		// old proxy settings in its network service until the app relaunches.
		retry, cancelled := dialog.Confirm("视频号页面还没连上",
			"通常是微信内置浏览器还在用旧的代理设置。wxdl 可以重启微信后重试一次。",
			"重启微信并重试", "不再重试")
		if cancelled || !retry {
			return coded(CodePageNotConnected, "没有检测到视频号页面，无法继续", pageHint)
		}
		// Only one restart per run: a second timeout is a real problem, not the
		// stale-webview one this prompt fixes.
		asked = true
		if err := s.restartWeChat(); err != nil {
			return err
		}
		timeout = 3 * time.Minute
	}
}

// waitForPage opens WeChat when needed and polls the Channels page until the
// deadline. A zero timeout means "keep waiting", which the GUI asks for.
func (s *Session) waitForPage(timeout time.Duration) bool {
	if s.cli.ChannelsAvailable() {
		s.emitWaitPage(timeout, 0, true)
		return true
	}
	if !sysinfo.WeChatRunning() {
		_ = exec.Command("open", "-a", "WeChat").Run()
	}
	if !s.Opt.NoDialog {
		dialog.Info("请在微信里打开视频号",
			"点微信左侧的「视频号」入口，随便打开一条视频并保持页面开着。连接成功后 wxdl 会自动继续。")
	}
	if s.pollPage(timeout) {
		s.Say("视频号页面已连接。")
		return true
	}
	return false
}

// pollPage polls /api/channels/status and reports elapsed time so a GUI can
// show "已等待 00:42" without measuring anything itself.
func (s *Session) pollPage(timeout time.Duration) bool {
	start := time.Now()
	var deadline time.Time
	if timeout > 0 {
		deadline = start.Add(timeout)
	}
	for {
		elapsed := int(time.Since(start).Seconds())
		if s.cli.ChannelsAvailable() {
			s.emitWaitPage(timeout, elapsed, true)
			return true
		}
		s.emitWaitPage(timeout, elapsed, false)
		if timeout > 0 && !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

func (s *Session) emitWaitPage(timeout time.Duration, elapsed int, connected bool) {
	secs := 0
	if timeout > 0 {
		secs = int(timeout.Seconds())
	}
	s.emit("wait.page", map[string]any{
		"timeout": secs, "elapsed": elapsed, "connected": connected,
	})
}

// restartWeChat quits WeChat (asking the user when wxdl may not) and relaunches
// it so the new proxy settings are picked up.
func (s *Session) restartWeChat() error {
	if err := quitApp("WeChat"); err != nil {
		if ok, _ := dialog.Confirm("请手动退出微信",
			"微信不允许 wxdl 自动退出。请右键托盘图标选择「退出微信」，然后点继续。", "我已退出", "取消"); !ok {
			return errors.New("已取消")
		}
	}
	for i := 0; i < 30 && sysinfo.WeChatRunning(); i++ {
		time.Sleep(time.Second)
	}
	_ = exec.Command("open", "-a", "WeChat").Run()
	if !s.Opt.NoDialog {
		dialog.Info("微信已重启", "请在微信里重新打开视频号页面，wxdl 会自动继续。")
	}
	return nil
}

// downloadOne creates a task through the engine and waits for the file. The
// boolean result reports a reused file, which is a skip rather than a transfer.
func (s *Session) downloadOne(selector map[string]any, index, total int) (string, bool, error) {
	args := map[string]any{"download_dir": s.Cfg.DownloadDir}
	for k, v := range selector {
		args[k] = v
	}
	if s.Opt.Filename != "" {
		args["filename"] = s.Opt.Filename
	}
	// skip keeps re-runs cheap. A record whose file is missing (deleted by hand,
	// or an interrupted run) cannot be overwritten by the engine, so wxdl drops
	// the record and creates the task again.
	args["existing_action"] = "skip"
	for attempt := 1; attempt <= 2; attempt++ {
		text, err := s.cli.CallTool("download_wxchannels_video", args)
		if err != nil {
			return "", false, codedWrap(CodeDownloadFailed, "下载任务没能创建", "重试这条；若仍失败请查看日志", err)
		}
		resp := unwrapJSON(text)
		if skipped, _ := resp["skipped"].(bool); skipped {
			task, _ := resp["existing_task"].(map[string]any)
			if task == nil {
				return "", false, coded(CodeDownloadFailed, "上游返回了已存在的任务但没有任务信息", "重试这条")
			}
			id := asInt(task["id"])
			detail, err := s.cli.TaskDetail(id)
			if err != nil {
				return "", false, err
			}
			if path := bestFile(detail); path != "" {
				if detail.Status == server.TaskFinished {
					s.Say("  已存在，直接复用：%s", filepath.Base(path))
					return path, true, nil
				}
				// Still running in another task slot: wait for it instead of
				// reporting the partial file as done.
				path, err := s.waitTask(id, index, total)
				return path, false, err
			}
			if attempt == 2 {
				return "", false, coded(CodeDownloadFailed,
					fmt.Sprintf("任务 %d 的记录里没有可用的文件，清理记录后重新下载仍被跳过", id), "重试这条")
			}
			if err := s.cli.DeleteTasks(id); err != nil {
				return "", false, codedWrap(CodeDownloadFailed, "清理失效的下载记录失败", "重试这条", err)
			}
			s.Say("  旧记录里的文件已经不在了，已清理记录并重新下载。")
			continue
		}
		id := firstTaskID(resp)
		if id <= 0 {
			return "", false, coded(CodeDownloadFailed,
				fmt.Sprintf("没有解析到任务 ID，响应: %s", truncate(text, 200)), "重试这条；若仍失败请查看日志")
		}
		path, err := s.waitTask(id, index, total)
		return path, false, err
	}
	return "", false, coded(CodeDownloadFailed, "下载任务没有完成", "重试这条")
}

// waitTask polls one task until it finishes and returns the output file.
func (s *Session) waitTask(id, index, total int) (string, error) {
	deadline := time.Now().Add(s.Opt.TaskTimeout)
	last := ""
	lastProgress := ""
	for time.Now().Before(deadline) {
		d, err := s.cli.TaskDetail(id)
		if err != nil {
			return "", codedWrap(CodeDownloadFailed, "读取下载任务状态失败", "重试这条", err)
		}
		switch d.Status {
		case server.TaskFinished:
			if p := bestFile(d); p != "" {
				return p, nil
			}
			return "", coded(CodeDownloadFailed, "任务已完成但没有找到输出文件",
				"查看下载目录；若文件不存在请重试")
		case server.TaskFailed, server.TaskCancelled:
			msg := d.ErrorMessage
			if msg == "" {
				msg = "任务被取消"
			}
			return "", coded(CodeDownloadFailed, "下载失败: "+msg, "重试这条；若仍失败请查看日志")
		}
		if key := progressKey(d); key != "" && key != lastProgress {
			lastProgress = key
			s.emitItemProgress(index, total, d)
		}
		if p := progressLine(d); p != "" && p != last {
			last = p
			s.Raw("\r" + p)
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return "", coded(CodeTimeout, fmt.Sprintf("下载超时（%s）", s.Opt.TaskTimeout),
		"重试这条；链接很多时可以先下载一部分")
}

// Restore puts the machine back: system proxy first, then the engine process.
func (s *Session) Restore(removeCert bool) error {
	// The Ctrl-C handler and the normal end of a run can both get here; one of
	// them must not raise a second elevation prompt behind the other's back.
	s.mu.Lock()
	defer s.mu.Unlock()
	// Reaching this point ends the session, so the instance lock goes with it on
	// every path, including a dismissed elevation prompt.
	defer s.releaseLock()

	st := s.State
	if st == nil {
		var err error
		st, err = proxy.Load()
		if err != nil {
			return err
		}
	}
	if st == nil {
		return nil
	}
	service := s.Service
	if service == "" {
		service = st.Service
	}
	if service == "" {
		// A damaged or empty record still needs a name to restore; the current
		// primary service is the only candidate left.
		service = sysinfo.PrimaryService()
	}
	remaining, err := proxy.Restore(st, removeCert, service)
	// The engine is wxdl's own process, so it stops even when the privileged
	// rollback was dismissed; otherwise a failed undo leaves it running.
	s.StopEngine()
	if err != nil {
		return err
	}
	s.State = remaining
	if remaining == nil {
		s.Say("已恢复系统代理设置，抓取引擎已停止。")
	} else {
		s.Say("已恢复系统代理设置；根证书 %s 仍保留在系统里（wxdl undo --cert 可删除）。", s.Cfg.CAName)
	}
	return nil
}

func (s *Session) releaseLock() {
	if s.unlock != nil {
		s.unlock()
		s.unlock = nil
	}
}

// releaseMachine stops the engine and, when a previous --keep run left it on,
// the system proxy too. Commands that delete or replace the engine must not run
// with the proxy pointing at a port that is about to have nothing listening.
func (s *Session) releaseMachine() error {
	if s.State != nil && s.State.Applied {
		s.Say("系统代理还指向 wxdl，先恢复原来的网络设置。")
		if err := s.Restore(false); err != nil {
			return fmt.Errorf("恢复系统代理失败，已停止本次操作（继续会让本机无法上网）: %w", err)
		}
		return nil // Restore stopped the engine too
	}
	s.StopEngine()
	return nil
}

// Abort releases what a run took over when the download cannot start or the
// user stops it. The lock is the ownership test: without it another wxdl run
// owns the engine, and stopping that engine would break the other run.
func (s *Session) Abort() {
	if s.unlock == nil {
		return
	}
	service := s.Service
	if service == "" {
		service = sysinfo.PrimaryService()
	}
	if !proxy.Active(service, s.Cfg.ProxyPort) {
		s.StopEngine()
		s.releaseLock()
		return
	}
	// Restore even with --keep: that flag only applies to a run that finishes
	// normally. Stopping the engine while the proxy still points at its closed
	// port would leave the machine without a working network.
	if err := s.Restore(false); err != nil {
		s.Say("自动恢复系统代理失败，请手动检查「系统设置-网络-代理」：%v", err)
		s.emit("error", map[string]any{
			"code": CodeRestoreFailed,
			"msg":  "停止时没能恢复系统代理",
			"hint": "在「系统设置-网络-代理」里手动关闭代理，或运行 wxdl off",
		})
	}
}

// Serve leaves wxdl running in the background of the machine's network: engine,
// certificate and system proxy stay up so the engine keeps injecting its
// download button into the 视频号 page. wxdl off restores the network.
func (s *Session) Serve() error {
	if err := s.prepareMachine(); err != nil {
		s.Abort()
		return err
	}
	s.Say("抓取引擎与系统代理已就绪，wxdl 退出后仍保持运行。")
	if err := s.waitChannels(90 * time.Second); err != nil {
		// The page may be opened at any time, and the proxy has to stay up for
		// that to work at all, so a missing page is a hint rather than a failure.
		s.Say("视频号页面还没连上：在微信里打开视频号页面并刷新一下即可。")
	} else {
		s.Say("视频号页面已连接。")
	}
	s.Say("视频号窗口里会出现「下载」按钮（引擎注入），可按单条下载；批量照旧用 wxdl get。")
	s.Say("结束并恢复网络：wxdl off")
	s.releaseLock()
	return nil
}

// Finish ends the run, keeping capture alive only when asked.
func (s *Session) Finish() error {
	if s.Opt.Keep {
		s.Say("已保留抓取引擎和系统代理，方便继续下载。执行 wxdl undo 可一键恢复。")
		return nil
	}
	return s.Restore(false)
}

// StopEngine shuts the engine down without touching the proxy.
func (s *Session) StopEngine() { s.engineProcess().Stop() }

// engineProcess returns a handle for the engine belonging to this wxdl
// directory whether or not this run started it: the pid file is how any wxdl
// process finds a `--keep` engine still alive.
func (s *Session) engineProcess() *server.Process {
	if s.proc != nil {
		return s.proc
	}
	return server.NewProcess(s.EngineName(), s.Paths.Work, "", s.Paths.Run)
}

// statusInfo is the snapshot both the human `status` text and its JSON event
// are rendered from, so the two can never disagree.
type statusInfo struct {
	DownloadDir   string
	Engine        string
	Tag           string
	PidText       string
	EngineRunning bool
	API           string
	APIAlive      bool
	Service       string
	Proxy         string
	ProxyOurs     bool
	Cert          string
	CertTrusted   bool
	Page          string
	PageConnected bool
	StartedAt     string
}

func (s *Session) statusInfo() statusInfo {
	tag := s.Tag
	if tag == "" {
		tag = s.Cfg.UpstreamTag
	}
	if tag == "" {
		tag = upstream.PinnedTag
	}
	service := s.Service
	if service == "" {
		// status is read-only: resolve the active interface without touching it.
		service = sysinfo.PrimaryService()
	}
	trusted := s.Cfg.CertFile != "" && sysinfo.CertTrusted(s.Cfg.CertFile)
	connected := s.cli.ChannelsAvailable()
	info := statusInfo{
		DownloadDir:   s.Cfg.DownloadDir,
		Engine:        s.EngineName(),
		Tag:           tag,
		PidText:       pidText(s),
		EngineRunning: s.engineProcess().Running() > 0,
		API:           aliveText(s),
		APIAlive:      s.cli.Alive(),
		Service:       service,
		Cert:          certTextFrom(s, trusted),
		CertTrusted:   trusted,
		Page:          pageTextFrom(connected),
		PageConnected: connected,
	}
	if service != "" {
		info.Proxy = proxyText(s, service)
		info.ProxyOurs = proxy.Active(service, s.Cfg.ProxyPort)
	}
	if st := s.State; st != nil {
		info.StartedAt = st.StartedAt
	}
	return info
}

// Status renders a human-readable report of the machine and the run state.
func (s *Session) Status() string {
	d := s.statusInfo()
	var lines []string
	lines = append(lines, "下载目录: "+d.DownloadDir)
	lines = append(lines, "引擎: "+d.Engine+" "+d.Tag)
	lines = append(lines, "抓取引擎进程: "+d.PidText)
	lines = append(lines, fmt.Sprintf("本机 API: %s", d.API))
	if d.Service != "" {
		lines = append(lines, "网络服务: "+d.Service)
		lines = append(lines, fmt.Sprintf("系统代理: %s", d.Proxy))
		if d.ProxyOurs {
			lines = append(lines, "常驻模式: 开（视频号窗口有「下载」按钮；wxdl off 结束）")
		}
	}
	lines = append(lines, fmt.Sprintf("根证书: %s", d.Cert))
	lines = append(lines, fmt.Sprintf("视频号页面: %s", d.Page))
	if d.StartedAt != "" {
		lines = append(lines, "本次开启时间: "+d.StartedAt)
	}
	return strings.Join(lines, "\n")
}

// StatusData is the machine-readable form consumed by the GUI.
func (s *Session) StatusData() map[string]any {
	d := s.statusInfo()
	return map[string]any{
		"download_dir":   d.DownloadDir,
		"engine":         d.Engine,
		"engine_tag":     d.Tag,
		"engine_process": d.PidText,
		"engine_running": d.EngineRunning,
		"api":            d.API,
		"api_alive":      d.APIAlive,
		"service":        d.Service,
		"proxy":          d.Proxy,
		"proxy_is_ours":  d.ProxyOurs,
		"cert":           d.Cert,
		"cert_present":   s.Cfg.CertFile != "",
		"cert_trusted":   d.CertTrusted,
		"page":           d.Page,
		"page_connected": d.PageConnected,
		"resident":       d.ProxyOurs,
		"started_at":     d.StartedAt,
	}
}

// EmitStatus publishes the single status event `status --json` produces.
func (s *Session) EmitStatus() {
	s.emit("status", s.StatusData())
}

func (s *Session) EngineName() string {
	if s.Engine == "" {
		return filepath.Join(s.Paths.Bin, "wx_video_download")
	}
	return s.Engine
}

func (s *Session) persist() error { return s.Cfg.Save() }

func pidText(s *Session) string {
	// Read the pid file rather than s.proc: `wxdl status` runs in its own
	// process, so an engine left alive by a `--keep` run has no s.proc here.
	if pid := s.engineProcess().Running(); pid > 0 {
		return fmt.Sprintf("运行中 (pid %d)", pid)
	}
	if s.proc != nil {
		return "未运行"
	}
	return "未启动"
}

func aliveText(s *Session) string {
	if s.cli.Alive() {
		return fmt.Sprintf("可访问 (127.0.0.1:%d)", s.Cfg.APIPort)
	}
	return "不可访问"
}

func proxyText(s *Session, service string) string {
	if proxy.Active(service, s.Cfg.ProxyPort) {
		return fmt.Sprintf("已指向 wxdl (127.0.0.1:%d)", s.Cfg.ProxyPort)
	}
	parts := make([]string, 0, 3)
	for _, p := range sysinfo.Proxies(service) {
		state := "关闭"
		if p.Enabled {
			state = p.Host + ":" + strconv.Itoa(p.Port)
		}
		parts = append(parts, p.Kind+"="+state)
	}
	return strings.Join(parts, " ")
}

func certText(s *Session) string {
	return certTextFrom(s, s.Cfg.CertFile != "" && sysinfo.CertTrusted(s.Cfg.CertFile))
}

func certTextFrom(s *Session, trusted bool) string {
	switch {
	case s.Cfg.CertFile == "":
		return "尚未生成"
	case trusted:
		return s.Cfg.CAName + "（已受信任）"
	case sysinfo.CertPresent(s.Cfg.CAName):
		return s.Cfg.CAName + "（在钥匙串里但未受信任）"
	default:
		return s.Cfg.CAName + "（未安装到系统）"
	}
}

func pageText(s *Session) string {
	return pageTextFrom(s.cli.ChannelsAvailable())
}

func pageTextFrom(connected bool) string {
	if connected {
		return "已连接"
	}
	return "未连接（需要在微信里打开视频号页面）"
}

// fatalConflicts drops warnings that wxdl can work around, such as a proxy
// slot wxdl is about to overwrite and restore.
func fatalConflicts(in []sysinfo.Conflict) []sysinfo.Conflict {
	var out []sysinfo.Conflict
	for _, c := range in {
		if c.Name == "系统代理已被占用" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func quitApp(name string) error {
	cmd := exec.Command("osascript", "-e", fmt.Sprintf("tell application %q to quit", name))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func wechatDetail() string {
	if sysinfo.WeChatRunning() {
		return "正在运行"
	}
	return "已安装，启动中"
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func unwrapJSON(text string) map[string]any {
	var raw map[string]any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return map[string]any{}
	}
	data, _ := raw["data"].(map[string]any)
	if data != nil {
		return data
	}
	return raw
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func firstTaskID(resp map[string]any) int {
	if ids, ok := resp["ids"].([]any); ok && len(ids) > 0 {
		if id := asInt(ids[0]); id > 0 {
			return id
		}
	}
	if task, ok := resp["task"].(map[string]any); ok {
		return asInt(task["id"])
	}
	return asInt(resp["id"])
}

// bestFile picks the finished media file out of a task detail payload.
func bestFile(d *server.TaskDetail) string {
	if d == nil {
		return ""
	}
	var fallback string
	var fallbackSize int64 = -1
	for _, f := range d.Files {
		if !f.Exists || f.LocalPath == "" {
			continue
		}
		if f.FileType == "video" || strings.HasSuffix(strings.ToLower(f.LocalPath), ".mp4") {
			return f.LocalPath
		}
		if f.Size > fallbackSize {
			fallback, fallbackSize = f.LocalPath, f.Size
		}
	}
	return fallback
}

func progressLine(d *server.TaskDetail) string {
	done, total := fileTotals(d)
	if total <= 0 {
		return ""
	}
	label := map[int]string{
		server.TaskWaiting: "排队", server.TaskPreparing: "准备",
		server.TaskDownloading: "下载", server.TaskPaused: "暂停",
		server.TaskMerging: "合并",
	}[d.Status]
	if label == "" {
		label = "处理"
	}
	return fmt.Sprintf("%s %.0f%% (%s/%s)   ", label, float64(done)/float64(total)*100,
		humanBytes(done), humanBytes(total))
}

func fileTotals(d *server.TaskDetail) (done, total int64) {
	for _, f := range d.Files {
		done += f.Downloaded
		total += f.Size
	}
	return done, total
}

// progressKey changes when the phase or the downloaded byte count changes, so
// the stream shows smooth progress without repeating identical lines.
func progressKey(d *server.TaskDetail) string {
	done, total := fileTotals(d)
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d", d.Status, done, total)
}

func (s *Session) emitItemProgress(index, total int, d *server.TaskDetail) {
	done, size := fileTotals(d)
	if size <= 0 {
		return
	}
	pct := int(float64(done) / float64(size) * 100)
	s.emit("item.progress", map[string]any{
		"index": index, "total": total, "pct": pct,
		"bytes": done, "total_bytes": size, "status": taskStatusName(d.Status),
	})
}

func taskStatusName(code int) string {
	switch code {
	case server.TaskWaiting:
		return "queued"
	case server.TaskPreparing:
		return "preparing"
	case server.TaskDownloading:
		return "downloading"
	case server.TaskPaused:
		return "paused"
	case server.TaskMerging:
		return "merging"
	}
	return "working"
}

// specFromPath recovers the engine's variant suffix from its
// "{{filename}}_{{spec}}" naming.
func specFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if i := strings.LastIndex(base, "_"); i >= 0 && i+1 < len(base) {
		return base[i+1:]
	}
	return ""
}

func runID() string {
	return "r-" + time.Now().Format("20060102-150405")
}

func humanBytes(n int64) string {
	unit := []string{"B", "KB", "MB", "GB"}
	i := 0
	v := float64(n)
	for v >= 1024 && i < len(unit)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f%s", v, unit[i])
}

// truncate cuts on rune boundaries: byte slices of a Chinese video title or a
// URL with a multibyte tail print as mojibake.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
