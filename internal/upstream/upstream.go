// Package upstream provisions the capture engine wxdl drives. The heavy
// lifting (MITM, WeChat login state, AES decryption) belongs to
// ltaoo/wx_channels_download, which is MIT-licensed with a Commons Clause
// condition; wxdl therefore ships it unmodified and never sells it.
package upstream

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	Repo       = "ltaoo/wx_channels_download"
	binaryName = "wx_video_download"
	// FallbackTag is overridden at build time with -ldflags -X.
	FallbackTag = "v000000"
)

// PinnedTag is the upstream release wxdl was built against.
var PinnedTag = FallbackTag

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

// GoArchToUpstream maps GOARCH to the suffix used in upstream asset names.
func GoArchToUpstream(arch string) string {
	if arch == "arm64" {
		return "arm64"
	}
	return "x86_64"
}

// ZipName is the upstream asset name for a release and architecture.
func ZipName(tag, arch string) string {
	return fmt.Sprintf("%s_%s_darwin_%s.zip", binaryName, tag, GoArchToUpstream(arch))
}

func ChecksumName(tag string) string {
	return fmt.Sprintf("%s_%s_checksums.txt", binaryName, tag)
}

func httpGet(url string, bearer string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s -> HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// LatestTag resolves the newest upstream release tag.
func LatestTag(bearer string) (string, error) {
	raw, err := httpGet("https://api.github.com/repos/"+Repo+"/releases/latest", bearer)
	if err != nil {
		return "", err
	}
	var r release
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	if r.TagName == "" {
		return "", fmt.Errorf("上游 release 解析失败")
	}
	return r.TagName, nil
}

func assetURL(bearer, tag, name string) (string, error) {
	raw, err := httpGet("https://api.github.com/repos/"+Repo+"/releases/tags/"+tag, bearer)
	if err != nil {
		return "", err
	}
	var r release
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("上游 release %s 中没有 %s", tag, name)
}

// fetchAsset downloads one release asset. The plain download URL is tried
// first because the GitHub API answers 403 once an unauthenticated hour of
// requests is spent, which is exactly the situation on a fresh machine.
func fetchAsset(tag, name, dest, bearer string) error {
	direct := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", Repo, tag, name)
	if err := download(direct, dest); err == nil {
		return nil
	}
	url, err := assetURL(bearer, tag, name)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", name, err)
	}
	return download(url, dest)
}

// Bundled looks for the upstream zip shipped inside the wxdl release archive.
// Search order honours the install.sh layout, a share dir relative to the
// binary, and an adjacent dev checkout.
func Bundled(explicit string) (string, string) {
	arch := GoArchToUpstream(runtime.GOARCH)
	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, tagFromZipName(filepath.Base(explicit), arch)
		}
	}

	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	home, _ := os.UserHomeDir()

	for _, dir := range []string{
		filepath.Join(home, ".wxdl", "vendor"),
		filepath.Join(exeDir, "..", "share", "wxdl", "upstream"),
		filepath.Join(exeDir, "upstream"),
		exeDir,
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !matchesArch(name, arch) {
				continue
			}
			return filepath.Join(dir, name), tagFromZipName(name, arch)
		}
	}
	return "", ""
}

func matchesArch(name, arch string) bool {
	return strings.HasPrefix(name, binaryName+"_") && strings.HasSuffix(name, "_darwin_"+arch+".zip")
}

func tagFromZipName(name, arch string) string {
	tag := strings.TrimPrefix(name, binaryName+"_")
	return strings.TrimSuffix(tag, "_darwin_"+arch+".zip")
}

// checksums parses an upstream checksums.txt file.
func checksums(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			out[fields[1]] = strings.ToLower(fields[0])
		}
	}
	return out, nil
}

// verify checks a downloaded or bundled zip against upstream's checksums.txt.
// A missing checksums file is not treated as a failure: not every release
// ships one, and the fallback path must stay usable.
func verify(zipPath, zipName, sumPath string) error {
	sum, err := checksums(sumPath)
	if err != nil {
		return nil
	}
	want, ok := sum[zipName]
	if !ok {
		return fmt.Errorf("上游 checksums.txt 缺少 %s 条目", zipName)
	}
	got, err := sha256File(zipPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("上游包 sha256 校验不通过（期望 %s，实际 %s）", want, got)
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s -> HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// ExtractBinary unpacks the engine binary from an upstream zip into dest.
func ExtractBinary(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开上游压缩包失败: %w", err)
	}
	defer zr.Close()

	var target *zip.File
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if name == "._"+binaryName || strings.HasPrefix(f.Name, "__MACOSX") {
			continue
		}
		if name == binaryName && !f.FileInfo().IsDir() {
			target = f
		}
	}
	if target == nil {
		return fmt.Errorf("上游压缩包里没有 %s", binaryName)
	}
	rc, err := target.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return err
	}
	// A release zip pulled from a browser carries com.apple.quarantine, and Gatekeeper
	// then refuses to launch the engine with an error no ordinary user can act on.
	_ = exec.Command("xattr", "-d", "com.apple.quarantine", dest).Run()
	return nil
}

