package main

import (
	"flag"
	"strings"
	"testing"
)

func testSet() (*flag.FlagSet, *commonFlags, *string) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := bindCommon(fs)
	fileFlag := fs.String("file", "", "")
	fs.Usage = func() {}
	return fs, c, fileFlag
}

func TestFlagsAfterPositional(t *testing.T) {
	fs, c, _ := testSet()
	if err := parseFlags(fs, []string{"https://weixin.qq.com/sph/abc", "-o", "/tmp/视频", "--keep"}); err != nil {
		t.Fatal(err)
	}
	if c.out != "/tmp/视频" {
		t.Errorf("out = %q", c.out)
	}
	if !c.keep {
		t.Error("--keep after positional was not parsed")
	}
	if got := fsArgs(fs); len(got) != 1 || got[0] != "https://weixin.qq.com/sph/abc" {
		t.Fatalf("args = %q", got)
	}
}

func TestValueFlagKeepsFollowingPositional(t *testing.T) {
	fs, _, fileFlag := testSet()
	if err := parseFlags(fs, []string{"昵称", "--file", "links.txt", "--limit"}); err == nil {
		t.Fatal("expected an error for the undefined flag")
	}

	fs, _, fileFlag = testSet()
	if err := parseFlags(fs, []string{"昵称", "--file", "links.txt"}); err != nil {
		t.Fatal(err)
	}
	if *fileFlag != "links.txt" {
		t.Errorf("file = %q", *fileFlag)
	}
	if got := fsArgs(fs); len(got) != 1 || got[0] != "昵称" {
		t.Fatalf("args = %q", got)
	}
}

func TestBoolFlagDoesNotConsumeNextArg(t *testing.T) {
	fs, c, _ := testSet()
	if err := parseFlags(fs, []string{"--yes", "https://weixin.qq.com/sph/abc"}); err != nil {
		t.Fatal(err)
	}
	if !c.yes {
		t.Error("--yes not parsed")
	}
	if got := fsArgs(fs); len(got) != 1 {
		t.Fatalf("args = %q", got)
	}
}

func TestDoubleDashEndsFlags(t *testing.T) {
	fs, c, _ := testSet()
	if err := parseFlags(fs, []string{"--", "-o", "not-a-flag"}); err != nil {
		t.Fatal(err)
	}
	if c.keep {
		t.Error("--keep should not be set")
	}
	got := fsArgs(fs)
	if len(got) != 2 || got[0] != "-o" {
		t.Fatalf("args = %q", got)
	}
}

func TestDoubleDashKeepsArgumentOrder(t *testing.T) {
	// `wxdl user 昵称 -- 别名` must not swap the words around: they are joined
	// into one search keyword.
	fs, _, _ := testSet()
	if err := parseFlags(fs, []string{"a", "--", "b"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fsArgs(fs), " ")
	if got != "a b" {
		t.Fatalf("args = %q", got)
	}
}

func TestBareDashStaysPositional(t *testing.T) {
	fs, _, _ := testSet()
	if err := parseFlags(fs, []string{"-"}); err != nil {
		t.Fatal(err)
	}
	got := fsArgs(fs)
	if len(got) != 1 || got[0] != "-" {
		t.Fatalf("args = %q", got)
	}
}

func TestEqualsSyntax(t *testing.T) {
	fs, c, _ := testSet()
	if err := parseFlags(fs, []string{"链接", "--out=/tmp/a", "--keep=false"}); err != nil {
		t.Fatal(err)
	}
	if c.out != "/tmp/a" || c.keep {
		t.Errorf("out=%q keep=%v", c.out, c.keep)
	}
	if got := fsArgs(fs); len(got) != 1 {
		t.Fatalf("args = %q", got)
	}
}

func TestSpecFlagAfterPositional(t *testing.T) {
	fs, c, _ := testSet()
	if err := parseFlags(fs, []string{"https://weixin.qq.com/sph/abc", "--spec", "original"}); err != nil {
		t.Fatal(err)
	}
	if c.spec != "original" {
		t.Fatalf("spec = %q", c.spec)
	}
	if got := fsArgs(fs); len(got) != 1 {
		t.Fatalf("args = %q", got)
	}
	if c.options().Spec != "original" {
		t.Fatal("Options.Spec not wired")
	}
}
