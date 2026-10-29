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
	"time"

	"github.com/fuxm0818/wxdl/internal/cfg"
	"github.com/fuxm0818/wxdl/internal/dialog"
	"github.com/fuxm0818/wxdl/internal/elevate"
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
}

// New loads configuration, creates ~/.wxdl and resolves the download dir.
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
	if err := os.MkdirAll(c.DownloadDir, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建下载目录 %s: %w", c.DownloadDir, err)
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
	}, nil
}

func (s *Session) failDialog(title, msg string) {
	if s.Opt.NoDialog {
		return
	}
	dialog.Error(title, msg)
}

// Say prints a progress line.
func (s *Session) Say(format string, a ...any) {
	fmt.Printf(format+"\n", a...)
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
	if runtime.GOOS != "darwin" {
		return errors.New("wxdl 目前只支持 macOS：视频号的播放地址需要微信 macOS 客户端的登录状态")
	}
	s.Service = sysinfo.PrimaryService()
	if s.Service == "" {
		return errors.New("没有识别到正在使用的网络服务，请确认本机已连接 Wi-Fi 或有线网络")
	}
	if !sysinfo.WeChatInstalled() {
		s.failDialog("需要微信 macOS 客户端",
			"wxdl 要在微信里拦截视频号请求才能拿到加密密钥，请先安装并登录 macOS 微信，再运行一次。")
		return errors.New("没有检测到微信 macOS 客户端")
	}

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
		st, err := proxy.Apply(s.Service, s.Cfg.ProxyPort, certFile, s.Cfg.CAName)
		switch {
		case errors.Is(err, elevate.ErrCancelled):
			return errors.New("你取消了授权窗口，无法安装证书和设置代理")
		case err != nil:
			return err
		}
		s.State = st
		if !sysinfo.CertTrusted(certFile) {
			return fmt.Errorf("证书已安装但系统仍不信任它，请在「钥匙串访问」里检查 %s", s.Cfg.CAName)
		}
	}
	if err := s.persist(); err != nil {
		return err
	}
	return s.waitChannels(2 * time.Minute)
}

// provision resolves the engine binary, downloading it when missing.
func (s *Session) provision() error {
	tag := s.Cfg.UpstreamTag
	bin, resolved, err := upstream.Ensure(s.Paths.Cache, s.Paths.Bin, tag,
		s.Cfg.GitHubToken, s.Opt.UpstreamZip, false)
	if err == nil {
		s.Engine, s.Tag = bin, resolved
		return nil
	}
	if !s.Opt.NoDialog {
		dialog.Info("第一次准备运行环境",
			"需要下载抓取引擎（约 40MB，来自 GitHub 上的 wx_channels_download）。网络不通时会失败，可以用 VPN 或镜像后重试。")
	}
	return fmt.Errorf("准备抓取引擎失败: %w", err)
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
	if s.proc.Running() == 0 {
		if !s.cli.Alive() && !sysinfo.PortFree(s.Cfg.APIPort) {
			return fmt.Errorf("端口 %d 已被其他程序占用，请改 ~/.wxdl/config.json 里的 api_port", s.Cfg.APIPort)
		}
		if err := s.proc.Start(s.Cfg.APIPort); err != nil {
			return err
		}
	}
	if err := s.proc.WaitReady(s.cli, 45*time.Second); err != nil {
		return err
	}
	_ = s.cli.EnableMCP()
	return nil
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
		return s.Cfg.CertFile, nil
	}
	s.Say("生成本机专用根证书 %s ...", s.Cfg.CAName)
	certFile, keyFile, err := s.cli.GenerateLocalCA(s.Cfg.CAName)
	if err != nil {
		return "", err
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
		return nil
	}
	names := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		names = append(names, c.Name)
	}
	s.Say("检测到会抢走视频号流量的程序: %s", strings.Join(names, "、"))
	if s.Opt.NoDialog {
		return fmt.Errorf("请先完全退出这些程序（%s）后重试；非交互模式不会替你退出应用", strings.Join(names, "、"))
	}
	quitNow, cancelled := dialog.Confirm("检测到代理冲突",
		"这些程序会把微信的流量改道，导致视频号抓取失败：\n"+strings.Join(names, "、")+
			"\n\nwxdl 可以帮你退出它们。注意：下载结束后不会自动帮你重新打开，需要你手动再开。",
		"帮我退出并继续", "我自己处理")
	if cancelled || !quitNow {
		return errors.New("已取消：请先手动退出上述代理程序")
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
			return nil
		}
	}
	return fmt.Errorf("这些程序仍在运行（%s）。请手动完全退出后重试", strings.Join(names, "、"))
}

