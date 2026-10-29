import AppKit
import Combine
import Foundation

struct DownloadTask: Identifiable, Equatable {
    enum State: Equatable {
        case running
        case done
        case failed
    }

    let id = UUID()
    var index: Int
    var title: String
    var state: State
    var detail: String = ""
}

struct StatusSnapshot: Equatable {
    enum Tone {
        case green
        case blue
        case orange
        case gray
    }

    var engine = "—"
    var proxy = "—"
    var cert = "—"
    var page = "—"
    var raw = ""
    var pillText = "检查中"
    var pillTone: Tone = .gray
}

struct Banner: Identifiable, Equatable {
    enum Kind {
        case info
        case success
        case error
    }

    let id = UUID()
    var kind: Kind
    var text: String
}

extension String {
    var trimmed: String { trimmingCharacters(in: .whitespacesAndNewlines) }
}

@MainActor
final class AppModel: ObservableObject {
    enum RunKind {
        case links
        case account
        case on
        case off
        case doctor
        case other
    }

    @Published var linksText = ""
    @Published var accountQuery = ""
    @Published var limit = 20 {
        didSet { UserDefaults.standard.set(limit, forKey: "wxdl.limit") }
    }
    @Published var useOriginal = false {
        didSet { UserDefaults.standard.set(useOriginal, forKey: "wxdl.useOriginal") }
    }
    @Published var downloadDir: String {
        didSet { UserDefaults.standard.set(downloadDir, forKey: "wxdl.downloadDir") }
    }
    @Published var tasks: [DownloadTask] = []
    @Published var log = ""
    @Published var isRunning = false
    @Published var runTitle = ""
    @Published var progressDone = 0.0
    @Published var progressTotal = 0.0
    @Published var banner: Banner?
    @Published var status = StatusSnapshot()
    @Published var tab = 0

    private var process: Process?
    private var pipe: Pipe?
    private var buffer = ""
    private var currentTask: Int?
    private var cancelled = false
    private var lastError: String?
    private var statusProcess: Process?
    private var pendingKind: RunKind = .other
    private var shutdownCompletion: (() -> Void)?

    init() {
        let defaults = UserDefaults.standard
        downloadDir = defaults.string(forKey: "wxdl.downloadDir") ?? "~/Downloads/wxdl"
        useOriginal = defaults.bool(forKey: "wxdl.useOriginal")
        let savedLimit = defaults.integer(forKey: "wxdl.limit")
        limit = savedLimit > 0 ? savedLimit : 20
    }

    // MARK: - Derived state

    var linkList: [String] { Self.extractLinks(linksText) }

    var linkCountText: String {
        let count = linkList.count
        return count == 0 ? "未识别到链接" : "识别到 \(count) 条链接"
    }

    var downloadDirPath: String { (downloadDir as NSString).expandingTildeInPath }

