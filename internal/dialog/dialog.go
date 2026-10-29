// Package dialog presents questions to the user. It prefers native macOS
// dialogs so non-technical users never have to read terminal output, and
// falls back to stdin prompts whenever a dialog cannot be shown.
package dialog

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

var disabled bool

// Disable forces the stdin fallback, used by --yes and scripted runs.
func Disable() { disabled = true }

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\t", " ")
}

// quoted builds an AppleScript string expression. A literal newline is illegal
// inside an AppleScript string, so multi-line text becomes linefeed
// concatenations.
func quoted(s string) string {
	parts := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	expr := make([]string, 0, len(parts))
	for _, p := range parts {
		expr = append(expr, `"`+escape(p)+`"`)
	}
	return strings.Join(expr, " & linefeed & ")
}

func infoScript(title, msg string) string {
	return bannerScript(title, msg, "note")
}

func errorScript(title, msg string) string {
	return bannerScript(title, msg, "stop")
}

func bannerScript(title, msg, icon string) string {
	return fmt.Sprintf(`display dialog %s with title %s buttons {"好"} default button "好" with icon %s`,
		quoted(msg), quoted(title), icon)
}

func notifyScript(title, msg string) string {
	return fmt.Sprintf(`display notification %s with title %s`, quoted(msg), quoted(title))
}

func confirmScript(title, msg, yesLabel, noLabel string) string {
	return fmt.Sprintf(`display dialog %s with title %s buttons {%s, %s} default button %s with icon caution`,
		quoted(msg), quoted(title), quoted(noLabel), quoted(yesLabel), quoted(yesLabel))
}

func askScript(title, msg, def string) string {
	return fmt.Sprintf(`display dialog %s with title %s default answer %s buttons {"取消", "继续"} default button "继续"`,
		quoted(msg), quoted(title), quoted(def))
}

func choiceScript(title, msg string, shown []string) string {
	list := make([]string, 0, len(shown)+1)
	for i := len(shown) - 1; i >= 0; i-- {
		list = append(list, quoted(shown[i]))
	}
	list = append(list, `"取消"`)
	return fmt.Sprintf(`display dialog %s with title %s buttons {%s} default button %s`,
		quoted(msg), quoted(title), strings.Join(list, ", "), quoted(shown[0]))
}

func run(script string) (string, string, error) {
	cmd := exec.Command("osascript", "-e", script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), stderr.String(), err
}

// userCancel recognises the "user pressed Cancel / closed the dialog" error
// (-128), which is a real answer rather than a broken dialog.
func userCancel(stderr string) bool {
	return strings.Contains(stderr, "-128") || strings.Contains(stderr, "User canceled")
}

func field(out, key string) string {
	idx := strings.Index(out, key)
	if idx < 0 {
		return ""
	}
	rest := out[idx+len(key):]
	for _, stop := range []string{", button returned:", ", text returned:"} {
		if c := strings.Index(rest, stop); c >= 0 {
			rest = rest[:c]
		}
	}
	return strings.TrimSpace(rest)
}

func button(out string) string { return field(out, "button returned:") }

func text(out string) string { return field(out, "text returned:") }

// Info shows a message the user only has to acknowledge. The terminal copy is
// always printed because a dialog can be missed on a busy desktop.
func Info(title, msg string) {
	fmt.Printf("ℹ️  %s: %s\n", title, msg)
	if disabled {
		return
	}
	_, _, _ = run(infoScript(title, msg))
}

// Error shows a blocking failure message.
func Error(title, msg string) {
	fmt.Printf("❌ %s: %s\n", title, msg)
	if disabled {
		return
	}
	_, _, _ = run(errorScript(title, msg))
}

// Notify posts a non-blocking banner for long-step progress.
func Notify(title, msg string) {
	if disabled {
		return
	}
	_, _, _ = run(notifyScript(title, msg))
}

// Confirm asks a yes/no question. cancel means the user dismissed the dialog.
func Confirm(title, msg, yesLabel, noLabel string) (yes bool, cancelled bool) {
	if disabled {
		return confirmStdin(title, msg)
	}
	out, stderr, err := run(confirmScript(title, msg, yesLabel, noLabel))
	switch {
	case err != nil && userCancel(stderr):
		return false, true
	case err != nil, button(out) == "":
		// No window server (SSH, launchd) or a dialog we could not draw:
		// asking in the terminal beats aborting a batch the user is waiting on.
		return confirmStdin(title, msg)
	default:
		return button(out) == yesLabel, false
	}
}

func confirmStdin(title, msg string) (yes bool, cancelled bool) {
	fmt.Printf("%s\n%s\n输入 y 确认，其他任意键取消：", title, msg)
	line := readLine()
	if line == "" {
		// Nothing to read means "not confirmed", never "the user cancelled".
		return false, false
	}
	return strings.EqualFold(line, "y"), false
}

// Ask opens a text input box with a prefilled default.
func Ask(title, msg, def string) (string, bool) {
	if disabled {
		return askStdin(title, msg, def)
	}
	out, stderr, err := run(askScript(title, msg, def))
	switch {
	case err != nil && userCancel(stderr):
		return "", true
	case err != nil, button(out) == "", button(out) == "取消":
		return askStdin(title, msg, def)
	}
	got := text(out)
	if got == "" {
		return def, false
	}
	return got, false
}

func askStdin(title, msg, def string) (string, bool) {
	fmt.Printf("%s\n%s [%s]: ", title, msg, def)
	line := readLine()
	if line == "" {
		line = def
	}
	return line, false
}

// Choice shows up to three option buttons plus Cancel and returns the label the
// user pressed. AppleScript caps a dialog at three buttons.
func Choice(title, msg string, options []string) (string, bool) {
	if len(options) == 0 {
		return "", true
	}
	if disabled {
		return choiceStdin(title, msg, options)
	}
	shown := options
	if len(shown) > 3 {
		shown = shown[:3]
	}
	out, stderr, err := run(choiceScript(title, msg, shown))
	switch {
	case err != nil && userCancel(stderr):
		return "", true
	case err != nil, button(out) == "", button(out) == "取消":
		// The terminal list is not capped at three, so offer every option.
		return choiceStdin(title, msg, options)
	}
	return button(out), false
}

func choiceStdin(title, msg string, options []string) (string, bool) {
	fmt.Printf("%s\n%s\n", title, msg)
	for i, o := range options {
		fmt.Printf("  %d) %s\n", i+1, o)
	}
	fmt.Print("输入序号：")
	n, err := strconv.Atoi(readLine())
	if err != nil || n < 1 || n > len(options) {
		return "", true
	}
	return options[n-1], false
}

func readLine() string {
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}
