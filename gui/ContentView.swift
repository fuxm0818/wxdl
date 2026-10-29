import AppKit
import SwiftUI

struct ContentView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            HStack(spacing: 0) {
                VStack(spacing: 0) {
                    Picker("", selection: $model.tab) {
                        Text("批量链接").tag(0)
                        Text("整个账号").tag(1)
                        Text("窗口按钮").tag(2)
                    }
                    .pickerStyle(.segmented)
                    .frame(width: 340)
                    .padding(.vertical, 10)
                    Divider()
                    ScrollView {
                        VStack(alignment: .leading, spacing: 14) {
                            if let banner = model.banner {
                                BannerView(banner: banner) { model.dismissBanner() }
                            }
                            switch model.tab {
                            case 1:
                                AccountPane(model: model)
                            case 2:
                                ServePane(model: model)
                            default:
                                LinksPane(model: model)
                            }
                            if model.hasProgress {
                                ProgressPane(model: model)
                            }
                            if !model.tasks.isEmpty {
                                TaskListPane(model: model)
                            }
                        }
                        .padding(18)
                    }
                }
                Divider()
                SidePanel(model: model)
                    .frame(width: 336)
            }
            Divider()
            footer
        }
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private var header: some View {
        HStack(spacing: 12) {
            Image(nsImage: NSApp.applicationIconImage)
                .resizable()
                .frame(width: 44, height: 44)
            VStack(alignment: .leading, spacing: 2) {
                Text("wxdl · 视频号批量下载")
                    .font(.system(size: 17, weight: .semibold))
                Text("一次下几十条，零配置；窗口里也能点「下载」")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer()
            StatusPill(status: model.status)
            Button {
                model.refreshStatus()
            } label: {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(.borderless)
            .help("刷新运行状态")
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 12)
    }

    private var footer: some View {
        HStack(spacing: 10) {
            Image(systemName: "folder")
                .foregroundColor(.secondary)
            Text(model.downloadDirDisplay)
                .font(.caption)
                .lineLimit(1)
                .truncationMode(.middle)
            Button("更改…") { model.chooseDownloadDir() }
                .controlSize(.small)
            Button("打开文件夹") { model.openDownloadDir() }
                .controlSize(.small)
            Button("在访达中显示") { model.revealLastFile() }
                .controlSize(.small)
            Spacer()
            Text("v\(model.appVersion)")
                .font(.caption2)
                .foregroundColor(.secondary)
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 9)
    }
}

// MARK: - Panes

struct LinksPane: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("把视频号链接粘到下面，一行一条；整段聊天记录直接粘也行，会自动挑出链接。")
                .font(.caption)
                .foregroundColor(.secondary)
            TextEditor(text: $model.linksText)
                .font(.system(size: 12, design: .monospaced))
                .frame(minHeight: 132)
                .padding(6)
                .background(RoundedRectangle(cornerRadius: 8).fill(Color(nsColor: .textBackgroundColor)))
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color(nsColor: .separatorColor)))
            HStack(spacing: 10) {
                Button {
                    model.pasteFromClipboard()
                } label: {
                    Label("从剪贴板粘贴", systemImage: "doc.on.clipboard")
                }
                Button {
                    model.chooseLinksFile()
                } label: {
                    Label("从文件导入", systemImage: "doc.badge.plus")
                }
                Text(model.linkCountText)
                    .font(.caption)
                    .foregroundColor(model.linkList.isEmpty ? .secondary : .accentColor)
                Spacer()
            }
            HStack {
                Toggle("下载原始视频（不转码）", isOn: $model.useOriginal)
                    .toggleStyle(.checkbox)
                Spacer()
                Button {
                    model.startLinks()
                } label: {
                    Label("开始下载", systemImage: "arrow.down.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(model.isRunning)
            }
            Text("首次运行会弹一次管理员密码：安装本机根证书并临时接管系统代理，结束后自动恢复。")
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .card()
    }
}

