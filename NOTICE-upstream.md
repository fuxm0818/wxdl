# 上游组件声明 / Third-Party Notice

wxdl 本身是 MIT 协议的独立程序。它**不包含**上游工程的源代码，只在你首次运行时
下载并使用下面这个开源引擎来完成真正的抓包与解密工作。

## 引擎组件

| 项目 | 内容 |
| --- | --- |
| 名称 | `wx_video_download`（微信视频号下载引擎） |
| 来源 | https://github.com/ltaoo/wx_channels_download |
| 协议 | MIT License + "Commons Clause" License Condition v1.0 |
| wxdl 钉定版本 | `v260907` |
| 协议全文 | 见本仓库 [`LICENSE-upstream`](LICENSE-upstream) |

## wxdl 做了哪些事

1. 按 `goos/goarch` 从上游 GitHub Release 下载对应压缩包（Apple Silicon 与 Intel 各自的包）。
2. 用上游发布的 `checksums.txt` 校验 sha256，不匹配则拒绝安装并删除。
3. 以子进程方式在本机 `127.0.0.1` 启动引擎，通过它自带的本地 HTTP / MCP 接口创建下载任务。
4. 收集网络环境信息，在获得你明确授权后临时把系统代理指向本机并信任引擎生成的根证书；
   任务结束后恢复你原来的代理设置。

wxdl 没有修改、也没有重新分发上游的编译产物以外的任何代码。本仓库的发布产物中如果内嵌了
上游压缩包，那是为了让你在没有 GitHub 访问条件的机器上也能离线使用；内嵌包与上游 Release
中的同名文件字节一致，sha256 见下表。

## 钉定的上游包完整性

| 文件 | sha256 |
| --- | --- |
| `wx_video_download_v260907_darwin_arm64.zip` | `c7b302db0b9e9f041c99c1bb60d086a9060eaed6244501f6d435848b0cc6df12` |
| `wx_video_download_v260907_darwin_x86_64.zip` | `8b6a254544c235c1123955537c02c8ea3ae608ff118f18f49d596823966229d0` |

（来源：同版本 Release 的 `wx_video_download_v260907_checksums.txt`。）

## Commons Clause 的影响

上游许可证在 MIT 基础上附加了 "Commons Clause"：**禁止 Sale**（出售、或以提供商业优势/
报酬为条件的授权分发）。其余权利——使用、复制、修改、合并、发布、再分发——不受影响。

因此：

- 你可以自由地免费分发 wxdl，也可以自行修改后免费分发。
- **不得出售 wxdl，不得出售包含 wxdl 的服务**（包括捆绑上游引擎的发布包）。
- 本仓库的 `LICENSE` 只覆盖 wxdl 自身的 MIT 部分；由于发行包内含上游引擎，
  **该 MIT 许可不授予出售 wxdl 或任何价值主要来自该引擎的产品的权利**——
  这项限制来自上游的 Commons Clause，全文见 `LICENSE-upstream`。
- 保留本声明和 `LICENSE-upstream` 的署名与许可文本。

## 用途限制

wxdl 及上游引擎仅供**学习与技术研究**。请只下载你有权保存的内容，遵守《微信公众平台音视频
账号运营规范》以及你所在地区法律；不要用于批量爬取、二次分发或商业目的。
