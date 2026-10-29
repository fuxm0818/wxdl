//go:build unix

package flow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockInstance keeps one wxdl run in charge of the machine. Rolling back the
// system proxy and stopping the engine are global actions: a second run that
// finishes first would switch the proxy off, or kill the engine the first run
// is still using.
func (s *Session) lockInstance() (func(), error) {
	path := filepath.Join(s.Paths.Run, "wxdl.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("另一个 wxdl 正在下载，请等它结束（锁文件 %s）", path)
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
