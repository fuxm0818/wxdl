// Package elevate runs privileged shell snippets on macOS without ever
// handling the user's password.
package elevate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrCancelled means the user dismissed the password dialog.
var ErrCancelled = errors.New("用户取消了授权")

// ErrNoGUI means this process has no window server session, so the native
// password panel cannot be shown (for example when wxdl is invoked over SSH).
var ErrNoGUI = errors.New("当前会话无法弹出授权窗口")

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
}

// Run executes script as root. It prefers the native macOS password panel and
// degrades to sudo on a controlling terminal when no panel is available.
func Run(script, prompt string) error {
	dir, err := os.MkdirTemp("", "wxdl-elevate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	file := filepath.Join(dir, "step.sh")
	body := "#!/bin/bash\nset -e\n" + script + "\n"
	if err := os.WriteFile(file, []byte(body), 0o700); err != nil {
		return err
	}

	asErr := runAppleScript(file, prompt)
	if asErr == nil {
		return nil
	}
	if errors.Is(asErr, ErrCancelled) {
		return asErr
	}
	if !errors.Is(asErr, ErrNoGUI) {
		// The panel appeared and the command itself failed; surface the reason.
		return asErr
	}

	// Headless session: sudo can prompt on the attached terminal instead.
	if _, err := os.Stat("/dev/tty"); err != nil {
		return asErr
	}
	fmt.Println("没有图形会话，改用终端 sudo 授权：")
	cmd := exec.Command("sudo", "-n", "/bin/bash", file)
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("sudo", "/bin/bash", file)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sudo 执行失败: %w", err)
		}
	}
	return nil
}

func runAppleScript(file, prompt string) error {
	script := fmt.Sprintf(`do shell script "/bin/bash %s" with administrator privileges with prompt "%s"`,
		escape(file), escape(prompt))
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.Output()
	if err == nil {
		return nil
	}

	stderr := ""
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = string(ee.Stderr)
	}
	combined := stderr + " " + string(out) + " " + err.Error()

	switch {
	case strings.Contains(combined, "-128"), strings.Contains(combined, "User canceled"):
		return ErrCancelled
	case strings.Contains(combined, "no user interaction was possible"),
		strings.Contains(combined, "not authorized to send Apple events"),
		strings.Contains(combined, "Apple Event not found"):
		return ErrNoGUI
	default:
		return fmt.Errorf("提权命令失败: %s", strings.TrimSpace(combined))
	}
}