// waitChannels blocks until a WeChat Channels page has connected through the
// proxy; every Channels API call is served by that page.
func (s *Session) waitChannels(timeout time.Duration) error {
	if s.cli.ChannelsAvailable() {
		return nil
	}
	if !sysinfo.WeChatRunning() {
		_ = exec.Command("open", "-a", "WeChat").Run()
	}
	if !s.Opt.NoDialog {
		dialog.Info("请在微信里打开视频号",
			"点微信左侧的「视频号」入口，随便打开一条视频并保持页面开着。连接成功后 wxdl 会自动继续。")
	}
	deadline := time.Now().Add(timeout)
	asked := false
	for time.Now().Before(deadline) {
		if s.cli.ChannelsAvailable() {
			s.Say("视频号页面已连接。")
			return nil
		}
		time.Sleep(time.Second)
	}
	if asked || s.Opt.NoDialog {
		return errors.New("2 分钟内没有检测到视频号页面。请确认微信已登录，并重新打开视频号页面")
	}
	// First-run injection usually needs a WeChat restart: the webview keeps the
	// old proxy settings in its network service until the app relaunches.
	retry, cancelled := dialog.Confirm("视频号页面还没连上",
		"通常是微信内置浏览器还在用旧的代理设置。wxdl 可以重启微信后重试一次。",
		"重启微信并重试", "不再重试")
	if cancelled || !retry {
		return errors.New("没有检测到视频号页面，无法继续")
	}
	asked = true
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
	return s.waitChannels(3 * time.Minute)
}

// downloadOne creates a task through the engine and waits for the file.
func (s *Session) downloadOne(selector map[string]any) (string, error) {
	args := map[string]any{"download_dir": s.Cfg.DownloadDir}
	for k, v := range selector {
		args[k] = v
	}
	if s.Opt.Filename != "" {
		args["filename"] = s.Opt.Filename
	}
	// skip keeps re-runs cheap; when the record exists but the file does not,
	// the same task is recreated with overwrite.
	for attempt, action := range []string{"skip", "overwrite"} {
		_ = attempt
		args["existing_action"] = action
		text, err := s.cli.CallTool("download_wxchannels_video", args)
		if err != nil {
			return "", err
		}
		resp := unwrapJSON(text)
		if skipped, _ := resp["skipped"].(bool); skipped {
			task, _ := resp["existing_task"].(map[string]any)
			if task == nil {
				return "", errors.New("上游返回了已存在的任务但没有任务信息")
			}
			id := asInt(task["id"])
			detail, err := s.cli.TaskDetail(id)
			if err == nil && bestFile(detail) != "" {
				return bestFile(detail), nil
			}
			continue
		}
		id := firstTaskID(resp)
		if id <= 0 {
			return "", fmt.Errorf("没有解析到任务 ID，响应: %s", truncate(text, 200))
		}
		path, err := s.waitTask(id)
		if err != nil {
			return "", err
		}
		return path, nil
	}
	return "", errors.New("下载任务没有完成")
}

