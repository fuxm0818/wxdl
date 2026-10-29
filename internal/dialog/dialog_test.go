package dialog

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// Every dialog is built as an AppleScript source string, so the bug that
// matters here is a script that does not parse: osascript then fails at
// runtime, the user sees nothing, and a batch dies on a "cancelled" answer.
// osacompile parses without ever drawing a window.
func compiles(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("AppleScript is macOS-only")
	}
	osac, err := exec.LookPath("osacompile")
	if err != nil {
		t.Skip("osacompile not installed")
	}
	cmd := exec.Command(osac, "-o", "/dev/null")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script does not compile: %s\n%s", script, out)
	}
}

func TestScriptsCompile(t *testing.T) {
	hard := "路径 C:\\Users\\x\"y\"\n第二行	含制表符"
	scripts := map[string]string{
		"info":      infoScript("请在微信里打开视频号", hard),
		"error":     errorScript("冲突", hard),
		"confirm":   confirmScript("检测到代理冲突", hard, "帮我退出并继续", "我自己处理"),
		"ask":       askScript("粘贴视频号链接", hard, "https://weixin.qq.com/sph/abc"),
		"choice":    choiceScript("找到多个账号", hard, []string{"账号 A", "账号 B", "账号 C"}),
		"notify":    notifyScript("wxdl", hard),
		"plain":     infoScript("标题", "普通文字"),
		"twobutton": confirmScript("t", "m", "好", "不好"),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) { compiles(t, script) })
	}
}

func TestQuotedEscapesQuotesAndBackslashes(t *testing.T) {
	got := quoted(`说"引用"和\反斜杠`)
	want := `"说\"引用\"和\\反斜杠"`
	if got != want {
		t.Errorf("quoted() = %s, want %s", got, want)
	}
}

func TestQuotedTurnsNewlinesIntoLinefeed(t *testing.T) {
	got := quoted("第一行\n第二行")
	if strings.Contains(got, "\n") {
		t.Errorf("quoted() kept a raw newline: %q", got)
	}
	if !strings.Contains(got, `" & linefeed & "`) {
		t.Errorf("quoted() = %s, want linefeed concatenation", got)
	}
}

func TestButtonAndTextParsing(t *testing.T) {
	out := `button returned:帮我退出并继续`
	if got := button(out); got != "帮我退出并继续" {
		t.Errorf("button() = %q", got)
	}
	out = `button returned:继续, text returned:https://weixin.qq.com/sph/abc`
	if got := button(out); got != "继续" {
		t.Errorf("button() = %q", got)
	}
	if got := text(out); got != "https://weixin.qq.com/sph/abc" {
		t.Errorf("text() = %q", got)
	}
	if got := button(`text returned:only`); got != "" {
		t.Errorf("button() on a reply without a button = %q", got)
	}
}

func TestUserCancelDetection(t *testing.T) {
	if !userCancel("osascript:  User canceled. (-128)") {
		t.Error("expected -128 to count as a cancel")
	}
	if userCancel("execution error:  Not authorized (-1743)") {
		t.Error("a permission failure must not be read as a cancel")
	}
}