struct AccountPane: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("输入账号昵称、username（以 @finder 结尾），或该账号任意一条视频链接。")
                .font(.caption)
                .foregroundColor(.secondary)
            TextField("账号昵称 / username / 视频链接", text: $model.accountQuery)
                .textFieldStyle(.roundedBorder)
            HStack {
                Stepper(value: $model.limit, in: 1 ... 200) {
                    Text("最多下载 \(model.limit) 条")
                }
                Spacer()
                Toggle("下载原始视频（不转码）", isOn: $model.useOriginal)
                    .toggleStyle(.checkbox)
            }
            Text("昵称不唯一时不会弹选择框，会直接报错；改用精确昵称或 username 即可。")
                .font(.caption)
                .foregroundColor(.secondary)
            Text("首次运行会弹一次管理员密码：安装本机根证书并临时接管系统代理，结束后自动恢复。")
                .font(.caption)
                .foregroundColor(.secondary)
            HStack {
                Spacer()
                Button {
                    model.startAccount()
                } label: {
                    Label("下载该账号的视频", systemImage: "person.crop.circle.badge.arrow.down")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(model.isRunning)
            }
        }
        .card()
    }
}

struct ServePane: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("开启后，微信视频号窗口里会出现引擎注入的「下载」按钮：详情页底部有悬浮按钮，首页右侧有下载图标；鼠标悬停可选「原始视频」和各清晰度。")
                .font(.callout)
            VStack(alignment: .leading, spacing: 7) {
                StepText(number: "1", text: "首次使用先 Cmd+Q 完全退出微信，再打开视频号页面。")
                StepText(number: "2", text: "点「开启下载按钮」；第一次会要一次管理员密码（安装本机证书 + 改系统代理）。")
                StepText(number: "3", text: "用完点「恢复网络」。常驻期间流量会经过本机代理。")
            }
            Divider()
            HStack(spacing: 10) {
                Button {
                    model.startOn()
                } label: {
                    Label("开启下载按钮", systemImage: "play.rectangle.on.rectangle")
                }
                .buttonStyle(.borderedProminent)
                Button {
                    model.restoreNetwork()
                } label: {
                    Label("恢复网络", systemImage: "bolt.horizontal.circle")
                }
                Spacer()
                if model.status.proxy.contains("已指向 wxdl") {
                    Label("常驻中", systemImage: "dot.radiowaves.left.and.right")
                        .font(.caption)
                        .foregroundColor(.blue)
                }
            }
            .disabled(model.isRunning)
            Text("如果你正开着 Clash / Surge 等代理软件，请先完全退出，否则抓不到视频号流量。")
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .card()
    }
}

struct ProgressPane: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(model.isRunning ? model.runTitle : "上次任务")
                    .font(.subheadline)
                    .fontWeight(.semibold)
                Spacer()
                if model.progressTotal > 0 {
                    Text("\(Int(model.progressDone)) / \(Int(model.progressTotal))")
                        .font(.caption)
                        .monospacedDigit()
                        .foregroundColor(.secondary)
                }
                if model.isRunning {
                    Button("停止") { model.cancel() }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                }
            }
            if model.progressTotal > 0 {
                ProgressView(value: model.progressFraction)
                    .progressViewStyle(.linear)
            } else if model.isRunning {
                ProgressView()
                    .controlSize(.small)
            }
        }
        .card()
    }
}

struct TaskListPane: View {
    @ObservedObject var model: AppModel

    private var okCount: Int { model.tasks.filter { $0.state == .done }.count }
    private var failedCount: Int { model.tasks.filter { $0.state == .failed }.count }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("下载列表")
                    .font(.subheadline)
                    .fontWeight(.semibold)
                Spacer()
                Text("\(okCount) 成功 · \(failedCount) 失败")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            ForEach(model.tasks) { task in
                TaskRow(task: task)
                if task.id != model.tasks.last?.id { Divider() }
            }
        }
        .card()
    }
}

struct TaskRow: View {
    let task: DownloadTask

    var body: some View {
        HStack(spacing: 9) {
            Image(systemName: iconName)
                .foregroundColor(iconColor)
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 1) {
                Text(task.title)
                    .font(.system(size: 12))
                    .lineLimit(1)
                if !task.detail.isEmpty {
                    Text(task.detail)
                        .font(.system(size: 10))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                }
            }
            Spacer()
            if task.state == .done, FileManager.default.fileExists(atPath: task.detail) {
                Button {
                    NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: task.detail)])
                } label: {
                    Image(systemName: "folder")
                }
                .buttonStyle(.borderless)
                .help("在访达中显示")
            }
        }
    }

    private var iconName: String {
        switch task.state {
        case .running: return "arrow.down.circle"
        case .done: return "checkmark.circle.fill"
        case .failed: return "xmark.octagon.fill"
        }
    }

    private var iconColor: Color {
        switch task.state {
        case .running: return .accentColor
        case .done: return .green
        case .failed: return .red
        }
    }
}

// MARK: - Side panel

