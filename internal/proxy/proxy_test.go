package proxy

import (
	"os"
	"path/filepath"
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

func TestMachineSlotsSkipsOurOwnLeftover(t *testing.T) {
	// A slot already pointing at wxdl is our leftover from an earlier run, not a
	// setting the machine had on its own. Recording it as "prior" would make
	// rollback switch the proxy back on to a port nothing listens on.
	all := []sysinfo.ProxyState{
		{Kind: "web", Enabled: true, Host: "127.0.0.1", Port: 2023},
		{Kind: "secureweb", Enabled: true, Host: "127.0.0.1", Port: 2023},
		{Kind: "socksfirewall", Enabled: true, Host: "127.0.0.1", Port: 7897},
		{Kind: "web", Enabled: false},
	}
	got := machineSlots(all, kinds, 2023)
	if len(got) != 1 || got[0].Enabled {
		t.Fatalf("snapshot = %+v, want only the disabled web slot", got)
	}
	// The user's own proxy is kept verbatim.
	theirs := []sysinfo.ProxyState{{Kind: "web", Enabled: true, Host: "127.0.0.1", Port: 7897}}
	if kept := machineSlots(theirs, kinds, 2023); len(kept) != 1 || kept[0].Port != 7897 {
		t.Fatalf("user proxy dropped from snapshot: %+v", kept)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := &State{Service: "Wi-Fi", Port: 2023, Applied: true, CertName: "wxdl test ca"}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Service != "Wi-Fi" || !got.Applied {
		t.Fatalf("loaded %+v", got)
	}
	// The rollback record is written by root-invoked helpers upstream: keep it
	// readable only by its owner.
	path, err := statePath()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("state.json mode = %v, want 600", info.Mode().Perm())
	}
	if err := st.Clear(); err != nil {
		t.Fatal(err)
	}
	if again, err := Load(); err != nil || again != nil {
		t.Fatalf("after Clear: %+v, %v", again, err)
	}
}

func TestLoadTreatsDamagedRecordAsApplied(t *testing.T) {
	// A record wxdl cannot read must not lock `wxdl undo` out of the machine: the
	// proxy may still be pointing at wxdl when nobody is listening.
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := statePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"service":"Wi-Fi",`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Load()
	if err != nil {
		t.Fatalf("damaged record should not fail Load: %v", err)
	}
	if st == nil || !st.Applied {
		t.Fatalf("got %+v", st)
	}
}
