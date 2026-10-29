package flow

import (
	"testing"

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
