package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fuxm0818/wxdl/internal/dialog"
)

// Item is one download request: the arguments for download_wxchannels_video
// plus a label for progress output.
type Item struct {
	Selector map[string]any
	Label    string
}

// Result is the outcome of one item.
type Result struct {
	Item Item
	Path string
	Err  error
}

// Batch downloads items one after another. The engine runs the actual transfer,
// wxdl only paces the requests because every metadata call is served by a
// single webview page inside WeChat.
func (s *Session) Batch(items []Item) []Result {
	var out []Result
	if len(items) == 0 {
		s.Say("没有需要下载的内容。")
		return out
	}
	if s.Opt.Spec != "" {
		items = withSpec(items, s.Opt.Spec)
		s.Say("视频规格：%s", s.Opt.Spec)
	}
	s.Say("共 %d 个视频，开始批量下载（下载目录：%s）", len(items), s.Cfg.DownloadDir)
	for i, it := range items {
		label := it.Label
		if label == "" {
			label = selectorLabel(it.Selector)
		}
		s.Say("[%d/%d] %s", i+1, len(items), firstLine(label))
		if !s.cli.ChannelsAvailable() {
			if err := s.reconnect(); err != nil {
				out = append(out, Result{Item: it, Err: err})
				s.Say("  跳过：视频号页面未连接")
				continue
			}
		}
		path, err := s.downloadOne(it.Selector)
		if err != nil {
			// A dropped page or a stale session usually works on the second try.
			time.Sleep(2 * time.Second)
			path, err = s.downloadOne(it.Selector)
		}
		switch {
		case err != nil:
			out = append(out, Result{Item: it, Err: err})
			s.Say("  失败：%v", err)
		case path == "":
			out = append(out, Result{Item: it, Err: fmt.Errorf("任务完成但没有输出文件路径")})
			s.Say("  失败：没有输出文件路径")
		default:
			out = append(out, Result{Item: it, Path: path})
			s.reportFile(path)
		}
		time.Sleep(time.Second)
	}
	s.summarize(out)
	return out
}

func (s *Session) summarize(results []Result) {
	var ok, failed int
	var lastFile string
	for _, r := range results {
		if r.Err != nil {
			failed++
			continue
		}
		ok++
		lastFile = r.Path
	}
	fmt.Print("\r\033[K")
	s.Say("完成：%d 成功，%d 失败。", ok, failed)
	if failed > 0 {
		for _, r := range results {
			if r.Err != nil {
				s.Say("  失败：%s -> %v", firstLine(selectorLabel(r.Item.Selector)), r.Err)
			}
		}
	}
	if ok > 0 {
		if !s.Opt.NoDialog && lastFile != "" {
			_ = run("open", "-R", lastFile)
		}
		s.Say("文件都在这里：%s", s.Cfg.DownloadDir)
	}
}

// reportFile prints what one finished download weighs and how long it is.
func (s *Session) reportFile(path string) {
	st, err := os.Stat(path)
	if err != nil {
		s.Say("  已保存：%s", path)
		return
	}
	line := fmt.Sprintf("  已保存：%s (%.1f MB)", path, float64(st.Size())/1024/1024)
	if d := ffprobeDuration(path); d != "" {
		line += " 时长 " + d
	}
	s.Say("%s", line)
}

// reconnect waits for the Channels page to come back without a long interview.
func (s *Session) reconnect() error {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if s.cli.ChannelsAvailable() {
			return nil
		}
		time.Sleep(time.Second)
	}
	if !s.Opt.NoDialog {
		notify("需要视频号页面", "请在微信里重新打开视频号页面，wxdl 会继续等待。")
	}
	deadline = time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if s.cli.ChannelsAvailable() {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("视频号页面未连接")
}

// callToolJSON invokes an MCP tool and decodes its JSON payload.
func (s *Session) callToolJSON(tool string, args map[string]any) (map[string]any, error) {
	text, err := s.cli.CallTool(tool, args)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("解析 %s 返回的 JSON 失败: %w", tool, err)
	}
	return raw, nil
}