// Ensure returns a usable engine binary path, provisioning it when missing.
// preferOnline forces fetching the pinned/latest upstream release.
func Ensure(cacheDir, binDir, tag, bearer, zipOverride string, preferOnline bool) (string, string, error) {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", "", err
	}
	bin := filepath.Join(binDir, binaryName)
	versionFile := filepath.Join(binDir, ".upstream_tag")

	if have, err := os.ReadFile(versionFile); err == nil {
		if tag == "" || string(have) == tag {
			if st, err := os.Stat(bin); err == nil && !st.IsDir() && st.Mode().Perm()&0o100 != 0 {
				return bin, string(have), nil
			}
		}
	}

	var zipPath, usedTag string
	if !preferOnline {
		if p, t := Bundled(zipOverride); p != "" {
			zipPath, usedTag = p, t
			if tag != "" && t != tag {
				zipPath, usedTag = "", ""
			}
		}
	}

	if zipPath == "" {
		used := tag
		if used == "" {
			used = PinnedTag
		}
		if used == "" || used == FallbackTag {
			latest, err := LatestTag(bearer)
			if err != nil {
				return "", "", fmt.Errorf("无法确定上游版本（离线且未内置包）: %w", err)
			}
			used = latest
		}
		zipName := ZipName(used, runtime.GOARCH)
		sumName := ChecksumName(used)
		dlDir := filepath.Join(cacheDir, used)
		if err := os.MkdirAll(dlDir, 0o755); err != nil {
			return "", "", err
		}
		zipPath = filepath.Join(dlDir, zipName)
		sumPath := filepath.Join(dlDir, sumName)

		if _, err := os.Stat(zipPath); err != nil {
			if err := fetchAsset(used, zipName, zipPath, bearer); err != nil {
				return "", "", err
			}
		}
		if _, err := os.Stat(sumPath); err != nil {
			_ = fetchAsset(used, sumName, sumPath, bearer)
		}
		// A truncated download still verifies wrong, so re-fetch once instead of
		// asking the user to clean the cache by hand.
		if err := verify(zipPath, zipName, sumPath); err != nil {
			_ = os.Remove(zipPath)
			if err := fetchAsset(used, zipName, zipPath, bearer); err != nil {
				return "", "", err
			}
			if err := verify(zipPath, zipName, sumPath); err != nil {
				return "", "", err
			}
		}
		usedTag = used
	} else if zipOverride == "" {
		// Verify a bundled zip against a sibling checksums file when present.
		sumPath := filepath.Join(filepath.Dir(zipPath), ChecksumName(usedTag))
		if err := verify(zipPath, filepath.Base(zipPath), sumPath); err != nil {
			return "", "", err
		}
	}

	tmp := bin + ".new"
	if err := ExtractBinary(zipPath, tmp); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, bin); err != nil {
		return "", "", err
	}
	if usedTag != "" {
		_ = os.WriteFile(versionFile, []byte(usedTag), 0o644)
	}
	return bin, usedTag, nil
}

// Version asks the engine binary for its own version string.
func Version(bin string) string {
	cmd := exec.Command(bin, "version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ConfigYAML is the engine configuration wxdl writes into its work dir.
// system=false and skipInstallRootCert=true keep the engine from touching the
// machine on its own: wxdl owns those privileged steps.
const ConfigYAML = `workdir: "%[1]s"

download:
  filenameTemplate: "{{filename}}_{{spec}}"
  dir: "%[2]s"
  playDoneAudio: false

api:
  protocol: "http"
  hostname: "127.0.0.1"
  port: %[3]d

mcp:
  enabled: true

proxy:
  enabled: true
  system: false
  hostname: "127.0.0.1"
  port: %[4]d
  tun: false
  skipInstallRootCert: true

channels:
  download:
    defaultHighest: false
    frontend: false
    cover: false

debug:
  error: false
  echolog: false
`

// WriteConfig renders the engine config for this machine.
func WriteConfig(workDir, downloadDir, certFile, certKey, caName string, apiPort, proxyPort int) (string, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return "", err
	}
	body := fmt.Sprintf(ConfigYAML, workDir, downloadDir, apiPort, proxyPort)
	if certFile != "" && certKey != "" {
		body += fmt.Sprintf("\ncert:\n  file: %q\n  key: %q\n  name: %q\n", certFile, certKey, caName)
	}
	path := filepath.Join(workDir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
