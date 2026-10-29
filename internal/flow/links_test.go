package flow

import (
	"reflect"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	text := "先看这个 https://weixin.qq.com/sph/AI7mqwJmdL。再看 " +
		"https://channels.weixin.qq.com/finder-pages/pages/promotion-detail?obj=1&nonce=2，" +
		"重复的 https://weixin.qq.com/sph/AI7mqwJmdL 和 https://example.com/sph/x"
	got := ExtractLinks(text)
	want := []string{
		"https://weixin.qq.com/sph/AI7mqwJmdL",
		"https://channels.weixin.qq.com/finder-pages/pages/promotion-detail?obj=1&nonce=2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractLinks = %v, want %v", got, want)
	}
}

func TestExtractLinksTrimsTrailingPunctuation(t *testing.T) {
	got := ExtractLinks("链接：https://weixin.qq.com/sph/AbC，就这样")
	if len(got) != 1 || got[0] != "https://weixin.qq.com/sph/AbC" {
		t.Fatalf("got %v", got)
	}
}

func TestIsChannelsLink(t *testing.T) {
	yes := []string{"https://weixin.qq.com/sph/x", "https://channels.weixin.qq.com/#/a", "https://finder.video.qq.com/feeds/x",
		"https://weixin.qq.com:443/sph/x", "https://WEIXIN.qq.com/sph/x"}
	for _, u := range yes {
		if !IsChannelsLink(u) {
			t.Errorf("expected %s to be a Channels link", u)
		}
	}
	no := []string{"http://localhost:2022", "https://example.com/sph/x", "weixin.qq.com/sph/x",
		"https://weixin.qq.com.evil.com/sph/x",          // suffix of our host
		"https://evil.com/?u=https://weixin.qq.com/sph", // our host only in the query
		"https://weixin.qq.com@evil.com/sph/x",          // userinfo carries our host
		"https://channels.weixin.qq.com.evil.com/",
		"ftp://weixin.qq.com/sph/x", ""}
	for _, u := range no {
		if IsChannelsLink(u) {
			t.Errorf("expected %s not to be a Channels link", u)
		}
	}
}

func TestExtractLinksTrimsBracketsFromMarkdown(t *testing.T) {
	got := ExtractLinks("[看这条](https://weixin.qq.com/sph/AbC123)")
	if len(got) != 1 || got[0] != "https://weixin.qq.com/sph/AbC123" {
		t.Fatalf("got %v", got)
	}
}

func TestLinksFromFileRejectsEmptyInput(t *testing.T) {
	if _, err := LinksFrom([]string{"随便一句话"}, ""); err == nil {
		t.Fatal("expected an error when no link is present")
	}
}