// VideosOfAccount pages through an account's feed list.
func (s *Session) VideosOfAccount(username string, limit, maxPages int) ([]Item, error) {
	var items []Item
	seen := map[string]bool{}
	marker := ""
	for page := 0; page < maxPages && len(items) < limit; page++ {
		if !s.cli.ChannelsAvailable() {
			if err := s.reconnect(); err != nil {
				return items, err
			}
		}
		resp, err := s.callToolJSON("get_wxchannels_account_videos", map[string]any{
			"username": username, "next_marker": marker,
		})
		if err != nil {
			return items, err
		}
		for _, v := range collectVideos(resp) {
			key := v.Selector["nid"]
			if seen[fmt.Sprint(key)] || len(items) >= limit {
				continue
			}
			seen[fmt.Sprint(key)] = true
			items = append(items, v)
		}
		next := findMarker(resp)
		if next == "" || next == marker {
			break
		}
		marker = next
		s.Say("已取到第 %d 页，累计 %d 个视频 ...", page+1, len(items))
		time.Sleep(1500 * time.Millisecond)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("这个账号没有可下载的视频")
	}
	return items, nil
}

// ResolveAccount turns a nickname, a username or a share link into a username
// ending with @finder, which is what the feed list needs.
func (s *Session) ResolveAccount(input string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("需要账号昵称、username 或该账号任意一条视频链接")
	}
	if strings.HasSuffix(input, "@finder") {
		return input, input, nil
	}
	if IsChannelsLink(input) {
		resp, err := s.callToolJSON("get_wxchannels_video_profile", map[string]any{"url": input})
		if err != nil {
			return "", "", err
		}
		if name := finderUsername(resp); name != "" {
			return name, name, nil
		}
		return "", "", fmt.Errorf("没有从这条链接里解析出账号")
	}

	resp, err := s.callToolJSON("search_wxchannels_accounts", map[string]any{"keyword": input})
	if err != nil {
		return "", "", err
	}
	cands := collectAccounts(resp)
	if len(cands) == 0 {
		return "", "", fmt.Errorf("没有找到视频号账号：%s", input)
	}
	for _, c := range cands {
		if c["nickname"] == input {
			return asString(c["username"]), input, nil
		}
	}
	if len(cands) == 1 {
		return asString(cands[0]["username"]), asString(cands[0]["nickname"]), nil
	}
	if s.Opt.NoDialog {
		return "", "", fmt.Errorf("找到多个账号，请用精确昵称重试；候选：%s", candidateLabels(cands))
	}
	choice, cancelled := chooseAccount(cands)
	if cancelled {
		return "", "", fmt.Errorf("已取消")
	}
	return choice, "", nil
}

// finish ends the run. A failed rollback is never hidden: the user has to know
// the system proxy still points at wxdl.
func (s *Session) finish() {
	if err := s.Finish(); err != nil {
		s.Say("恢复系统代理时出错，请手动检查「系统设置-网络-代理」：%v", err)
	}
}

// Run prepares the machine, downloads everything and puts the machine back.
func (s *Session) Run(items []Item) error {
	if len(items) == 0 {
		return fmt.Errorf("没有需要下载的内容")
	}
	if err := s.Prepare(); err != nil {
		s.Abort()
		return err
	}
	results := s.Batch(items)
	s.finish()
	ok := 0
	for _, r := range results {
		if r.Err == nil {
			ok++
		}
	}
	if ok == 0 {
		return fmt.Errorf("%d 个视频全部失败，见上面的失败原因", len(results))
	}
	return nil
}

// RunLinks downloads an explicit list of share links.
func (s *Session) RunLinks(links []string) error { return s.Run(ItemsFromLinks(links)) }

// RunAccount downloads an account's recent videos: limit items over at most
// maxPages list pages.
func (s *Session) RunAccount(input string, limit, maxPages int) error {
	if err := s.Prepare(); err != nil {
		s.Abort()
		return err
	}
	username, nickname, err := s.ResolveAccount(input)
	if err != nil {
		s.finish()
		return err
	}
	s.Say("账号：%s (%s)", nickname, username)
	items, err := s.VideosOfAccount(username, limit, maxPages)
	if err != nil {
		s.finish()
		return err
	}
	if !s.Opt.NoDialog {
		ok, cancelled := dialog.Confirm("确认批量下载",
			fmt.Sprintf("准备下载该账号最近 %d 个视频，是否继续？", len(items)),
			"开始下载", "取消")
		if cancelled || !ok {
			s.finish()
			return fmt.Errorf("已取消")
		}
	}
	return s.runPrepared(items)
}

