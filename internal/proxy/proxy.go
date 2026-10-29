// Package proxy flips the macOS system proxy onto wxdl's capture port and back,
// remembering exactly what the machine looked like beforehand.
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuxm0818/wxdl/internal/cfg"
	"github.com/fuxm0818/wxdl/internal/elevate"
	"github.com/fuxm0818/wxdl/internal/sysinfo"
)

// State is wxdl's rollback record. Stored on disk so an interrupted run can
// still be undone by a later `wxdl undo`.
type State struct {
	Service   string               `json:"service"`
	Port      int                  `json:"port"`
	Applied   bool                 `json:"applied"`
	Prior     []sysinfo.ProxyState `json:"prior"`
	CertFile  string               `json:"cert_file"`
	CertName  string               `json:"cert_name"`
	CertAdded bool                 `json:"cert_added"`
	StartedAt string               `json:"started_at"`
}

// sentinelKind limits the slots wxdl touches to the two HTTP proxies it sets.
var kinds = []string{"web", "secureweb"}

func statePath() (string, error) {
	home, err := cfg.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "state.json"), nil
}

// Load returns the saved state, or nil when wxdl has never applied anything. A
// record it cannot read is treated as "the proxy is still ours": failing here
// would lock `wxdl undo` out while the machine still routes traffic to wxdl.
func Load() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(raw, st); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ 无法解析 %s（%v），将按“代理仍被 wxdl 占用”处理，请用 wxdl undo 恢复。\n", path, err)
		return &State{Applied: true}, nil
	}
	return st, nil
}

func (s *State) Save() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

// writeFileAtomic replaces the file through a temporary in the same directory,
// so a crash mid-write cannot leave a half-written rollback record behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wxdl-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Clear removes the rollback record once the machine is back to its old state.
func (s *State) Clear() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Needs reports whether anything still has to be undone for this state.
func (s *State) Needs() bool {
	return s != nil && (s.Applied || s.CertAdded)
}

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func setCmd(kind, service string) (string, string) {
	switch kind {
	case "secureweb":
		return "-setsecurewebproxy", "-setsecurewebproxystate"
	case "socksfirewall":
		return "-setsocksfirewallproxy", "-setsocksfirewallproxystate"
	default:
		return "-setwebproxy", "-setwebproxystate"
	}
}

// Apply points the service's HTTP and HTTPS proxies at 127.0.0.1:port and, when
// certFile is set, trusts that root CA in the system keychain. Both privileged
// actions share one authorisation prompt.
func Apply(service string, port int, certFile, certName string) (*State, error) {
	st, err := Load()
	if err != nil {
		return nil, err
	}
	if st == nil {
		st = &State{}
	}
	if st.Service != service {
		// A different network service is active now, so the saved snapshot
		// belongs to the old one and must not be reused.
		st.Service, st.Prior, st.Applied = service, nil, false
	}

	current := sysinfo.Proxies(service)
	trustNeeded := certFile != "" && !sysinfo.CertTrusted(certFile)
	needsProxy := !allOurs(port, current)
	// Snapshot the machine's own settings before the first change so Restore can
	// put them back verbatim. Slots already pointing at wxdl are excluded: they
	// were our own leftover, and restoring them would leave a dead proxy on.
	if len(st.Prior) == 0 && (trustNeeded || needsProxy) {
		st.Prior = machineSlots(current, kinds, port)
		st.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}

	var script []string
	if trustNeeded {
		// Same trust setting the established local-CA tools install: the WeChat
		// webview's verification path is not something wxdl can narrow from here.
		script = append(script,
			"security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "+shq(certFile))
	}
	if needsProxy {
		for _, kind := range kinds {
			p := findPrior(current, kind)
			if p != nil && p.Enabled && p.Host == "127.0.0.1" && p.Port == port {
				continue
			}
			set, state := setCmd(kind, service)
			script = append(script,
				fmt.Sprintf("networksetup %s %s 127.0.0.1 %d", set, shq(service), port),
				fmt.Sprintf("networksetup %s %s on", state, shq(service)))
		}
	}

	st.Port, st.CertFile, st.CertName = port, certFile, certName
	if len(script) == 0 {
		st.Applied = true
		return st, st.Save()
	}

	if err := elevate.Run(strings.Join(script, "\n"),
		"wxdl 需要管理员权限：信任本机根证书并把系统代理指向 wxdl（用完会自动恢复）"); err != nil {
		return nil, err
	}
	st.Applied = true
	st.Service = service
	st.Port = port
	st.CertFile = certFile
	st.CertName = certName
	st.CertAdded = trustNeeded
	return st, st.Save()
}

