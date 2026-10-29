// Package sysinfo reads the facts about the machine that decide whether wxdl
// can capture Channels traffic: macOS version, network service, proxy state,
// competing proxies, certificate trust, ports and WeChat.
package sysinfo

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ProxyState is one macOS proxy slot on a network service.
type ProxyState struct {
	Kind    string // web | secureweb | socksfirewall
	Enabled bool
	Host    string
	Port    int
}

// Conflict is something already occupying the proxy path wxdl needs.
type Conflict struct {
	Name   string
	Detail string
	// AppName is the macOS app to quit when Quitable is set.
	AppName string
	// Quitable means wxdl knows how to ask the app to exit.
	Quitable bool
}

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// MacOSMajor returns the major version number, or 0 when it cannot be parsed.
func MacOSMajor() int {
	v, err := output("sw_vers", "-productVersion")
	if err != nil {
		return 0
	}
	parts := strings.Split(v, ".")
	if parts[0] == "10" && len(parts) > 1 {
		// 10.x era: the meaningful number is the second component.
		n, _ := strconv.Atoi(parts[1])
		return n
	}
	n, _ := strconv.Atoi(parts[0])
	return n
}

// MacOSVersion returns the full product version string.
func MacOSVersion() string {
	v, _ := output("sw_vers", "-productVersion")
	return v
}

// Arch is the Go architecture of this binary (arm64 or amd64).
func Arch() string { return runtime.GOARCH }

// DefaultInterface returns the primary route interface, e.g. en0.
func DefaultInterface() string {
	out, err := output("route", "-n", "get", "default")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "interface:" {
			return fields[1]
		}
	}
	return ""
}

// PrimaryService maps the active interface to its networksetup service name,
// e.g. en0 -> "Wi-Fi". Falls back to the first enabled service.
func PrimaryService() string {
	return parseServiceOrder(mustOutput("networksetup", "-listnetworkserviceorder"), DefaultInterface())
}

func mustOutput(name string, args ...string) string {
	out, err := output(name, args...)
	if err != nil {
		return ""
	}
	return out
}

// parseServiceOrder reads the -listnetworkserviceorder listing. Hardware lines
// follow their numbered service line, so the pending name is the answer; the
// device is written as "Device: en0)" with no trailing comma.
func parseServiceOrder(listing, iface string) string {
	var pending, firstEnabled string
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "(Hardware Port:"):
			if iface != "" && strings.Contains(line, "Device: "+iface+")") {
				return pending
			}
		case strings.HasPrefix(line, "("):
			parts := strings.SplitN(line, ") ", 2)
			if len(parts) != 2 {
				continue
			}
			name := strings.TrimSpace(parts[1])
			// A leading asterisk marks a disabled service; networksetup only
			// accepts the bare name, so it never travels into the command.
			disabled := strings.HasPrefix(name, "*")
			pending = strings.TrimSpace(strings.TrimPrefix(name, "*"))
			if firstEnabled == "" && !disabled {
				firstEnabled = pending
			}
		}
	}
	return firstEnabled
}

// Proxies returns the three HTTP-ish proxy slots for a service.
func Proxies(service string) []ProxyState {
	var out []ProxyState
	for _, kind := range []string{"web", "secureweb", "socksfirewall"} {
		joined, err := output("networksetup", "-get"+kind+"proxy", service)
		if err != nil {
			continue
		}
		st := ProxyState{Kind: kind}
		for _, line := range strings.Split(joined, "\n") {
			key, val, ok := strings.Cut(strings.TrimSpace(line), ": ")
			if !ok {
				continue
			}
			switch key {
			case "Enabled":
				st.Enabled = val == "Yes"
			case "Server":
				st.Host = val
			case "Port":
				st.Port, _ = strconv.Atoi(val)
			}
		}
		out = append(out, st)
	}
	return out
}

var competingApps = []struct {
	pattern  string
	name     string
	appName  string
	quitable bool
}{
	{"clash", "Clash（Verge / mihomo）", "Clash Verge", true},
	{"surge", "Surge", "Surge", true},
	{"loon", "Loon", "Loon", true},
	{"stash", "Stash", "Stash", true},
	{"sing-box", "sing-box", "", false},
	{"v2ray", "v2ray", "", false},
	{"xray", "xray", "", false},
	{"trojan", "trojan", "", false},
	{"charles", "Charles 抓包工具", "Charles", true},
	{"mitmproxy", "mitmproxy", "", false},
	{"proxifier", "Proxifier", "Proxifier", true},
}