// waitTask polls one task until it finishes and returns the output file.
func (s *Session) waitTask(id int) (string, error) {
	deadline := time.Now().Add(s.Opt.TaskTimeout)
	last := ""
	for time.Now().Before(deadline) {
		d, err := s.cli.TaskDetail(id)
		if err != nil {
			return "", err
		}
		switch d.Status {
		case server.TaskFinished:
			if p := bestFile(d); p != "" {
				return p, nil
			}
			return "", fmt.Errorf("任务已完成但没有找到输出文件，检查下载目录 %s", s.Cfg.DownloadDir)
		case server.TaskFailed, server.TaskCancelled:
			msg := d.ErrorMessage
			if msg == "" {
				msg = "任务被取消"
			}
			return "", fmt.Errorf("下载失败: %s", msg)
		}
		if p := progressLine(d); p != "" && p != last {
			last = p
			fmt.Print("\r" + p)
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return "", fmt.Errorf("下载超时（%s）", s.Opt.TaskTimeout)
}

// Restore puts the machine back: system proxy first, then the engine process.
func (s *Session) Restore(removeCert bool) error {
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
	remaining, err := proxy.Restore(st, removeCert, service)
	if err != nil {
		return err
	}
	s.State = remaining
	if s.proc != nil {
		s.proc.Stop()
	}
	if remaining == nil {
		s.Say("已恢复系统代理设置，抓取引擎已停止。")
	} else {
		s.Say("已恢复系统代理设置；根证书 %s 仍保留在系统里（wxdl undo --cert 可删除）。", s.Cfg.CAName)
	}
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
func (s *Session) StopEngine() {
	if s.proc != nil {
		s.proc.Stop()
	}
}

// Status renders a human-readable report of the machine and the run state.
func (s *Session) Status() string {
	var lines []string
	tag := s.Tag
	if tag == "" {
		tag = s.Cfg.UpstreamTag
	}
	if tag == "" {
		tag = upstream.PinnedTag
	}
	lines = append(lines, "下载目录: "+s.Cfg.DownloadDir)
	lines = append(lines, "引擎: "+s.EngineName()+" "+tag)
	lines = append(lines, fmt.Sprintf("抓取引擎进程: %s", pidText(s)))
	lines = append(lines, fmt.Sprintf("本机 API: %s", aliveText(s)))
	if s.Service != "" {
		lines = append(lines, "网络服务: "+s.Service)
		lines = append(lines, fmt.Sprintf("系统代理: %s", proxyText(s)))
	}
	lines = append(lines, fmt.Sprintf("根证书: %s", certText(s)))
	lines = append(lines, fmt.Sprintf("视频号页面: %s", pageText(s)))
	if st := s.State; st != nil && st.StartedAt != "" {
		lines = append(lines, "本次开启时间: "+st.StartedAt)
	}
	return strings.Join(lines, "\n")
}

func (s *Session) EngineName() string {
	if s.Engine == "" {
		return filepath.Join(s.Paths.Bin, "wx_video_download")
	}
	return s.Engine
}

func (s *Session) persist() error { return s.Cfg.Save() }

func pidText(s *Session) string {
	if s.proc == nil {
		return "未启动"
	}
	if pid := s.proc.Running(); pid > 0 {
		return fmt.Sprintf("运行中 (pid %d)", pid)
	}
	return "未运行"
}

func aliveText(s *Session) string {
	if s.cli.Alive() {
		return fmt.Sprintf("可访问 (127.0.0.1:%d)", s.Cfg.APIPort)
	}
	return "不可访问"
}

func proxyText(s *Session) string {
	if proxy.Active(s.Service, s.Cfg.ProxyPort) {
		return fmt.Sprintf("已指向 wxdl (127.0.0.1:%d)", s.Cfg.ProxyPort)
	}
	parts := make([]string, 0, 3)
	for _, p := range sysinfo.Proxies(s.Service) {
		state := "关闭"
		if p.Enabled {
			state = p.Host + ":" + strconv.Itoa(p.Port)
		}
		parts = append(parts, p.Kind+"="+state)
	}
	return strings.Join(parts, " ")
}

func certText(s *Session) string {
	switch {
	case s.Cfg.CertFile == "":
		return "尚未生成"
	case sysinfo.CertTrusted(s.Cfg.CertFile):
		return s.Cfg.CAName + "（已受信任）"
	case sysinfo.CertPresent(s.Cfg.CAName):
		return s.Cfg.CAName + "（在钥匙串里但未受信任）"
	default:
		return s.Cfg.CAName + "（未安装到系统）"
	}
}

func pageText(s *Session) string {
	if s.cli.ChannelsAvailable() {
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
	var done, total int64
	for _, f := range d.Files {
		done += f.Downloaded
		total += f.Size
	}
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