// Restore puts the proxy slots back and optionally deletes the root CA. It
// returns the state that still needs attention: a trusted CA survives rollback
// until the user asks for it to be removed.
func Restore(st *State, removeCert bool, service string) (*State, error) {
	if st == nil {
		return nil, nil
	}
	if service == "" {
		service = st.Service
	}
	var script []string
	if st.Applied && service != "" {
		for _, kind := range kinds {
			want := findPrior(st.Prior, kind)
			set, state := setCmd(kind, service)
			if want != nil && want.Enabled {
				script = append(script,
					fmt.Sprintf("networksetup %s %s %s %d", set, shq(service), shq(want.Host), want.Port),
					fmt.Sprintf("networksetup %s %s on", state, shq(service)))
			} else {
				script = append(script, fmt.Sprintf("networksetup %s %s off", state, shq(service)))
			}
		}
	}
	if removeCert {
		// By fingerprint, not by common name: the name is user-editable, so a
		// different root could share it and be deleted by mistake. `|| true`
		// keeps an already-absent certificate from aborting the proxy restore in
		// the same privileged script (elevate runs it under set -e).
		fp, err := sysinfo.CertFingerprint(st.CertFile)
		switch {
		case err == nil:
			script = append(script,
				"security delete-certificate -Z "+fp+" /Library/Keychains/System.keychain || true")
		case st.CertName != "":
			script = append(script,
				"security delete-certificate -c "+shq(st.CertName)+
					" /Library/Keychains/System.keychain || true")
		default:
			// An unidentifiable certificate must never block the proxy rollback.
			fmt.Fprintf(os.Stderr, "⚠️ 无法定位根证书（%v）：本次只恢复系统代理，请用「钥匙串访问」删除 %s。\n", err, st.CertFile)
		}
	}
	if len(script) == 0 {
		return st, nil
	}

	if err := elevate.Run(strings.Join(script, "\n"),
		"wxdl 需要管理员权限：恢复系统代理设置"); err != nil {
		return st, err
	}
	st.Applied = false
	st.Prior = nil
	if removeCert {
		st.CertAdded = false
		st.CertName = ""
	}
	if st.CertAdded {
		return st, st.Save()
	}
	return nil, st.Clear()
}

// allOurs reports whether both HTTP slots already point at our capture port.
func allOurs(port int, current []sysinfo.ProxyState) bool {
	for _, kind := range kinds {
		p := findPrior(current, kind)
		if p == nil || !p.Enabled || p.Host != "127.0.0.1" || p.Port != port {
			return false
		}
	}
	return true
}

// Active reports whether the service currently points at our capture port.
func Active(service string, port int) bool {
	for _, p := range sysinfo.Proxies(service) {
		if p.Enabled && p.Host == "127.0.0.1" && p.Port == port {
			return true
		}
	}
	return false
}

func filterKinds(all []sysinfo.ProxyState, want []string) []sysinfo.ProxyState {
	var out []sysinfo.ProxyState
	for _, p := range all {
		if contains(want, p.Kind) {
			out = append(out, p)
		}
	}
	return out
}

// machineSlots is the rollback snapshot minus the slots that already point at
// wxdl's own capture port. Those are leftovers from a previous run, not
// settings the machine had on its own, so restoring them would re-enable a proxy
// that nothing is listening on.
func machineSlots(all []sysinfo.ProxyState, want []string, port int) []sysinfo.ProxyState {
	var out []sysinfo.ProxyState
	for _, p := range filterKinds(all, want) {
		if p.Enabled && p.Host == "127.0.0.1" && p.Port == port {
			continue
		}
		out = append(out, p)
	}
	return out
}

func findPrior(list []sysinfo.ProxyState, kind string) *sysinfo.ProxyState {
	for i := range list {
		if list[i].Kind == kind {
			return &list[i]
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
