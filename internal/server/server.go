// Package server controls the capture engine process and talks to its MCP and
// REST endpoints.
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Client is a thin REST/MCP client for one engine instance.
type Client struct {
	Base   string
	hc     *http.Client
	mcpURL string
	sid    string
	mcpOn  bool
}

// New builds a client for the local API port.
func New(apiPort int) *Client {
	return &Client{
		Base: fmt.Sprintf("http://127.0.0.1:%d", apiPort),
		// Tool calls may wait for a page parse, so allow generous time.
		hc:     &http.Client{Timeout: 6 * time.Minute},
		mcpURL: fmt.Sprintf("http://127.0.0.1:%d/mcp", apiPort),
	}
}

// ChannelsAvailable reports whether a WeChat Channels page has connected to
// the engine over /ws/channels. Every Channels API call needs that page.
func (c *Client) ChannelsAvailable() bool {
	raw, err := c.get("/api/channels/status")
	if err != nil {
		return false
	}
	var e envelope
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	available, _ := e.Data["available"].(bool)
	return e.Code == 0 && available
}

// File is one download-task output file.
type File struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	Downloaded int64  `json:"downloaded"`
	FileType   string `json:"file_type"`
	LocalPath  string `json:"local_path"`
	Exists     bool   `json:"exists"`
}

// TaskDetail is the subset of /api/v1/download_task/detail wxdl polls.
type TaskDetail struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Status       int    `json:"status"`
	ErrorMessage string `json:"error_message"`
	Files        []File `json:"files"`
}

// Task detail status codes, mirroring upstream database/model/download_task.go.
const (
	TaskWaiting     = 0
	TaskPreparing   = 1
	TaskDownloading = 2
	TaskPaused      = 3
	TaskMerging     = 4
	TaskFinished    = 5
	TaskFailed      = 6
	TaskCancelled   = 7
)

// TaskDetail fetches one task, or an error when the API rejects the call.
func (c *Client) TaskDetail(id int) (*TaskDetail, error) {
	raw, err := c.get("/api/v1/download_task/detail?id=" + strconv.Itoa(id))
	if err != nil {
		return nil, err
	}
	var e struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	if e.Code != 0 {
		return nil, fmt.Errorf("download_task/detail: %s", e.Msg)
	}
	// Re-marshal through the typed struct: the payload is flat under data.
	buf, err := json.Marshal(e.Data)
	if err != nil {
		return nil, err
	}
	var d TaskDetail
	if err := json.Unmarshal(buf, &d); err != nil {
		return nil, err
	}
	d.ID = id
	return &d, nil
}

