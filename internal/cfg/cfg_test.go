package cfg

import (
	"os"
	"path/filepath"
	"testing"
)

func layout(root string) Paths {
	home := filepath.Join(root, ".wxdl")
	return Paths{
		Home:    home,
		Bin:     filepath.Join(home, "bin"),
		Cache:   filepath.Join(home, "cache"),
		Work:    filepath.Join(home, "work"),
		Run:     filepath.Join(home, "run"),
		CertDir: filepath.Join(home, "certs"),
	}
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestEnsureKeepsCertsPrivate(t *testing.T) {
	p := layout(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := perm(t, p.CertDir); got != 0o700 {
		t.Errorf("CertDir mode = %o, want 700", got)
	}
	if got := perm(t, p.Bin); got != 0o755 {
		t.Errorf("Bin mode = %o, want 755", got)
	}
}

func TestEnsureTightensExistingCertsDir(t *testing.T) {
	p := layout(t.TempDir())
	if err := os.MkdirAll(p.CertDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := perm(t, p.CertDir); got != 0o700 {
		t.Errorf("CertDir mode = %o, want 700", got)
	}
}

// The engine writes its private key under <workdir>/certs, so Layout must name
// that directory; a path of its own would leave the key in a 0755 folder.
func TestLayoutCertsDirIsTheEngines(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	p, err := Layout()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wxdl", "work", "certs"); p.CertDir != want {
		t.Fatalf("CertDir = %q, want %q", p.CertDir, want)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, p.CertDir); got != 0o700 {
		t.Errorf("CertDir mode = %o, want 700", got)
	}
	if got := perm(t, filepath.Join(root, ".wxdl", "work")); got != 0o755 {
		t.Errorf("Work mode = %o, want 755", got)
	}
}
