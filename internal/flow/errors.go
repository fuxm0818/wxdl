package flow

import "errors"

// Error codes are the stable contract with the GUI. Wording may change; these
// strings, and the hint attached to them, may not.
const (
	CodePageNotConnected    = "page_not_connected"
	CodeWeChatNotRunning    = "wechat_not_running"
	CodeProxyConflict       = "proxy_conflict"
	CodeAuthCancelled       = "auth_cancelled"
	CodeLockHeld            = "lock_held"
	CodeEngineMissing       = "engine_missing"
	CodeEngineInstallFailed = "engine_install_failed"
	CodePortInUse           = "port_in_use"
	CodeNetworkUnavailable  = "network_unavailable"
	CodeTimeout             = "timeout"
	CodeDiskFull            = "disk_full"
	CodeNetworkLost         = "network_lost"
	CodeDownloadFailed      = "download_failed"
	CodeDoctorFailed        = "doctor_failed"
	CodeRestoreFailed       = "restore_failed"
	CodeInternal            = "internal"
)

// Coded wraps a user-facing message and an actionable hint around an error.
type Coded struct {
	Code string
	Msg  string
	Hint string
	Err  error
}

func (e *Coded) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	if e.Msg == "" {
		return e.Err.Error()
	}
	return e.Msg + ": " + e.Err.Error()
}

func (e *Coded) Unwrap() error { return e.Err }

func coded(code, msg, hint string) error {
	return &Coded{Code: code, Msg: msg, Hint: hint}
}

func codedWrap(code, msg, hint string, err error) error {
	return &Coded{Code: code, Msg: msg, Hint: hint, Err: err}
}

func asCoded(err error) *Coded {
	var c *Coded
	if errors.As(err, &c) {
		return c
	}
	return nil
}

// ErrorCode returns the stable code a UI uses to choose copy and actions.
func ErrorCode(err error) string {
	if c := asCoded(err); c != nil {
		return c.Code
	}
	return CodeInternal
}

// ErrorMessage is the short sentence a UI shows; paths and Go detail stay in
// the wrapped error and the log.
func ErrorMessage(err error) string {
	if c := asCoded(err); c != nil && c.Msg != "" {
		return c.Msg
	}
	return err.Error()
}

// ErrorHint is the one action that usually resolves the failure, if any.
func ErrorHint(err error) string {
	if c := asCoded(err); c != nil {
		return c.Hint
	}
	return ""
}
