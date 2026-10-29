// Package dialog presents questions to the user. It prefers native macOS
// dialogs so non-technical users never have to read terminal output, and
// falls back to stdin prompts when no window server is reachable.
package dialog

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

var (
	once       sync.Once
	nativeTest bool
	disabled   bool
)

// Disable forces the stdin fallback, used by --yes and scripted runs.
func Disable() { disabled = true }

// escape turns an arbitrary Go string into safe AppleScript string literal
// content. Newlines are flattened because they cannot survive a dialog prompt.
func escape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	return s
}

func run(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Native reports whether native dialogs are usable in this session. It is
// cached after the first probe so a headless run does not retry per question.
func Native() bool {
	if disabled {
		return false
	}
	once.Do(func() {
		nativeTest = false
		if _, err := exec.LookPath("osascript"); err != nil {
			return
		}
		// A real panel is opened and dismissed immediately; `display dialog`
		// fails outright when the process has no GUI session.
		_, err := run(`display dialog "wxdl 环境检测" buttons {"OK"} default button "OK" giving up after 1`)
		nativeTest = err == nil
	})
	return nativeTest
}

// Info shows a message the user only has to acknowledge.
func Info(title, msg string) {
	fmt.Printf("ℹ️  %s: %s\n", title, msg)
	if !Native() {
		return
	}
	_, _ = run(fmt.Sprintf(`display dialog %s with title %s buttons {"好"} default button "好" with icon note`,
		escape(msg), escape(title)))
}

// Error shows a blocking failure message.
func Error(title, msg string) {
	fmt.Printf("❌ %s: %s\n", title, msg)
	if !Native() {
		return
	}
	_, _ = run(fmt.Sprintf(`display dialog %s with title %s buttons {"好"} default button "好" with icon stop`,
		escape(msg), escape(title)))
}

// Confirm asks a yes/no question. cancel means the user closed the dialog.
func Confirm(title, msg string, yesLabel, noLabel string) (yes bool, cancelled bool) {
	if !Native() {
		fmt.Printf("%s\n%s\n输入 y 继续，其他任意键取消：", title, msg)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		return strings.EqualFold(strings.TrimSpace(line), "y"), false
	}
	_, err := run(fmt.Sprintf(
		`tell application "System Events" to display dialog %s with title %s buttons {%s, %s} default button {%s} with icon caution`,
		escape(msg), escape(title), escape(noLabel), escape(yesLabel), escape(yesLabel)))
	if err != nil {
		// A dismissed dialog or the no button both mean stop.
		return false, true
	}
	return true, false
}

// Ask opens a text input box with a prefilled default.
func Ask(title, msg, def string) (string, bool) {
	if !Native() {
		fmt.Printf("%s\n%s [%s]: ", title, msg, def)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			line = def
		}
		return line, false
	}
	out, err := run(fmt.Sprintf(
		`tell application "System Events" to display dialog %s with title %s default answer %s buttons {"取消","继续"} default button "继续"`,
		escape(msg), escape(title), escape(def)))
	if err != nil {
		return "", true
	}
	// Response is `text returned of result ...` — osascript prints `button returned:X, text returned:Y`.
	if idx := strings.Index(out, "text returned:"); idx >= 0 {
		return strings.TrimSpace(out[idx+len("text returned:"):]), false
	}
	return "", true
}

// Notify posts a non-blocking banner for long-step progress.
func Notify(title, msg string) {
	if !Native() {
		return
	}
	_, _ = run(fmt.Sprintf(`display notification %s with title %s`, escape(msg), escape(title)))
}

// Choice shows up to three option buttons plus Cancel and returns the label the
// user pressed. AppleScript caps a dialog at three buttons.
func Choice(title, msg string, options []string) (string, bool) {
	if len(options) == 0 {
		return "", true
	}
	if !Native() {
		fmt.Printf("%s\n%s\n", title, msg)
		for i, o := range options {
			fmt.Printf("  %d) %s\n", i+1, o)
		}
		fmt.Print("输入序号：")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(options) {
			return "", true
		}
		return options[n-1], false
	}
	shown := options
	if len(shown) > 3 {
		shown = shown[:3]
	}
	var buttons []string
	for i := len(shown) - 1; i >= 0; i-- {
		buttons = append(buttons, escape(shown[i]))
	}
	buttons = append(buttons, `"取消"`)
	out, err := run(fmt.Sprintf(
		`tell application "System Events" to display dialog %s with title %s buttons {%s} default button {%s}`,
		escape(msg), escape(title), strings.Join(buttons, ", "), escape(shown[0])))
	if err != nil {
		return "", true
	}
	if idx := strings.Index(out, "button returned:"); idx >= 0 {
		return strings.TrimSpace(out[idx+len("button returned:"):]), false
	}
	return "", true
}
