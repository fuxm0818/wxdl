// Package sysinfo reads the facts about the machine that decide whether wxdl
// can capture Channels traffic: macOS version, network service, proxy state,
// competing proxies, certificate trust, ports and WeChat.
package sysinfo

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	iface := DefaultInterface()
	out, err := output("networksetup", "-listnetworkserviceorder")
	if err != nil {
		return ""
	}
	var pending string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "(") && strings.Contains(line, ") ") && !strings.HasPrefix(line, "(Hardware") {
			pending = strings.TrimSpace(strings.SplitN(line, ") ", 2)[1])
			continue
		}
		if strings.HasPrefix(line, "(Hardware Port:") && iface != "" && strings.Contains(line, "Device: "+iface+",") {
			return pending
		}
	}
	return pending
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

	ps, err := output("ps", "-axo", "pid=,comm=")
	if err == nil {
		seen := map[string]bool{}
		for _, line := range strings.Split(ps, "\n") {
			lower := strings.ToLower(line)
			for _, app := range competingApps {
				if seen[app.name] || !strings.Contains(lower, app.pattern) {
					continue
				}
				seen[app.name] = true
				out = append(out, Conflict{
					Name:     app.name,
					Detail:   "进程在运行",
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

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
