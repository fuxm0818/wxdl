package proxy

import (
	"testing"

	"github.com/fuxm0818/wxdl/internal/sysinfo"
)

func TestShqProtectsServiceNames(t *testing.T) {
	cases := map[string]string{
		"Wi-Fi":         `'Wi-Fi'`,
		`O'Brien 5G`:    `'O'\''Brien 5G'`,
		"":              `''`,
		`a"b; rm -rf /`: `'a"b; rm -rf /'`,
		"$(whoami)":     `'$(whoami)'`,
	}
	for in, want := range cases {
		if got := shq(in); got != want {
			t.Errorf("shq(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSetCmdCoversBothHTTPSlots(t *testing.T) {
	for kind, want := range map[string]string{
		"web":       "-setwebproxy",
		"secureweb": "-setsecurewebproxy",
	} {
		set, state := setCmd(kind, "Wi-Fi")
		if set != want || state == "" {
			t.Errorf("setCmd(%q) = %q, %q", kind, set, state)
		}
	}
}

func TestAllOursRequiresBothSlots(t *testing.T) {
	off := sysinfo.ProxyState{Kind: "web", Enabled: false}
	on := sysinfo.ProxyState{Kind: "secureweb", Enabled: true, Host: "127.0.0.1", Port: 2023}
	if allOurs(2023, []sysinfo.ProxyState{off, on}) {
		t.Error("a disabled web slot must not count as ours")
	}
	both := []sysinfo.ProxyState{
		{Kind: "web", Enabled: true, Host: "127.0.0.1", Port: 2023},
		{Kind: "secureweb", Enabled: true, Host: "127.0.0.1", Port: 2023},
	}
	if !allOurs(2023, both) {
		t.Error("both slots pointing at us should count as ours")
	}
	if allOurs(2023, []sysinfo.ProxyState{
		{Kind: "web", Enabled: true, Host: "127.0.0.1", Port: 7897},
		{Kind: "secureweb", Enabled: true, Host: "127.0.0.1", Port: 2023},
	}) {
		t.Error("another proxy on the web slot must not count as ours")
	}
}

func TestFindPriorReturnsNilForUnknownKind(t *testing.T) {
	if findPrior([]sysinfo.ProxyState{{Kind: "web"}}, "socksfirewall") != nil {
		t.Fatal("expected no match")
	}
}
