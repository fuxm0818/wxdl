# wxdl

微信视频号短视频**批量**下载命令行工具（macOS）。

复制几条视频号链接 → 一条命令 → 全部下到本地。不用打开网页、不用一个个点按钮、
不用懂抓包：环境检查、下载引擎、根证书、系统代理、任务创建、进度、收尾恢复，全自动完成。

```bash
wxdl https://weixin.qq.com/sph/AI7mqwJmdL https://weixin.qq.com/sph/xxxxx
```

> ⚠️ 仅供学习与技术研究。视频号内容有版权，请只下载你有权保存的视频，不要用于批量采集、
> 二次分发或任何商业用途。本仓库许可证见 [LICENSE](LICENSE)，随附引擎的许可见
> [NOTICE-upstream.md](NOTICE-upstream.md)。

---

## 它解决什么

上游开源引擎 `ltaoo/wx_channels_download` 的 Release 版是给视频号播放窗口加一个下载按钮，
一个一个点。wxdl 保留它的解密能力，把交互换成**批量化 + 零配置**：

| 想要的效果 | wxdl 的命令 |
| --- | --- |
| 一次下几十条链接 | `wxdl get --file links.txt` 或直接 `wxdl <链接1> <链接2> ...` |
| 剪贴板里有一堆链接 | `wxdl`（不传参数，自动读剪贴板） |
| 某个账号的视频全下 | `wxdl user "账号昵称" --limit 50` |
| 同事不用命令行 | 桌面双击「批量下载视频号视频.command」 |

## 安装

### 方式一：下载打包好的产物（推荐）

