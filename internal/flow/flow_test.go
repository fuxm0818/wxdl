package flow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fuxm0818/wxdl/internal/cfg"
	"github.com/fuxm0818/wxdl/internal/events"
	"github.com/fuxm0818/wxdl/internal/server"
)

func TestUnwrapJSONHandlesBothEnvelopes(t *testing.T) {
	rest := `{"code":0,"msg":"ok","data":{"available":true}}`
	if got := unwrapJSON(rest)["available"]; got != true {
		t.Fatalf("REST envelope not unwrapped: %v", got)
	}
	tool := `{"created":true,"ids":[7,8]}`
	if got := firstTaskID(unwrapJSON(tool)); got != 7 {
		t.Fatalf("firstTaskID = %d, want 7", got)
	}
}

func TestFirstTaskIDFallsBackToTaskAndID(t *testing.T) {
	if got := firstTaskID(map[string]any{"task": map[string]any{"id": float64(42)}}); got != 42 {
		t.Fatalf("task.id = %d", got)
	}
	if got := firstTaskID(map[string]any{"id": "17"}); got != 17 {
		t.Fatalf("string id = %d", got)
	}
}

func TestBestFilePrefersVideo(t *testing.T) {
	d := &server.TaskDetail{Files: []server.File{
		{LocalPath: "/x/cover.jpg", Exists: true, Size: 10},
		{LocalPath: "/x/movie_w1244.mp4", Exists: true, Size: 900, FileType: "video"},
	}}
	if got := bestFile(d); got != "/x/movie_w1244.mp4" {
		t.Fatalf("bestFile = %s", got)
	}
	// A task without a recognisable video still yields its largest real file.
	d = &server.TaskDetail{Files: []server.File{
		{LocalPath: "/x/part", Exists: true, Size: 5},
		{LocalPath: "/x/big", Exists: true, Size: 500},
		{LocalPath: "/x/gone.mp4", Exists: false, Size: 900},
	}}
	if got := bestFile(d); got != "/x/big" {
		t.Fatalf("fallback = %s", got)
	}
}

func TestCollectVideosFindsNestedObjects(t *testing.T) {
	payload := map[string]any{
		"data": []any{
			map[string]any{"object": map[string]any{
				"objectId":      "o1",
				"objectNonceId": "n1",
				"desc":          map[string]any{"description": "第一条"},
			}},
			map[string]any{"object": map[string]any{"objectId": "o2"}},
		},
	}
	items := collectVideos(payload)
	if len(items) != 1 {
		t.Fatalf("want 1 complete video object, got %d: %+v", len(items), items)
	}
	if items[0].Selector["oid"] != "o1" || items[0].Selector["nid"] != "n1" {
		t.Fatalf("selectors = %+v", items[0].Selector)
	}
	if items[0].Label != "第一条" {
		t.Fatalf("label = %q", items[0].Label)
	}
}

func TestCollectAccountsAcceptsBothUsernameFields(t *testing.T) {
	payload := map[string]any{
		"data": map[string]any{"list": []any{
			map[string]any{"finderUsername": "abc@finder", "nickname": "甲"},
			map[string]any{"username": "def@finder"},
			map[string]any{"nickname": "不是账号"},
		}},
	}
	got := collectAccounts(payload)
	if len(got) != 2 {
		t.Fatalf("accounts = %+v", got)
	}
	if finderUsername(payload) != "abc@finder" {
		t.Fatalf("finderUsername = %s", finderUsername(payload))
	}
}

func TestFindMarkerAcceptsBothSpellings(t *testing.T) {
	if got := findMarker(map[string]any{"data": map[string]any{"lastBuffer": "abc"}}); got != "abc" {
		t.Fatalf("lastBuffer = %q", got)
	}
	if got := findMarker(map[string]any{"data": map[string]any{"lastBuff": float64(12)}}); got != "12" {
		t.Fatalf("lastBuff = %q", got)
	}
}

func TestSelectorLabel(t *testing.T) {
	if got := selectorLabel(map[string]any{"url": "https://weixin.qq.com/sph/x"}); got != "https://weixin.qq.com/sph/x" {
		t.Fatalf("url label = %q", got)
	}
	if got := selectorLabel(map[string]any{"oid": "o", "nid": "n"}); got != "oid=o nid=n eid=" {
		t.Fatalf("id label = %q", got)
	}
}

