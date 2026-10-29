// Command wxdl is a one-command batch downloader for WeChat Channels videos on
// macOS: it checks the machine, provisions the capture engine, asks for the few
// privileged steps it cannot avoid, then downloads every link it is given.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fuxm0818/wxdl/internal/dialog"
	"github.com/fuxm0818/wxdl/internal/elevate"
	"github.com/fuxm0818/wxdl/internal/flow"
	"github.com/fuxm0818/wxdl/internal/upstream"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	raw := os.Args[1:]
	if len(raw) > 0 && (raw[0] == "-h" || raw[0] == "--help") {
		usage()
		return
	}
	cmd, args := dispatch(raw)
	if len(args) > 0 && wantsHelp(args) {
		// `wxdl get 链接 --help` asks for that subcommand's own usage.
		args = []string{"-h"}
	}
	if err := run(cmd, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return // the flag set already printed its usage
		}
		fmt.Fprintf(os.Stderr, "\n❌ %v\n", err)
		os.Exit(1)
	}
}

// dispatch treats the first non-flag, non-URL word as the subcommand; anything
// else means `wxdl get`, so a bare link or double-click just works.
func dispatch(args []string) (string, []string) {
	if len(args) == 0 {
		return "get", nil
	}
	first := args[0]
	if strings.HasPrefix(first, "-") || flow.IsChannelsLink(first) {
		return "get", args
	}
	return first, args[1:]
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func run(cmd string, args []string) error {
	switch cmd {
	case "get":
		return cmdGet(args)
	case "user":
		return cmdUser(args)
	case "doctor":
		return cmdDoctor(args)
	case "status":
		return cmdStatus(args)
	case "undo":
		return cmdUndo(args)
	case "clean":
		return cmdSimple(args, "clean")
	case "update":
		return cmdUpdate(args)
	case "install-shortcut":
		return cmdShortcut()
	case "version":
		fmt.Printf("wxdl %s (上游钉定版本 %s)\n", version, upstream.PinnedTag)
		fmt.Printf("commit %s, built %s, %s/%s\n", commit, date, runtime.GOOS, runtime.GOARCH)
		return nil
	case "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("未知命令 %q", cmd)
	}
}

type commonFlags struct {
	out      string
	keep     bool
	yes      bool
	zip      string
	filename string
	timeout  time.Duration
}

func bindCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.out, "out", "", "下载目录（默认 ~/Downloads/wxdl）")
	fs.StringVar(&c.out, "o", "", "同 --out")
	fs.BoolVar(&c.keep, "keep", false, "下载后保留代理和抓取引擎，方便连续批量下载")
	fs.BoolVar(&c.yes, "yes", false, "不弹窗，所有交互走标准输入")
	fs.StringVar(&c.zip, "zip", "", "使用本地 wx_video_download_*.zip 安装包，不联网下载")
	fs.StringVar(&c.filename, "filename", "", "自定义文件名（仅单个视频时有意义）")
	fs.DurationVar(&c.timeout, "timeout", 20*time.Minute, "单个视频的最长等待时间")
	return c
}

func (c *commonFlags) options() flow.Options {
	if c.yes {
		dialog.Disable()
	}
	return flow.Options{
		DownloadDir: c.out,
		Filename:    c.filename,
		Keep:        c.keep,
		NoDialog:    c.yes,
		UpstreamZip: c.zip,
		TaskTimeout: c.timeout,
	}
}

// parseFlags accepts flags written after the positional arguments, so
// `wxdl user 昵称 --limit 5` and `wxdl get 链接 -o ~/视频` behave the way people
// type them; the standard library stops parsing at the first positional.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var reordered, positional, literals []string
	literal := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" && !literal {
			// Everything after "--" is an argument, even a word that looks like
			// a flag.
			literal = true
			continue
		}
		if literal {
			literals = append(literals, a)
			continue
		}
		if len(a) < 2 || !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if j := strings.Index(name, "="); j >= 0 {
			name = name[:j]
		}
		reordered = append(reordered, a)
		f := fs.Lookup(name)
		if f == nil {
			continue // let Parse report the unknown flag
		}
		if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); isBool {
			continue
		}
		if !strings.Contains(a, "=") && i+1 < len(args) {
			i++
			reordered = append(reordered, args[i])
		}
	}
	all := append(append(reordered, positional...), "--")
	return fs.Parse(append(all, literals...))
}

// fsArgs returns the positional arguments without the "--" separator, which the
// reorder above can leave inside the tail.
func fsArgs(fs *flag.FlagSet) []string {
	out := make([]string, 0, fs.NArg())
	for _, a := range fs.Args() {
		if a != "--" {
			out = append(out, a)
		}
	}
	return out
}

func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	shared := bindCommon(fs)
	fileFlag := fs.String("file", "", "从文本文件读取链接（- 表示标准输入）")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := flow.New(shared.options())
	if err != nil {
		return err
	}
	installInterruptHandler(s)

	links, err := collectLinks(fsArgs(fs), *fileFlag, s)
	if err != nil {
		return err
	}
	if shared.filename != "" && len(links) > 1 {
		// The engine names every task from this argument, so a batch would
		// overwrite itself instead of producing one file per video.
		return fmt.Errorf("--filename 只适用于单个链接（当前 %d 条），请去掉 --filename 或一次传一条", len(links))
	}
	fmt.Printf("待下载 %d 个视频。\n", len(links))
	return finishError(s.RunLinks(links))
}