到 [Releases](https://github.com/fuxm0818/wxdl/releases) 下载对应机型压缩包，解压后按里面的 `install.sh` 走：

```bash
cd wxdl_*_darwin_arm64
sh install.sh
wxdl doctor
```

`install.sh` 会把 `wxdl` 装到 `~/.wxdl/bin/`，软链到 `/usr/local/bin/wxdl`（需要一次密码），
并把抓取引擎安装包一起放进 `~/.wxdl/vendor/`，**首装不必联网下载引擎**。

Apple Silicon（M1/M2/M3/M4）选 `darwin_arm64`，Intel 选 `darwin_x86_64`。

### 方式二：自己编译

```bash
git clone https://github.com/fuxm0818/wxdl.git
cd wxdl
go build -trimpath -o dist/wxdl .
./dist/wxdl doctor
```

需要 Go 1.23+；除标准库外零依赖。

## 使用

### 1. 批量下若干条链接

```bash
# 直接传参
wxdl get "https://weixin.qq.com/sph/Axxx" "https://weixin.qq.com/sph/Byyy"

# 从文件读（一行一个，混在中文文字里也能识别）
wxdl get --file links.txt

# 从标准输入
cat links.txt | wxdl get --file -

# 什么都不传：先读剪贴板，剪贴板没有就弹窗让你粘贴
wxdl
```

链接写成 `https://weixin.qq.com/sph/xxx`、`https://channels.weixin.qq.com/...` 都认。
重复链接自动去重，同一条重跑不会重复下载（已存在的文件会跳过）。

### 2. 下载整个账号

```bash
wxdl user "精准昵称" --limit 50 --pages 5
```

昵称不唯一时会弹窗让你选账号；也可以直接传该账号任意一条视频链接来定位。

### 3. 常用参数

| 参数 | 作用 |
| --- | --- |
| `--out DIR` / `-o` | 下载目录，默认 `~/Downloads/wxdl` |
| `--keep` | 下完不恢复系统代理，方便接着批量下（长批次必用） |
| `--limit N` | `user` 命令的下载上限，默认 20 |
| `--pages N` | `user` 命令最多翻几页列表，默认 5 |
| `--timeout 10m` | 单个视频最长等待，默认 20 分钟 |
| `--yes` | 不弹窗，交互走终端（脚本/CI 用） |
| `--zip FILE` | 用本地引擎安装包，完全离线 |
| `--filename NAME` | 自定义文件名（单视频时有意义） |

### 4. 运维命令

```bash
wxdl doctor              # 环境自检，不改任何设置
wxdl status              # 现在代理/证书/引擎是什么状态
wxdl undo                # 手动恢复系统代理设置
wxdl undo --cert         # 连本机根证书一起删
wxdl update --follow     # 升级到上游最新版引擎（默认是钉定版本）
wxdl update --tag v260907
wxdl clean               # 删除引擎和缓存，保留视频与配置
wxdl install-shortcut    # 桌面创建双击入口
```

## 第一次运行会发生什么

1. **检查**：系统版本、微信是否安装、当前网络服务、端口是否被占、是否已有别的代理软件。
2. **准备引擎**：按机型从 GitHub Release 下载上游引擎，用官方 `checksums.txt` 校验 sha256；
   校验不过就删除重下，两次不过直接失败退出。**这一步在所有会断网的操作之前完成。**
3. **冲突处理**：如果检测到 Clash / Surge / 代理类程序在跑，会弹窗告诉你原因，给你
   「帮我退出并继续」和「我自己处理」两个选择。你同意后它才退出这些程序。
4. **一次管理员授权**：信任 wxdl 本机根证书 + 把系统 HTTP/HTTPS 代理指向 `127.0.0.1:2023`，
   两个动作合并在同一个密码框里，只输一次。
5. **连接视频号**：自动打开微信，提示你点开任意一个视频号视频页面；页面连上后开始下载。
6. **下载**：逐个创建任务并打印进度 `[2/20] 标题 … 43%`。
7. **收尾**：自动把系统代理恢复成你原来的设置（`--keep` 时保留），完成通知 + 打开所在文件夹。

第二次开始，只要根证书还信任着、代理设置没变，**不再需要输密码**。

按 Ctrl-C 会立刻恢复代理再退出，不会把你的网络留在坏状态。

## 常见问题

**下载完 GitHub 打不开了 / 网络怪怪的**
系统代理恢复前中断过。跑 `wxdl undo`。之前用 Clash Verge 的话它会回到 7897 那套设置。

**弹窗说「视频号页面没连上」**
必须有一个视频号窗口是打开且正在显示的。在微信里点进视频号 → 随便打开一个视频 → 回去等它自检。
`--keep` 场景下中途关掉页面会断，重新打开即可，wxdl 会自动重连。

**公司电脑装不了证书 / 没有管理员密码**
这台机器装不了。MITM 方案必须信任一个本机根证书，绕不过去。

**装了 Clash / Surge，能不能不退出？**
可以，但两者会抢系统代理。推荐先完全退出代理软件再跑 wxdl；或者你自己手动把代理指到
`127.0.0.1:2023`（Clash 里开「系统代理」关掉、避免它接管），跑完再改回来。
TUN/虚拟网卡模式（fake-IP 段 `198.18.x.x`）会被识别为冲突并提示。

**下下来的文件名很乱**
默认用视频标题；非法字符会自动替换。指定名字用 `--filename`（只对单条生效）。

**只想下最高画质**
默认已经取引擎给出的最优视频规格。上游多规格支持通过 `video_variant_spec` 暴露，命令行暂未开放选择。

## 工作原理（一句话版）

视频号视频是加密的，解密所需的 `decode_key` 只出现在你本机微信与腾讯服务器之间的接口响应里。
wxdl 复用的上游引擎在你机器上做一个只对本机流量生效的 MITM 代理，从这些响应里取到密钥并解密。
所以：**必须临时接管一次系统代理 + 信任一个本地根证书**，也必须让微信的视频号页面连着引擎。
证书与密钥只存在 `~/.wxdl/certs/`（权限 0700/0600），不上传任何地方。

## 数据与配置位置

```
~/.wxdl/config.json     下载目录、端口、CA 名称、钉定的上游版本
~/.wxdl/state.json      你原来的代理设置快照（用于恢复）
~/.wxdl/bin/            上游引擎可执行文件
~/.wxdl/cache/          引擎安装包与校验文件
~/.wxdl/certs/          本机根证书与私钥（0600）
~/Downloads/wxdl/       下载到的视频
```

删除 `~/.wxdl` 即彻底清理（`wxdl undo --cert` 先把系统设置和证书摘掉）。

## 开发

```bash
go vet ./...
go test ./...
gofmt -l .
```

- `internal/upstream` 引擎下载与 sha256 校验
- `internal/proxy` 系统代理与证书状态机（含快照/恢复）
- `internal/flow` 端到端编排：自检 → 冲突处理 → 连接 → 批量任务
- `internal/server` 引擎本地 API + MCP 客户端
- `internal/dialog` 原生 macOS 弹窗（无 GUI 时降级到标准输入）

发布产物由 `.github/workflows/release.yml` 自动打包：交叉编译 arm64 + x86_64，
把钉定版本的上游引擎 zip 连同校验和一起塞进 Release。

## 许可

- wxdl：MIT，见 [LICENSE](LICENSE)。
- 随附/下载的上游引擎：MIT + "Commons Clause"，见 [LICENSE-upstream](LICENSE-upstream) 与
  [NOTICE-upstream.md](NOTICE-upstream.md)。Commons Clause 禁止出售，因此**不得出售 wxdl
  或含 wxdl 的服务**；免费分发和自行修改分发都不受影响。