// DeleteTasks drops task records without touching any file on disk
// (delete_files defaults to false upstream). wxdl uses it to clear a stale
// record whose output file is gone, which the engine refuses to overwrite.
func (c *Client) DeleteTasks(ids ...int) error {
	if len(ids) == 0 {
		return nil
	}
	raw, err := c.CallTool("delete_download_tasks", map[string]any{
		"task_ids":     ids,
		"delete_files": false,
	})
	if err != nil {
		return err
	}
	var resp struct {
		Results []struct {
			TaskID     int    `json:"task_id"`
			Success    bool   `json:"success"`
			StatusText string `json:"status_text"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil // shape changed; the JSON-RPC error path already covers refusals
	}
	for _, r := range resp.Results {
		if !r.Success {
			return fmt.Errorf("删除任务 %d 的记录失败（状态 %s）", r.TaskID, r.StatusText)
		}
	}
	return nil
}

func (c *Client) get(path string) ([]byte, error) {
	resp, err := c.hc.Get(c.Base + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) post(path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(http.MethodPost, c.Base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Alive reports whether the engine API answers.
func (c *Client) Alive() bool {
	raw, err := c.get("/api/proxy/status")
	return err == nil && bytes.Contains(raw, []byte(`"service"`))
}

// ProxyStatus returns the decoded /api/proxy/status payload.
func (c *Client) ProxyStatus() (map[string]any, error) {
	raw, err := c.get("/api/proxy/status")
	if err != nil {
		return nil, err
	}
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("proxy/status: %s", env.Msg)
	}
	return env.Data, nil
}

// CaptureListener reports whether the engine's own capture listener is up on
// port. A bare TCP accept is not enough evidence before taking over the system
// proxy: another program on that port would swallow all of the machine's
// traffic and no Channels request would ever reach wxdl.
func (c *Client) CaptureListener(port int) bool {
	raw, err := c.get("/api/status")
	if err != nil {
		return false
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Proxy struct {
				Addr      string `json:"addr"`
				Listening bool   `json:"listening"`
			} `json:"proxy"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Code != 0 {
		return false
	}
	return env.Data.Proxy.Listening &&
		strings.HasSuffix(env.Data.Proxy.Addr, ":"+strconv.Itoa(port))
}

// StartInterceptor brings the capture service up. The engine only starts its
// HTTP API by itself: until this call nothing listens on the proxy port, so
// pointing the system proxy at it would black-hole the whole machine.
func (c *Client) StartInterceptor() error {
	raw, err := c.post("/api/service/start", map[string]any{"name": "interceptor"})
	if err != nil {
		return err
	}
	var e envelope
	if json.Unmarshal(raw, &e) != nil {
		// A payload wxdl cannot read is not a refusal: the caller checks the
		// capture listener itself before it lets the system proxy point at it.
		return nil
	}
	if e.Code != 0 {
		return fmt.Errorf("启动抓包服务失败: %s", e.Msg)
	}
	if status, ok := e.Data["interceptor"].(string); ok && status != "running" {
		return fmt.Errorf("启动抓包服务后状态异常: %v", e.Data)
	}
	return nil
}

// EnableMCP turns on the Streamable HTTP MCP endpoint.
func (c *Client) EnableMCP() error {
	_, err := c.post("/api/mcp/enable", nil)
	return err
}

type envelope struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Data map[string]any `json:"data"`
}

// GenerateLocalCA asks the engine to create a machine-specific root CA. The
// repository's bundled SunnyNet CA ships with a public private key, so wxdl
// never trusts it. Returns cert and key paths.
func (c *Client) GenerateLocalCA(name string) (certFile, keyFile string, err error) {
	raw, err := c.post("/api/proxy/certificate/generate", map[string]any{
		"name":        name,
		"install":     false,
		"restart":     true,
		"valid_years": 5,
	})
	if err != nil {
		return "", "", err
	}
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", "", err
	}
	if e.Code != 0 {
		return "", "", fmt.Errorf("生成本机证书失败: %s", e.Msg)
	}
	conf, _ := e.Data["certificate"].(map[string]any)
	configured, _ := conf["configured"].(map[string]any)
	certFile, _ = configured["file"].(string)
	keyFile, _ = configured["key"].(string)
	if certFile == "" || keyFile == "" {
		return "", "", errors.New("生成本机证书后未返回证书路径")
	}
	return certFile, keyFile, nil
}

// RestartProxy makes the engine reload the certificate files after config edits.
func (c *Client) RestartProxy() error {
	_, err := c.post("/api/proxy/restart", nil)
	return err
}

// CallTool invokes an MCP tool and returns the first text payload, which the
// engine fills with JSON.
func (c *Client) CallTool(name string, args map[string]any) (string, error) {
	if err := c.mcpInit(); err != nil {
		return "", err
	}
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	}
	raw, err := c.mcpPost(body)
	if err != nil {
		return "", err
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("解析 MCP 响应失败: %w (%s)", err, truncate(string(raw), 300))
	}
	if resp.Error != nil {
		return "", fmt.Errorf("MCP 错误: %s", resp.Error.Message)
	}
	if resp.Result == nil {
		return "", errors.New("MCP 未返回结果")
	}
	var text string
	for _, c := range resp.Result.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	if resp.Result.IsError {
		return "", fmt.Errorf("工具 %s 执行失败: %s", name, truncate(text, 500))
	}
	return text, nil
}

