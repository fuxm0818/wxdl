// Package events writes wxdl's machine-readable progress as NDJSON.
//
// Human output and events share one emitter so that --json mode cannot be
// broken by a stray fmt.Print: callers either say something (which becomes a
// log event) or emit a typed event, and decoration such as carriage-return
// progress is dropped in JSON mode.
package events

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Emitter serialises all stdout writes of one wxdl process.
type Emitter struct {
	mu   sync.Mutex
	out  io.Writer
	json bool
}

// New builds an emitter. jsonMode switches stdout to one JSON object per line.
func New(out io.Writer, jsonMode bool) *Emitter {
	return &Emitter{out: out, json: jsonMode}
}

// JSON reports whether the machine-readable channel is active.
func (e *Emitter) JSON() bool { return e != nil && e.json }

// Event writes one event. It is a no-op outside JSON mode, which keeps the
// human CLI byte-for-byte identical to earlier releases.
func (e *Emitter) Event(t string, fields map[string]any) {
	if !e.JSON() {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writeLocked(t, fields)
}

// Say prints a human line, or a log event in JSON mode.
func (e *Emitter) Say(format string, a ...any) {
	if e == nil {
		return
	}
	msg := fmt.Sprintf(format, a...)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.json {
		e.writeLocked("log", map[string]any{"level": "info", "msg": msg})
		return
	}
	fmt.Fprintln(e.out, msg)
}

// Raw writes terminal decoration such as "\r" progress or screen clears. It is
// meaningless to a JSON consumer, so it disappears there.
func (e *Emitter) Raw(format string, a ...any) {
	if e == nil || e.json {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	fmt.Fprintf(e.out, format, a...)
}

// Error writes a stable-coded failure event.
func (e *Emitter) Error(code, msg, hint string) {
	e.Event("error", map[string]any{"code": code, "msg": msg, "hint": hint})
}

func (e *Emitter) writeLocked(t string, fields map[string]any) {
	// "t" comes first and the rest is sorted so a captured stream is diffable.
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k != "t" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(`{"t":`)
	writeJSON(&b, t)
	for _, k := range keys {
		v, err := json.Marshal(fields[k])
		if err != nil {
			continue
		}
		b.WriteByte(',')
		writeJSON(&b, k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteString("}\n")
	_, _ = io.WriteString(e.out, b.String())
}

func writeJSON(b *strings.Builder, s string) {
	raw, _ := json.Marshal(s)
	b.Write(raw)
}