func cmdUser(args []string) error {
	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	shared := bindCommon(fs)
	limit := fs.Int("limit", 20, "最多下载多少个视频")
	pages := fs.Int("pages", 5, "最多翻多少页列表")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("用法：wxdl user <视频号昵称/username/该账号任意一条视频链接>")
	}
	if shared.filename != "" {
		return errors.New("user 是批量下载，不支持 --filename；单个文件请用 wxdl get <链接> --filename 名称")
	}
	s, err := flow.New(shared.options())
	if err != nil {
		return err
	}
	installInterruptHandler(s)
	return finishError(s.RunAccount(strings.Join(fsArgs(fs), " "), *limit, *pages))
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	shared := bindCommon(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := flow.New(shared.options())
	if err != nil {
		return err
	}
	fmt.Println("wxdl 环境自检")
	fmt.Println(strings.Repeat("-", 40))
	fails := s.PrintDoctor()
	if fails > 0 {
		return fmt.Errorf("%d 项检查未通过", fails)
	}
	fmt.Println("\n可以运行：wxdl get <视频号链接>")
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	shared := bindCommon(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := flow.New(shared.options())
	if err != nil {
		return err
	}
	fmt.Println(s.Status())
	return nil
}

func cmdUndo(args []string) error {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	shared := bindCommon(fs)
	removeCert := fs.Bool("cert", false, "同时删除本机根证书（下次使用需要重新授权）")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := flow.New(shared.options())
	if err != nil {
		return err
	}
	if err := s.Restore(*removeCert); err != nil {
		return err
	}
	if !*removeCert {
		fmt.Println("提示：根证书保留着，下次批量下载不必再输密码。要一并删除请用 wxdl undo --cert")
	}
	return nil
}

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	shared := bindCommon(fs)
	tag := fs.String("tag", "", "安装指定的上游版本，例如 v260907")
	follow := fs.Bool("follow", false, "跟随上游最新版本")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := flow.New(shared.options())
	if err != nil {
		return err
	}
	if *tag != "" && *follow {
		return errors.New("--tag 和 --follow 只能选一个")
	}
	return s.Update(*tag, *follow)
}

func cmdSimple(args []string, name string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	shared := bindCommon(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	s, err := flow.New(shared.options())
	if err != nil {
		return err
	}
	return s.Clean()
}

func cmdShortcut() error {
	path, err := flow.InstallShortcut()
	if err != nil {
		return err
	}
	fmt.Printf("已在桌面创建双击入口：%s\n", path)
	fmt.Println("用法：在微信里复制视频号链接，然后双击这个文件。")
	fmt.Println("第一次使用需要 macOS 管理员授权（安装本机根证书 + 临时接管系统代理）。")
	return nil
}

func collectLinks(words []string, file string, s *flow.Session) ([]string, error) {
	if len(words) > 0 || file != "" {
		return flow.LinksFrom(words, file)
	}
	if links := flow.ClipboardLinks(); len(links) > 0 {
		return links, nil
	}
	text := flow.PromptLink()
	if text == "" && s.Opt.NoDialog {
		text = flow.ReadLinesFromStdin("请粘贴视频号链接")
	}
	if text == "" {
		return nil, errors.New("没有输入链接")
	}
	return flow.LinksFrom([]string{text}, "")
}

// finishError keeps the exit code meaningful while still reporting success text.
func finishError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, elevate.ErrCancelled) {
		return fmt.Errorf("你取消了管理员授权，无法继续：%w", err)
	}
	return err
}

// installInterruptHandler restores the system proxy if the user hits Ctrl-C.
func installInterruptHandler(s *flow.Session) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range ch {
			fmt.Println("\n收到中断，正在恢复系统代理设置 ...")
			// Stop catching further signals: a second Ctrl-C while the elevation
			// panel is open must not start a parallel rollback.
			signal.Stop(ch)
			s.Abort()
			os.Exit(130)
		}
	}()
}

func usage() {
	prog := filepath.Base(os.Args[0])
	// Pads are counted in terminal cells (CJK glyphs take two), which %-8s-style
	// verbs cannot do; the description column must line up for a friendly tool.
	fmt.Print(`用法：
  ` + prog + ` [视频号链接...]            批量下载（默认命令，不传参数会读剪贴板或弹窗输入）
  ` + prog + ` get <链接...>              批量下载指定链接
  ` + prog + ` get --file links.txt       从文本文件批量下载（一行一个，或整段文字里自动识别）
  ` + prog + ` user <账号昵称>            批量下载某个视频号账号的视频
  ` + prog + ` doctor                     环境自检，不改动任何设置
  ` + prog + ` status                     查看当前状态
  ` + prog + ` undo [--cert]              恢复系统代理（可选删除根证书）
  ` + prog + ` update [--follow|--tag V]  更新抓取引擎（默认钉定上游版本）
  ` + prog + ` clean                      删除抓取引擎文件，保留视频和配置
  ` + prog + ` install-shortcut           在桌面创建双击入口
  ` + prog + ` version                    版本信息

常用参数：
  --out DIR        下载目录（默认 ~/Downloads/wxdl）
  --keep           下载后不恢复代理，方便连续批量下载
  --limit N        user 命令的下载数量上限（默认 20）
  --yes            不弹窗，用于脚本/CI
  --zip FILE       使用本地抓取引擎安装包，不联网

说明：wxdl 只是把上游开源项目 ltaoo/wx_channels_download 的 MITM 能力包装成命令行，
仅供学习研究使用；它不会出售该软件。详见 LICENSE 与 NOTICE-upstream.md。
`)
}
