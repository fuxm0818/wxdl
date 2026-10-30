package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEventWritesOneJSONObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	em := New(&buf, true)
	em.Event("item.done", map[string]any{"path": "/tmp/a.mp4", "bytes": int64(7), "index": 1})
	em.Event("run.done", map[string]any{"ok": 1})

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], `{"t":"item.done",`) {
		t.Fatalf("t must come first: %s", lines[0])
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, lines[0])
	}
	if got["path"] != "/tmp/a.mp4" || got["bytes"] != float64(7) || got["index"] != float64(1) {
		t.Fatalf("fields round-trip: %+v", got)
	}
}

func TestEventIsSilentWithoutJSONMode(t *testing.T) {
	var buf bytes.Buffer
	em := New(&buf, false)
	em.Event("log", map[string]any{"msg": "x"})
	if buf.Len() != 0 {
		t.Fatalf("non-JSON emitter wrote an event: %q", buf.String())
	}
	em.Raw("raw")
	if got := buf.String(); got != "raw" {
		t.Fatalf("raw human decoration must still print: %q", got)
	}
}

func TestSayStaysHumanWithoutJSON(t *testing.T) {
	var buf bytes.Buffer
	em := New(&buf, false)
	em.Say("待下载 %d 个视频。", 3)
	em.Raw("\r\033[K")
	if got := buf.String(); got != "待下载 3 个视频。\n\r\033[K" {
		t.Fatalf("human output changed: %q", got)
	}
}

func TestSayBecomesLogEventInJSONMode(t *testing.T) {
	var buf bytes.Buffer
	em := New(&buf, true)
	em.Say("共 %d 个视频", 2)
	em.Raw("\r16%%")

	line := strings.TrimSuffix(buf.String(), "\n")
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, line)
	}
	if got["t"] != "log" || got["level"] != "info" || got["msg"] != "共 2 个视频" {
		t.Fatalf("log event = %+v", got)
	}
	if strings.Contains(line, "16") {
		t.Fatalf("raw decoration leaked into JSON: %s", line)
	}
}

func TestErrorCarriesCodeAndHint(t *testing.T) {
	var buf bytes.Buffer
	em := New(&buf, true)
	em.Error("lock_held", "另一个 wxdl 正在下载", "等它结束")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["t"] != "error" || got["code"] != "lock_held" || got["hint"] != "等它结束" {
		t.Fatalf("error event = %+v", got)
	}
}

func TestNilEmitterIsSafe(t *testing.T) {
	var em *Emitter
	em.Event("x", nil)
	em.Say("x")
	em.Raw("x")
	if em.JSON() {
		t.Fatal("nil emitter must not be JSON mode")
	}
}