func TestTruncateCountsCharacters(t *testing.T) {
	// Cutting a Chinese title at a byte boundary produces mojibake.
	if got := truncate("如何辨别假酒？一条视频讲清楚", 6); got != "如何辨别假酒…" {
		t.Fatalf("got %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Fatalf("short string changed: %q", got)
	}
}

func TestFirstLineKeepsOneLineAndCountsCharacters(t *testing.T) {
	if got := firstLine("第一行\n第二行"); got != "第一行" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("酒", 120)
	got := firstLine(long)
	if n := len([]rune(got)); n != 91 {
		t.Fatalf("rune count = %d (%q)", n, got)
	}
}

func TestWithSpecDoesNotMutateInput(t *testing.T) {
	in := []Item{{Selector: map[string]any{"url": "https://weixin.qq.com/sph/x"}, Label: "l"}}
	out := withSpec(in, "original")
	if out[0].Selector["video_variant_spec"] != "original" {
		t.Fatalf("spec missing: %v", out[0].Selector)
	}
	if out[0].Selector["url"] != "https://weixin.qq.com/sph/x" || out[0].Label != "l" {
		t.Fatalf("selector or label lost: %+v", out[0])
	}
	if _, leaked := in[0].Selector["video_variant_spec"]; leaked {
		t.Fatal("withSpec mutated the caller's selector")
	}
}

func TestErrorCodesSurviveWrapping(t *testing.T) {
	err := fmt.Errorf("outer: %w",
		codedWrap(CodeEngineInstallFailed, "抓取引擎没能装好", "重试", errors.New("io")))
	if got := ErrorCode(err); got != CodeEngineInstallFailed {
		t.Fatalf("code = %q", got)
	}
	if got := ErrorMessage(err); got != "抓取引擎没能装好" {
		t.Fatalf("msg = %q", got)
	}
	if got := ErrorHint(err); got != "重试" {
		t.Fatalf("hint = %q", got)
	}
	if got := ErrorCode(errors.New("plain")); got != CodeInternal {
		t.Fatalf("uncoded error = %q", got)
	}
	if got := ErrorHint(errors.New("plain")); got != "" {
		t.Fatalf("uncoded hint = %q", got)
	}
}

func TestProgressKeyTracksBytesAndPhase(t *testing.T) {
	d := &server.TaskDetail{Status: server.TaskDownloading, Files: []server.File{{Downloaded: 50, Size: 100}}}
	first := progressKey(d)
	if first == "" {
		t.Fatal("known size must produce a key")
	}
	d.Files[0].Downloaded = 60
	if progressKey(d) == first {
		t.Fatal("byte change must produce a new key")
	}
	d.Files[0].Downloaded = 50
	d.Status = server.TaskMerging
	if progressKey(d) == first {
		t.Fatal("phase change must produce a new key")
	}
	if progressKey(&server.TaskDetail{}) != "" {
		t.Fatal("unknown size must not emit")
	}
}

func TestEmitItemProgressUsesByteTotals(t *testing.T) {
	var buf bytes.Buffer
	s := &Session{em: events.New(&buf, true)}
	s.emitItemProgress(2, 5, &server.TaskDetail{
		Status: server.TaskDownloading,
		Files:  []server.File{{Downloaded: 25, Size: 100}},
	})
	var ev map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &ev); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, buf.String())
	}
	if ev["t"] != "item.progress" || ev["index"] != float64(2) || ev["total"] != float64(5) {
		t.Fatalf("event = %+v", ev)
	}
	if ev["pct"] != float64(25) || ev["bytes"] != float64(25) || ev["total_bytes"] != float64(100) {
		t.Fatalf("progress = %+v", ev)
	}
	if ev["status"] != "downloading" {
		t.Fatalf("status = %v", ev["status"])
	}
}

func TestTaskStatusNamesAreStable(t *testing.T) {
	cases := map[int]string{
		server.TaskWaiting: "queued", server.TaskPreparing: "preparing",
		server.TaskDownloading: "downloading", server.TaskPaused: "paused",
		server.TaskMerging: "merging", 99: "working",
	}
	for code, want := range cases {
		if got := taskStatusName(code); got != want {
			t.Fatalf("status %d = %q, want %q", code, got, want)
		}
	}
}

func TestSpecFromPath(t *testing.T) {
	if got := specFromPath("/x/老王的派对_xWT111.mp4"); got != "xWT111" {
		t.Fatalf("spec = %q", got)
	}
	if got := specFromPath("/x/movie.mp4"); got != "" {
		t.Fatalf("spec without suffix = %q", got)
	}
	if got := specFromPath("/x/movie_"); got != "" {
		t.Fatalf("trailing underscore = %q", got)
	}
}

func TestSummarizeEmitsRunDoneWithSkipSeparate(t *testing.T) {
	var buf bytes.Buffer
	s := &Session{
		Cfg: &cfg.Config{DownloadDir: "/tmp/wxdl"},
		Opt: Options{NoDialog: true},
		em:  events.New(&buf, true),
	}
	s.summarize([]Result{
		{Item: Item{Selector: map[string]any{"url": "u1"}}, Path: "/tmp/a.mp4"},
		{Item: Item{Selector: map[string]any{"url": "u2"}}, Path: "/tmp/b.mp4", Skipped: true},
		{Item: Item{Selector: map[string]any{"url": "u3"}}, Err: coded(CodePageNotConnected, "未连接", "重开微信")},
	})
	var done map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %v (%s)", err, line)
		}
		if ev["t"] == "run.done" {
			done = ev
		}
	}
	if done == nil {
		t.Fatalf("no run.done event in %s", buf.String())
	}
	if done["ok"] != float64(1) || done["skipped"] != float64(1) || done["failed"] != float64(1) {
		t.Fatalf("run.done = %+v", done)
	}
	if done["last_file"] != "/tmp/b.mp4" {
		t.Fatalf("last_file = %v", done["last_file"])
	}
}

func TestCertAndPageLabels(t *testing.T) {
	s := &Session{Cfg: &cfg.Config{CAName: "wxdl-local-ca"}}
	if got := certTextFrom(s, false); got != "尚未生成" {
		t.Fatalf("cert = %q", got)
	}
	s.Cfg.CertFile = "/tmp/ca.pem"
	if got := certTextFrom(s, true); got != "wxdl-local-ca（已受信任）" {
		t.Fatalf("trusted cert = %q", got)
	}
	if got := pageTextFrom(true); got != "已连接" {
		t.Fatalf("page = %q", got)
	}
	if got := pageTextFrom(false); !strings.Contains(got, "未连接") {
		t.Fatalf("disconnected page = %q", got)
	}
}