// Conflicts lists running proxies plus any non-wxdl proxy already configured
// on the service and any fake-IP TUN interface.
func Conflicts(service string, ourPort int) []Conflict {
	var out []Conflict

	for _, st := range Proxies(service) {
		if st.Enabled && st.Port != ourPort {
			out = append(out, Conflict{
				Name:   "系统代理已被占用",
				Detail: st.Kind + " -> " + st.Host + ":" + strconv.Itoa(st.Port),
			})
			break
		}
	}

	ps, err := output("ps", "-axo", "pid=,uid=,comm=")
	if err == nil {
		seen := map[string]bool{}
		self := strconv.Itoa(os.Getuid())
		for _, line := range strings.Split(ps, "\n") {
			// A root launchd helper (Clash Verge installs one) keeps running
			// after the app is quit and cannot steal WeChat's traffic; a
			// root-run core would still surface through TunActive below.
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[1] != self {
				continue
			}
			lower := strings.ToLower(line)
			for _, app := range competingApps {
				if seen[app.name] || !strings.Contains(lower, app.pattern) {
					continue
				}
				seen[app.name] = true
				out = append(out, Conflict{
					Name:     app.name,
					Detail:   "本机代理进程正在运行",
					Quitable: app.quitable && app.appName != "",
					AppName:  app.appName,
				})
			}
		}
	}

	if TunActive() {
		out = append(out, Conflict{Name: "TUN 虚拟网卡", Detail: "检测到 fake-IP 路由（198.18.0.x），会在网络层抢走流量"})
	}
	return out
}

// TunActive reports whether a fake-IP style TUN interface is up.
func TunActive() bool {
	out, err := output("ifconfig", "-a")
	if err != nil {
		return false
	}
	return strings.Contains(out, "198.18.0.")
}

// PortFree checks whether a TCP port can be bound on loopback.
func PortFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// PortOpen reports whether something is already accepting connections on the
// loopback port, which is the check that must pass before the system proxy is
// pointed at it.
func PortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// WeChatInstalled checks the standard app location.
func WeChatInstalled() bool {
	for _, p := range []string{"/Applications/WeChat.app", filepath.Join(homeDir(), "Applications", "WeChat.app")} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// WeChatRunning checks the main process.
func WeChatRunning() bool {
	_, err := output("pgrep", "-x", "WeChat")
	return err == nil
}

// FFprobeAvailable checks for optional media verification.
func FFprobeAvailable() bool {
	_, err := exec.LookPath("ffprobe")
	return err == nil
}

// CertTrusted verifies a root CA is trusted for SSL in the system store.
// The CT warnings macOS prints for local CAs are irrelevant and ignored.
func CertTrusted(pemPath string) bool {
	if pemPath == "" {
		return false
	}
	cmd := exec.Command("security", "verify-cert", "-c", pemPath, "-p", "ssl", "-l", "-L", "-q")
	return cmd.Run() == nil
}

// CertPresent checks whether the certificate exists in the system keychain.
func CertPresent(commonName string) bool {
	_, err := output("security", "find-certificate", "-c", commonName, "/Library/Keychains/System.keychain")
	return err == nil
}

// CertFingerprint returns the SHA-1 fingerprint keychain tools identify a
// certificate by, e.g. "AABBCC...". wxdl deletes its root CA by fingerprint so a
// different certificate that happens to share the common name is never touched.
func CertFingerprint(pemPath string) (string, error) {
	if pemPath == "" {
		return "", errors.New("没有证书文件")
	}
	if _, err := os.Stat(pemPath); err != nil {
		return "", err
	}
	out, err := output("openssl", "x509", "-noout", "-fingerprint", "-sha1", "-in", pemPath)
	if err != nil {
		return "", err
	}
	return parseFingerprint(out)
}

// parseFingerprint turns openssl's "SHA1 Fingerprint=AA:BB:..." into the bare
// upper-case hex that `security delete-certificate -Z` expects.
func parseFingerprint(out string) (string, error) {
	if i := strings.Index(out, "="); i >= 0 {
		out = out[i+1:]
	}
	if f := strings.Fields(out); len(f) > 0 {
		out = f[0]
	}
	hex := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(out), ":", ""))
	if len(hex) != 40 {
		return "", fmt.Errorf("无法解析证书指纹: %s", out)
	}
	return hex, nil
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