    var downloadDirDisplay: String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let path = downloadDirPath
        if path == home { return "~" }
        if path.hasPrefix(home + "/") { return "~" + path.dropFirst(home.count) }
        return path
    }

    var progressFraction: Double {
        progressTotal > 0 ? min(1, max(0, progressDone / progressTotal)) : 0
    }

    var hasProgress: Bool { isRunning || progressTotal > 0 }

    var appVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
    }

    var engineInstalled: Bool {
        let path = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".wxdl/bin/wx_video_download").path
        return FileManager.default.isExecutableFile(atPath: path)
    }

    var cliURL: URL? {
        if let bundled = Bundle.main.url(forResource: "wxdl", withExtension: nil, subdirectory: "bin") {
            return bundled
        }
        let fallback = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".wxdl/bin/wxdl")
        return FileManager.default.isExecutableFile(atPath: fallback.path) ? fallback : nil
    }

    private var engineZipPath: String? {
        guard let dir = Bundle.main.resourceURL?.appendingPathComponent("share/wxdl/upstream"),
              let names = try? FileManager.default.contentsOfDirectory(atPath: dir.path),
              let zip = names.first(where: { $0.hasSuffix(".zip") })
        else { return nil }
        return dir.appendingPathComponent(zip).path
    }

    // MARK: - Lifecycle

    func start() {
        if !engineInstalled {
            notify(.info, "首次使用会安装抓取引擎，并要一次管理员密码。若下载时提示视频号页面未连接，请 Cmd+Q 完全退出微信再打开。")
        }
        refreshStatus()
    }

    func shutdown(completion: @escaping () -> Void) {
        guard let running = process, isRunning else {
            completion()
            return
        }
        cancelled = true
        shutdownCompletion = completion
        appendLog("· 正在停止并恢复系统代理 …")
        running.interrupt()
        DispatchQueue.main.asyncAfter(deadline: .now() + 12) { [weak self] in
            guard let self, let current = self.process, current.isRunning else { return }
            current.terminate()
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 15) { [weak self] in
            guard let self, let finish = self.shutdownCompletion else { return }
            self.shutdownCompletion = nil
            finish()
        }
    }

    // MARK: - Input helpers

    func pasteFromClipboard() {
        guard let text = NSPasteboard.general.string(forType: .string), !text.trimmed.isEmpty else {
            notify(.info, "剪贴板里没有文字。")
            return
        }
        appendLinks(text)
    }

    func chooseLinksFile() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.message = "选择包含视频号链接的文本文件"
        panel.prompt = "导入"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        guard let data = try? Data(contentsOf: url), let text = String(data: data, encoding: .utf8) else {
            notify(.error, "读不了这个文件：\(url.lastPathComponent)")
            return
        }
        appendLinks(text)
    }

    func chooseDownloadDir() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = true
        panel.directoryURL = URL(fileURLWithPath: downloadDirPath)
        panel.message = "选择视频保存位置"
        panel.prompt = "使用这个文件夹"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        downloadDir = url.path
    }

    func openDownloadDir() {
        let url = URL(fileURLWithPath: downloadDirPath)
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        NSWorkspace.shared.open(url)
    }

    func revealLastFile() {
        if let path = tasks.last(where: { $0.state == .done })?.detail, !path.isEmpty,
           FileManager.default.fileExists(atPath: path) {
            NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
            return
        }
        openDownloadDir()
    }

    func copyLog() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(log, forType: .string)
        notify(.info, "日志已复制。")
    }

    func clearLog() {
        log = ""
    }

    func dismissBanner() {
        banner = nil
    }

    func notify(_ kind: Banner.Kind, _ text: String) {
        let item = Banner(kind: kind, text: text)
        banner = item
        DispatchQueue.main.asyncAfter(deadline: .now() + 9) { [weak self] in
            if self?.banner?.id == item.id { self?.banner = nil }
        }
    }

    // MARK: - Actions

    func startLinks() {
        let links = linkList
        guard !links.isEmpty else {
            notify(.error, "没有识别到视频号链接。支持 weixin.qq.com/sph/… 和 channels.weixin.qq.com/…")
            return
        }
        var args = ["get"] + links
        args += downloadArgs()
        run(args: args, title: "下载 \(links.count) 条链接", kind: .links)
    }

    func startAccount() {
        let query = accountQuery.trimmed
        guard !query.isEmpty else {
            notify(.error, "先填账号昵称、username，或该账号任意一条视频链接。")
            return
        }
        var args = ["user", query, "--limit", String(limit), "--pages", "5"]
        args += downloadArgs()
        run(args: args, title: "下载账号最近的 \(limit) 条", kind: .account)
    }

    func startOn() {
        var args = ["on", "--yes"]
        if let zip = engineZipPath { args += ["--zip", zip] }
        run(args: args, title: "开启窗口下载按钮", kind: .on)
    }

    func restoreNetwork() {
        run(args: ["off", "--yes"], title: "恢复系统网络", kind: .off)
    }

    func runDoctor() {
        run(args: ["doctor", "--yes"], title: "环境自检", kind: .doctor)
    }

    func cancel() {
        guard let running = process, isRunning else { return }
        cancelled = true
        appendLog("· 正在停止并恢复网络 …")
        running.interrupt()
        DispatchQueue.main.asyncAfter(deadline: .now() + 12) { [weak self] in
            guard let self, let current = self.process, current.isRunning else { return }
            current.terminate()
        }
    }

    private func downloadArgs() -> [String] {
        var args = ["--yes", "--out", downloadDirPath]
        if useOriginal { args += ["--spec", "original"] }
        if let zip = engineZipPath { args += ["--zip", zip] }
        return args
    }

    private func appendLinks(_ text: String) {
        var merged = linksText
        if !merged.isEmpty && !merged.hasSuffix("\n") { merged += "\n" }
        merged += text
        if !merged.hasSuffix("\n") { merged += "\n" }
        linksText = merged
    }

    // MARK: - Process

    private func run(args: [String], title: String, kind: RunKind) {
        guard !isRunning else {
            notify(.info, "上一个任务还在跑，等它结束或点「停止」。")
            return
        }
        guard let executable = cliURL else {
            notify(.error, "应用包里的 wxdl 命令行不见了，请重新下载安装包。")
            return
        }
        tasks = []
        progressDone = 0
        progressTotal = 0
        currentTask = nil
        lastError = nil
        cancelled = false
        pendingKind = kind
        runTitle = title
        isRunning = true
        banner = nil
        appendLog("\n$ wxdl " + args.joined(separator: " "))

        let child = Process()
        child.executableURL = executable
        child.arguments = args
        child.environment = cliEnvironment()
        let output = Pipe()
        child.standardOutput = output
        child.standardError = output
        output.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            DispatchQueue.main.async { self?.consume(text) }
        }
        child.terminationHandler = { [weak self] finished in
            DispatchQueue.main.async { self?.finished(finished) }
        }
        do {
            try child.run()
        } catch {
            isRunning = false
            notify(.error, "启动 wxdl 失败：\(error.localizedDescription)")
            return
        }
        process = child
        pipe = output
    }

    private func cliEnvironment() -> [String: String] {
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = (env["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin") + ":/usr/local/bin:/opt/homebrew/bin"
        env["LANG"] = "zh_CN.UTF-8"
        return env
    }

    private func finished(_ completed: Process) {
        pipe?.fileHandleForReading.readabilityHandler = nil
        pipe = nil
        process = nil
        if !buffer.isEmpty {
            let rest = buffer
            buffer = ""
            handle(line: rest)
        }
        isRunning = false

        if cancelled {
            for index in tasks.indices where tasks[index].state == .running {
                tasks[index].state = .failed
                tasks[index].detail = "已取消"
            }
            notify(.info, "已停止，系统代理已恢复。")
            cancelled = false
        } else if completed.terminationStatus == 0 {
            switch pendingKind {
            case .links, .account:
                let ok = tasks.filter { $0.state == .done }.count
                let failed = tasks.filter { $0.state == .failed }.count
                if failed == 0 {
                    notify(.success, "完成：\(ok) 个视频已保存到 \(downloadDirDisplay)")
                } else {
                    notify(.info, "完成：\(ok) 成功，\(failed) 失败。失败原因见右侧日志。")
                }
            case .on:
                notify(.success, "窗口下载按钮已开启。去微信视频号页面看看；用完点「恢复网络」。")
            case .off:
                notify(.success, "已恢复系统代理。")
            case .doctor:
                notify(.success, "自检完成，结果见右侧日志。")
            case .other:
                notify(.success, "完成。")
            }
        } else if pendingKind == .doctor {
            // doctor exits non-zero when any check fails; those are findings,
            // not a crash, so the banner must not read like an app error.
            notify(.info, "自检完成：有项目没通过，逐条看右侧日志。")
        } else {
            var message = lastError ?? "wxdl 退出码 \(completed.terminationStatus)，详情见右侧日志。"
            if message.contains("视频号页面") {
                message += "（如果刚设置过代理，请 Cmd+Q 完全退出微信再重新打开。）"
            }
            notify(.error, message)
        }
        currentTask = nil
        if let finish = shutdownCompletion {
            shutdownCompletion = nil
            finish()
            return
        }
        refreshStatus()
    }

    // MARK: - Output parsing

    private func consume(_ chunk: String) {
        let withoutColors = chunk.replacingOccurrences(
            of: "\u{001B}\\[[0-9;]*[A-Za-z]", with: "", options: .regularExpression)
        buffer += withoutColors
        while let newline = buffer.firstIndex(of: "\n") {
            var line = String(buffer[..<newline])
            buffer = String(buffer[buffer.index(after: newline)...])
            if let last = line.split(separator: "\r", omittingEmptySubsequences: false).last {
                line = String(last)
            }
            line = line.trimmingCharacters(in: .whitespaces)
            if line.isEmpty { continue }
            handle(line: line)
        }
    }

    private func handle(line: String) {
        appendLog(line)

        if line.contains("❌") {
            lastError = line.replacingOccurrences(of: "❌", with: "").trimmed
            return
        }
        if parseItemStart(line) { return }

        if line.contains("已保存：") {
            let raw = line.components(separatedBy: "已保存：").last?.trimmed ?? ""
            if let index = currentTask, tasks.indices.contains(index) {
                tasks[index].state = .done
                tasks[index].detail = cleanPath(raw)
                progressDone = Double(tasks[index].index)
            }
            return
        }
        if line.contains("跳过：") || line.contains("失败：") {
            if let index = currentTask, tasks.indices.contains(index), tasks[index].state == .running {
                tasks[index].state = .failed
                tasks[index].detail = line
                progressDone = Double(tasks[index].index)
            }
            return
        }
        if line.contains("完成："), progressTotal > 0 {
            progressDone = progressTotal
        }
    }

    private func parseItemStart(_ line: String) -> Bool {
        guard line.hasPrefix("["), let close = line.firstIndex(of: "]") else { return false }
        let inside = line[line.index(after: line.startIndex)..<close]
        let parts = inside.split(separator: "/")
        guard parts.count == 2, let number = Int(parts[0]), let total = Int(parts[1]),
              number > 0, total >= number else { return false }
        let title = String(line[line.index(after: close)...]).trimmed
        progressTotal = Double(total)
        progressDone = Double(number - 1)
        if let index = currentTask, tasks.indices.contains(index), tasks[index].state == .running {
            tasks[index].state = .done
        }
        tasks.append(DownloadTask(index: number, title: title.isEmpty ? "第 \(number) 条" : title, state: .running))
        currentTask = tasks.count - 1
        return true
    }

    private func cleanPath(_ raw: String) -> String {
        var path = raw
        if let range = path.range(of: " 时长") {
            path = String(path[..<range.lowerBound])
        }
        if let range = path.range(of: " (", options: .backwards) {
            path = String(path[..<range.lowerBound])
        }
        return path.trimmed
    }

    private func appendLog(_ line: String) {
        var text = log
        text += line + "\n"
        if text.count > 120_000 { text = String(text.suffix(80_000)) }
        log = text
    }

    // MARK: - Status

    func refreshStatus() {
        guard statusProcess == nil, let executable = cliURL else { return }
        let child = Process()
        child.executableURL = executable
        child.arguments = ["status", "--yes"]
        child.environment = cliEnvironment()
        let output = Pipe()
        child.standardOutput = output
        child.standardError = output
        child.terminationHandler = { [weak self] completed in
            let data = output.fileHandleForReading.readDataToEndOfFile()
            let text = String(data: data, encoding: .utf8) ?? ""
            DispatchQueue.main.async {
                self?.statusProcess = nil
                if completed.terminationStatus == 0 { self?.applyStatus(text) }
            }
        }
        do {
            try child.run()
            statusProcess = child
        } catch {
            statusProcess = nil
        }
    }

    private func applyStatus(_ text: String) {
        var snapshot = StatusSnapshot()
        snapshot.raw = text
        for raw in text.split(separator: "\n") {
            let line = String(raw)
            func value(of key: String) -> String? {
                let prefix = key + ":"
                guard line.hasPrefix(prefix) else { return nil }
                return String(line.dropFirst(prefix.count)).trimmed
            }
            if let v = value(of: "抓取引擎进程") {
                snapshot.engine = v
            } else if let v = value(of: "系统代理") {
                snapshot.proxy = v
            } else if let v = value(of: "根证书") {
                snapshot.cert = v
            } else if let v = value(of: "视频号页面") {
                snapshot.page = v
            }
        }
        if snapshot.proxy.contains("已指向 wxdl") {
            snapshot.pillText = "常驻中"
            snapshot.pillTone = .blue
        } else if snapshot.cert.contains("已受信任") {
            snapshot.pillText = "环境就绪"
            snapshot.pillTone = .green
        } else if snapshot.cert == "—" {
            snapshot.pillText = "未初始化"
            snapshot.pillTone = .orange
        } else {
            snapshot.pillText = "需要准备"
            snapshot.pillTone = .orange
        }
        status = snapshot
    }

    // MARK: - Link extraction

    static func extractLinks(_ text: String) -> [String] {
        let pattern = "https?://[^\\s\"'<>，。；、）】]+"
        guard let regex = try? NSRegularExpression(pattern: pattern) else { return [] }
        let source = text as NSString
        var seen = Set<String>()
        var links: [String] = []
        for match in regex.matches(in: text, range: NSRange(location: 0, length: source.length)) {
            var link = source.substring(with: match.range)
            while let last = link.last, ".,;:!?)]}>'\u{201D}".contains(last) {
                link.removeLast()
            }
            guard link.contains("weixin.qq.com/sph/") || link.contains("channels.weixin.qq.com") else { continue }
            if seen.insert(link).inserted { links.append(link) }
        }
        return links
    }
}