func (s *Session) runPrepared(items []Item) error {
	results := s.Batch(items)
	s.finish()
	if allFailed(results) {
		return fmt.Errorf("%d 个视频全部失败，见上面的失败原因", len(results))
	}
	return nil
}

func allFailed(results []Result) bool {
	if len(results) == 0 {
		return true
	}
	for _, r := range results {
		if r.Err == nil {
			return false
		}
	}
	return true
}

// ItemsFromLinks maps share links to download selectors.
func ItemsFromLinks(links []string) []Item {
	out := make([]Item, 0, len(links))
	for _, l := range links {
		out = append(out, Item{Selector: map[string]any{"url": l}, Label: l})
	}
	return out
}

// withSpec stamps every selector with the requested variant. The engine reads
// video_variant_spec=original as "give me the untouched source file".
func withSpec(items []Item, spec string) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		sel := make(map[string]any, len(it.Selector)+1)
		for k, v := range it.Selector {
			sel[k] = v
		}
		sel["video_variant_spec"] = spec
		out = append(out, Item{Selector: sel, Label: it.Label})
	}
	return out
}

// collectVideos walks any response shape looking for video objects.
func collectVideos(node any) []Item {
	var out []Item
	switch v := node.(type) {
	case map[string]any:
		oid, nid := asString(v["objectId"]), asString(v["objectNonceId"])
		if oid != "" && nid != "" {
			label := videoLabel(v)
			if label == "" {
				label = nid
			}
			out = append(out, Item{
				Selector: map[string]any{"oid": oid, "nid": nid},
				Label:    label,
			})
		}
		for _, child := range v {
			out = append(out, collectVideos(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, collectVideos(child)...)
		}
	}
	return out
}

// collectAccounts walks a search response looking for account cards.
func collectAccounts(node any) []map[string]any {
	var out []map[string]any
	switch v := node.(type) {
	case map[string]any:
		name, nick := asString(v["username"]), asString(v["nickname"])
		if name == "" {
			name = asString(v["finderUsername"])
		}
		if strings.HasSuffix(name, "@finder") {
			out = append(out, map[string]any{"username": name, "nickname": nick})
			return out
		}
		for _, child := range v {
			out = append(out, collectAccounts(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, collectAccounts(child)...)
		}
	}
	return out
}

func finderUsername(node any) string {
	for _, m := range collectAccounts(node) {
		if u := asString(m["username"]); u != "" {
			return u
		}
	}
	return ""
}

// findString searches the tree for the first non-empty string under key.
func findString(node any, key string) string {
	switch v := node.(type) {
	case map[string]any:
		if s, ok := v[key].(string); ok && s != "" {
			return s
		}
		for _, child := range v {
			if s := findString(child, key); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range v {
			if s := findString(child, key); s != "" {
				return s
			}
		}
	}
	return ""
}

// findMarker reads the paging cursor. Upstream names it lastBuffer for the feed
// list and lastBuff for search, and WeChat may return it as a number.
func findMarker(node any) string {
	for _, key := range []string{"lastBuffer", "lastBuff"} {
		if s := findString(node, key); s != "" {
			return s
		}
		if n := findNumber(node, key); n != "" {
			return n
		}
	}
	return ""
}

func findNumber(node any, key string) string {
	switch v := node.(type) {
	case map[string]any:
		if n, ok := v[key].(float64); ok && n > 0 {
			return strconv.FormatFloat(n, 'f', -1, 64)
		}
		for _, child := range v {
			if s := findNumber(child, key); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range v {
			if s := findNumber(child, key); s != "" {
				return s
			}
		}
	}
	return ""
}

func videoLabel(object map[string]any) string {
	for _, key := range []string{"desc", "description", "title", "content"} {
		if s := asString(object[key]); s != "" {
			return s
		}
		if m, ok := object[key].(map[string]any); ok {
			if s := findString(m, "desc"); s != "" {
				return s
			}
			for _, key2 := range []string{"description", "title", "content"} {
				if s := asString(m[key2]); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func candidateLabels(list []map[string]any) string {
	var names []string
	for _, c := range list {
		names = append(names, asString(c["nickname"]))
	}
	return strings.Join(names, "、")
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return s[:i]
	}
	r := []rune(s)
	if len(r) > 90 {
		return string(r[:90]) + "…"
	}
	return s
}