struct SidePanel: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            statusCard
            logCard
        }
        .padding(14)
    }

    private var statusCard: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack {
                Text("运行状态")
                    .font(.subheadline)
                    .fontWeight(.semibold)
                Spacer()
                Button("自检") { model.runDoctor() }
                    .controlSize(.small)
            }
            StatusRow(icon: "gearshape.2", name: "抓取引擎", value: model.status.engine)
            StatusRow(icon: "network", name: "系统代理", value: model.status.proxy)
            StatusRow(icon: "checkmark.shield", name: "根证书", value: model.status.cert)
            StatusRow(icon: "play.rectangle", name: "视频号页面", value: model.status.page)
            if !model.status.raw.isEmpty {
                DisclosureGroup("状态原文") {
                    ScrollView {
                        Text(model.status.raw)
                            .font(.system(size: 10, design: .monospaced))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(height: 96)
                }
                .font(.caption)
            }
        }
        .card()
    }

    private var logCard: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("运行日志")
                    .font(.subheadline)
                    .fontWeight(.semibold)
                Spacer()
                Button {
                    model.copyLog()
                } label: {
                    Image(systemName: "doc.on.doc")
                }
                .buttonStyle(.borderless)
                .help("复制日志")
                Button {
                    model.clearLog()
                } label: {
                    Image(systemName: "trash")
                }
                .buttonStyle(.borderless)
                .help("清空日志")
            }
            ScrollViewReader { reader in
                ScrollView {
                    Text(model.log.isEmpty ? "还没有运行记录。" : model.log)
                        .font(.system(size: 10.5, design: .monospaced))
                        .foregroundColor(model.log.isEmpty ? .secondary : .primary)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(6)
                    Color.clear.frame(height: 1).id("log-bottom")
                }
                .background(RoundedRectangle(cornerRadius: 7).fill(Color(nsColor: .textBackgroundColor)))
                .overlay(RoundedRectangle(cornerRadius: 7).stroke(Color(nsColor: .separatorColor)))
                .onChange(of: model.log) { _ in
                    reader.scrollTo("log-bottom", anchor: .bottom)
                }
            }
            Text("遇到问题时把这段日志发给开发者。")
                .font(.system(size: 10))
                .foregroundColor(.secondary)
        }
        .card()
    }
}

struct StatusRow: View {
    let icon: String
    let name: String
    let value: String

    var body: some View {
        HStack(alignment: .top, spacing: 7) {
            Image(systemName: icon)
                .font(.system(size: 11))
                .foregroundColor(.secondary)
                .frame(width: 15)
            VStack(alignment: .leading, spacing: 1) {
                Text(name)
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
                Text(value)
                    .font(.system(size: 11))
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
    }
}

struct StatusPill: View {
    let status: StatusSnapshot

    var body: some View {
        HStack(spacing: 6) {
            Circle()
                .fill(toneColor)
                .frame(width: 7, height: 7)
            Text(status.pillText)
                .font(.system(size: 11, weight: .medium))
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 5)
        .background(Capsule().fill(toneColor.opacity(0.14)))
        .foregroundColor(toneColor)
    }

    private var toneColor: Color {
        switch status.pillTone {
        case .green: return .green
        case .blue: return .blue
        case .orange: return .orange
        case .gray: return .gray
        }
    }
}

struct BannerView: View {
    let banner: Banner
    let dismiss: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: iconName)
                .foregroundColor(tint)
            Text(banner.text)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 4)
            Button {
                dismiss()
            } label: {
                Image(systemName: "xmark")
                    .font(.system(size: 10))
            }
            .buttonStyle(.borderless)
        }
        .padding(10)
        .background(RoundedRectangle(cornerRadius: 8).fill(tint.opacity(0.10)))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(tint.opacity(0.35)))
    }

    private var iconName: String {
        switch banner.kind {
        case .info: return "info.circle.fill"
        case .success: return "checkmark.circle.fill"
        case .error: return "exclamationmark.triangle.fill"
        }
    }

    private var tint: Color {
        switch banner.kind {
        case .info: return .accentColor
        case .success: return .green
        case .error: return .red
        }
    }
}

struct StepText: View {
    let number: String
    let text: String

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Text(number)
                .font(.system(size: 10, weight: .bold))
                .foregroundColor(.white)
                .frame(width: 17, height: 17)
                .background(Circle().fill(Color.accentColor))
            Text(text)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}

extension View {
    func card() -> some View {
        padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 10).fill(Color(nsColor: .controlBackgroundColor)))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color(nsColor: .separatorColor).opacity(0.6)))
    }
}