func (c *Client) mcpInit() error {
	if c.mcpOn {
		return nil
	}
	// The endpoint stays off until asked; an engine restart resets it, so every
	// tool call makes the attempt itself. initialize reports the real reason if
	// it is still unavailable.
	_ = c.EnableMCP()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "wxdl", "version": "1.0"},
		},
	}
	req, err := http.NewRequest(http.MethodPost, c.mcpURL, jsonReader(body))
	if err != nil {
		return err
	}
	setMCPHeaders(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("抓取引擎的 MCP 端点不可用（HTTP %d）：%s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 200))
	}
	// Stateless engines answer without a session header; the spec allows that.
	c.sid = resp.Header.Get("mcp-session-id")
	c.mcpOn = true

	note := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	req2, err := http.NewRequest(http.MethodPost, c.mcpURL, jsonReader(note))
	if err != nil {
		return nil
	}
	setMCPHeaders(req2)
	if c.sid != "" {
		req2.Header.Set("mcp-session-id", c.sid)
	}
	if resp2, err := c.hc.Do(req2); err == nil {
		_, _ = io.Copy(io.Discard, resp2.Body)
		resp2.Body.Close()
	}
	return nil
}

func setMCPHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
}

func jsonReader(v any) io.Reader {
	raw, _ := json.Marshal(v)
	return bytes.NewReader(raw)
}

func (c *Client) mcpPost(body map[string]any) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, c.mcpURL, jsonReader(body))
	if err != nil {
		return nil, err
	}
	setMCPHeaders(req)
	if c.sid != "" {
		req.Header.Set("mcp-session-id", c.sid)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if ctype := resp.Header.Get("content-type"); strings.Contains(ctype, "text/event-stream") {
		var payload []byte
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "data:") {
				payload = append(payload, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))...)
			}
		}
		if len(payload) > 0 {
			return payload, nil
		}
	}
	return raw, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Process handles the engine child process and its pid file.
type Process struct {
	bin     string
	workDir string
	cfgPath string
	runDir  string
	cmd     *exec.Cmd
}

// NewProcess prepares but does not start the engine command line.
func NewProcess(bin, workDir, cfgPath, runDir string) *Process {
	return &Process{bin: bin, workDir: workDir, cfgPath: cfgPath, runDir: runDir}
}

func (p *Process) pidFile() string { return filepath.Join(p.runDir, "engine.pid") }

// Running returns the pid of a live engine started earlier by wxdl.
func (p *Process) Running() int {
	raw, err := os.ReadFile(p.pidFile())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0
	}
	if proc, err := os.FindProcess(pid); err != nil || proc.Signal(syscall.Signal(0)) != nil {
		_ = os.Remove(p.pidFile())
		return 0
	}
	return pid
}

// Start launches the engine detached enough to survive this process, logging
// to runDir/engine.log.
//
// --hostname/--port configure the engine's *proxy* listener, not the API: the
// API address only comes from config.yaml. Passing the API port here made the
// interceptor try to bind the API's own port, so nothing listened on the proxy
// port and the machine lost its network the moment the system proxy pointed at
// it.
func (p *Process) Start(proxyPort int) error {
	if pid := p.Running(); pid > 0 {
		return nil
	}
	if err := os.MkdirAll(p.runDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(p.runDir, "engine.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(p.bin, "server", "--config", p.cfgPath, "--workdir", p.workDir,
		"--hostname", "127.0.0.1", "--port", strconv.Itoa(proxyPort))
	cmd.Dir = p.workDir
	cmd.SysProcAttr = detach()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	// Reap without blocking on the child's lifetime.
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		_ = os.Remove(p.pidFile())
	}()
	if err := os.WriteFile(p.pidFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return err
	}
	p.cmd = cmd
	return nil
}

// WaitReady blocks until the API answers or the deadline passes.
func (p *Process) WaitReady(c *Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Alive() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("抓取引擎 %s 未就绪，查看 %s", c.Base, filepath.Join(p.runDir, "engine.log"))
}

// Stop terminates the engine and clears the pid file.
func (p *Process) Stop() {
	pid := p.Running()
	if pid == 0 {
		_ = os.Remove(p.pidFile())
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGTERM)
		for i := 0; i < 20; i++ {
			time.Sleep(300 * time.Millisecond)
			if proc.Signal(syscall.Signal(0)) != nil {
				break
			}
		}
		_ = proc.Kill()
	}
	_ = os.Remove(p.pidFile())
}
