import AppKit
import Combine
import Foundation

struct DownloadTask: Identifiable, Equatable {
    enum State: Equatable {
        case running
        case done
        case skipped
        case failed
    }

    let id = UUID()
    var index: Int
    var title: String
    var state: State
    var detail: String = ""
    var path: String = ""
    var pct: Double = 0
    var bytes: Int64 = 0
    var totalBytes: Int64 = 0
    var phase: String = "queued"
    var errorCode: String = ""
    var hint: String = ""
}

struct StatusSnapshot: Equatable {
    enum Tone {
        case green
        case blue
        case orange
        case gray
    }

    var engine = "—"
    var engineProcess = "—"
    var engineTag = ""
    var engineRunning = false
    var api = "—"
    var apiAlive = false
    var proxy = "—"
    var proxyIsOurs = false
    var cert = "—"
    var certPresent = false
    var certTrusted = false
    var page = "—"
    var pageConnected = false
    var resident = false
    var startedAt = ""
    var raw = ""
    var pillText = "检查中"
    var pillTone: Tone = .gray
}

struct PageWaitInfo: Equatable {
    var elapsed = 0
    var timeout = 0

    var clock: String {
        String(format: "%02d:%02d", elapsed / 60, elapsed % 60)
    }
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

    private struct RunSummary {
        var ok = 0
        var failed = 0
        var skipped = 0
        var dir = ""
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
    @Published var pageWait: PageWaitInfo?
    @Published var activity = ""

    private var process: Process?
    private var stdoutPipe: Pipe?
    private var stderrPipe: Pipe?
    private var stdoutBuffer = ""
    private var stderrBuffer = ""
    private var stderrTail = ""
    private var cancelled = false
    private var lastError: String?
    private var lastErrorHint: String?
    private var statusProcess: Process?
    private var pendingKind: RunKind = .other
    private var shutdownCompletion: (() -> Void)?
    private var summary: RunSummary?
    private var doctorFails = 0
    private var restoreFailed = false

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
        activity = "正在停止并恢复网络设置…"
        pageWait = nil
        appendLog("· 正在停止并恢复系统代理 …")
        running.interrupt()
        // Restoring the proxy can raise an administrator prompt; killing the
        // child before the user answers would leave the system proxy pointed at
        // a closed port, so the fallbacks are deliberately generous.
        DispatchQueue.main.asyncAfter(deadline: .now() + 60) { [weak self] in
            guard let self, let current = self.process, current.isRunning else { return }
            current.terminate()
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 75) { [weak self] in
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
        if let path = tasks.last(where: { $0.state == .done && !$0.path.isEmpty })?.path,
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
        // The feed list holds roughly 20 videos per page; a fixed five pages
        // could never reach the 200 the stepper allows.
        let pages = max(1, Int(ceil(Double(limit) / 20.0)))
        var args = ["user", query, "--limit", String(limit), "--pages", String(pages)]
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
        guard let running = process, isRunning, !cancelled else { return }
        cancelled = true
        activity = "正在停止并恢复网络设置…"
        appendLog("· 正在停止并恢复网络 …")
        running.interrupt()
        DispatchQueue.main.asyncAfter(deadline: .now() + 60) { [weak self] in
            guard let self, let current = self.process, current.isRunning else { return }
            current.terminate()
        }
    }

    private func downloadArgs() -> [String] {
        // --wait-page=forever is the GUI's one behavioural difference: waiting
        // for the Channels page is shown as a step, never a silent timeout.
        var args = ["--yes", "--wait-page=forever", "--out", downloadDirPath]
        if useOriginal { args += ["--spec", "original"] }
        if let zip = engineZipPath { args += ["--zip", zip] }
        // A download started while resident must not tear the proxy down when
        // it ends; ending resident mode stays an explicit action.
        if status.proxyIsOurs { args.append("--keep") }
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
        var fullArgs = args
        if !fullArgs.contains("--json") { fullArgs.append("--json") }

        tasks = []
        progressDone = 0
        progressTotal = 0
        lastError = nil
        lastErrorHint = nil
        cancelled = false
        summary = nil
        doctorFails = 0
        restoreFailed = false
        pageWait = nil
        activity = ""
        pendingKind = kind
        runTitle = title
        isRunning = true
        banner = nil
        stdoutBuffer = ""
        stderrBuffer = ""
        stderrTail = ""
        appendLog("\n$ wxdl " + fullArgs.joined(separator: " "))

        let child = Process()
        child.executableURL = executable
        child.arguments = fullArgs
        child.environment = cliEnvironment()
        let outPipe = Pipe()
        let errPipe = Pipe()
        child.standardOutput = outPipe
        child.standardError = errPipe
        outPipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            DispatchQueue.main.async { self?.consumeStdout(text) }
        }
        errPipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            DispatchQueue.main.async { self?.consumeStderr(text) }
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
        stdoutPipe = outPipe
        stderrPipe = errPipe
    }

    private func cliEnvironment() -> [String: String] {
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = (env["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin") + ":/usr/local/bin:/opt/homebrew/bin"
        env["LANG"] = "zh_CN.UTF-8"
        return env
    }

    private func finished(_ completed: Process) {
        stdoutPipe?.fileHandleForReading.readabilityHandler = nil
        stderrPipe?.fileHandleForReading.readabilityHandler = nil
        stdoutPipe = nil
        stderrPipe = nil
        process = nil

        if !stdoutBuffer.trimmed.isEmpty {
            let rest = stdoutBuffer
            stdoutBuffer = ""
            handleEventLine(rest.trimmed)
        }
        if !stderrBuffer.trimmed.isEmpty {
            let rest = stderrBuffer.trimmed
            stderrBuffer = ""
            if let last = rest.split(separator: "\n").last {
                stderrTail = String(last).trimmed
            }
            appendLog(rest)
        }

        isRunning = false
        activity = ""
        pageWait = nil

        if cancelled {
            for index in tasks.indices where tasks[index].state == .running {
                tasks[index].state = .failed
                tasks[index].detail = "已取消"
            }
            if restoreFailed {
                notify(.error, "已停止，但没能恢复系统代理。请在系统设置里检查，或点「恢复网络」。")
            } else {
                notify(.info, "已停止，系统代理已恢复。")
            }
            cancelled = false
            restoreFailed = false
        } else if completed.terminationStatus == 0 {
            switch pendingKind {
            case .links, .account:
                let ok = summary?.ok ?? tasks.filter { $0.state == .done }.count
                let failed = summary?.failed ?? tasks.filter { $0.state == .failed }.count
                let skipped = summary?.skipped ?? tasks.filter { $0.state == .skipped }.count
                if failed == 0 {
                    var text = "完成：\(ok) 个视频已保存到 \(downloadDirDisplay)"
                    if skipped > 0 { text = "完成：\(ok) 个已保存，\(skipped) 个之前下过。" }
                    notify(.success, text)
                } else {
                    var text = "完成：\(ok) 成功，\(failed) 失败"
                    if skipped > 0 { text += "，\(skipped) 跳过" }
                    notify(.info, text + "。失败原因见右侧日志。")
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
            notify(.info, "自检完成：有 \(doctorFails) 项没通过，逐条看右侧日志。")
        } else {
            var message = lastError ?? (stderrTail.isEmpty ? nil : stderrTail)
                ?? "wxdl 退出码 \(completed.terminationStatus)，详情见右侧日志。"
            if let hint = lastErrorHint, !hint.isEmpty {
                message += "（\(hint)）"
            }
            notify(.error, message)
        }
        if let finish = shutdownCompletion {
            shutdownCompletion = nil
            finish()
            return
        }
        refreshStatus()
    }

    // MARK: - Event stream

    private func consumeStdout(_ chunk: String) {
        stdoutBuffer += chunk
        while let newline = stdoutBuffer.firstIndex(of: "\n") {
            let line = String(stdoutBuffer[..<newline]).trimmed
            stdoutBuffer = String(stdoutBuffer[stdoutBuffer.index(after: newline)...])
            if !line.isEmpty { handleEventLine(line) }
        }
    }

    private func consumeStderr(_ chunk: String) {
        stderrBuffer += chunk
        while let newline = stderrBuffer.firstIndex(of: "\n") {
            let line = String(stderrBuffer[..<newline]).trimmed
            stderrBuffer = String(stderrBuffer[stderrBuffer.index(after: newline)...])
            if !line.isEmpty {
                stderrTail = line
                appendLog(line)
            }
        }
    }

    private func handleEventLine(_ line: String) {
        guard let data = line.data(using: .utf8),
              let raw = try? JSONSerialization.jsonObject(with: data),
              let obj = raw as? [String: Any],
              let type = obj["t"] as? String else {
            appendLog("· 非事件输出：\(line)")
            return
        }
        handle(event: type, obj)
    }

    private func handle(event: String, _ obj: [String: Any]) {
        switch event {
        case "run.start":
            let total = intValue(obj, "total")
            if total > 0 { progressTotal = Double(total) }
            activity = "正在准备运行环境…"
            appendLog("· 开始：\(stringValue(obj, "cmd"))，\(total > 0 ? "\(total) 条" : "待确认数量")")
        case "run.plan":
            progressTotal = Double(intValue(obj, "total"))
            appendLog("· 计划下载 \(Int(progressTotal)) 条")
        case "env.check":
            appendLog("· 环境检查 \(stringValue(obj, "name"))：\(stringValue(obj, "detail"))")
        case "engine.install":
            let phase = stringValue(obj, "phase")
            switch phase {
            case "done":
                activity = ""
                appendLog("· 抓取引擎已就绪 \(stringValue(obj, "tag"))")
            case "failed":
                activity = ""
            default:
                activity = "正在准备抓取引擎…"
            }
        case "auth.request":
            activity = "等待管理员授权…"
            pageWait = nil
            appendLog("· 需要管理员授权：\(stringValue(obj, "reason"))")
        case "auth.done":
            activity = ""
            appendLog("· 管理员授权\(boolValue(obj, "granted") ? "已完成" : "被取消")")
        case "wait.page":
            if boolValue(obj, "connected") {
                pageWait = nil
                activity = ""
            } else {
                pageWait = PageWaitInfo(elapsed: intValue(obj, "elapsed"), timeout: intValue(obj, "timeout"))
                activity = "等待微信视频号页面…"
            }
        case "item.start":
            startTask(obj)
        case "item.progress":
            guard let index = taskIndex(intValue(obj, "index")) else { break }
            tasks[index].pct = doubleValue(obj, "pct")
            tasks[index].bytes = int64Value(obj, "bytes")
            tasks[index].totalBytes = int64Value(obj, "total_bytes")
            tasks[index].phase = stringValue(obj, "status")
            if tasks[index].state != .running { tasks[index].state = .running }
        case "item.done":
            guard let index = taskIndex(intValue(obj, "index")) else { break }
            tasks[index].state = .done
            tasks[index].path = stringValue(obj, "path")
            tasks[index].pct = 100
            tasks[index].bytes = int64Value(obj, "bytes")
            if tasks[index].totalBytes == 0 { tasks[index].totalBytes = tasks[index].bytes }
            let name = (tasks[index].path as NSString).lastPathComponent
            let duration = stringValue(obj, "duration")
            tasks[index].detail = duration.isEmpty ? name : "\(name) · \(duration)"
        case "item.skip":
            guard let index = taskIndex(intValue(obj, "index")) else { break }
            tasks[index].state = .skipped
            tasks[index].path = stringValue(obj, "path")
            tasks[index].detail = "之前下过了"
        case "item.fail":
            guard let index = taskIndex(intValue(obj, "index")) else { break }
            tasks[index].state = .failed
            tasks[index].detail = stringValue(obj, "msg")
            tasks[index].errorCode = stringValue(obj, "code")
            tasks[index].hint = stringValue(obj, "hint")
        case "item.retry":
            guard let index = taskIndex(intValue(obj, "index")) else { break }
            tasks[index].detail = "正在重试…"
        case "run.progress":
            progressDone = Double(intValue(obj, "done"))
            progressTotal = Double(intValue(obj, "total"))
        case "run.done":
            summary = RunSummary(
                ok: intValue(obj, "ok"),
                failed: intValue(obj, "failed"),
                skipped: intValue(obj, "skipped"),
                dir: stringValue(obj, "dir")
            )
            if progressTotal > 0 { progressDone = progressTotal }
            activity = ""
            pageWait = nil
            if let s = summary {
                appendLog("· 完成：\(s.ok) 成功，\(s.skipped) 跳过，\(s.failed) 失败")
            }
        case "log":
            appendLog(stringValue(obj, "msg"))
        case "error":
            let code = stringValue(obj, "code")
            if code == "restore_failed" { restoreFailed = true }
            lastError = stringValue(obj, "msg")
            lastErrorHint = stringValue(obj, "hint")
            activity = ""
            pageWait = nil
            appendLog("❌ \(lastError ?? "")")
        case "status":
            applyStatusJSON(obj)
        case "doctor.done":
            doctorFails = intValue(obj, "fails")
            activity = ""
            appendLog("· 自检完成：\(doctorFails) 项未通过")
        default:
            // A newer CLI may add events; an older App must keep working.
            appendLog("· 未识别事件 \(event)")
        }
    }

    private func startTask(_ obj: [String: Any]) {
        let index = intValue(obj, "index")
        let total = intValue(obj, "total")
        if total > 0 { progressTotal = Double(total) }
        let title = stringValue(obj, "title")
        let url = stringValue(obj, "url")
        let display = title.isEmpty ? (url.isEmpty ? "第 \(index) 条" : url) : title
        activity = ""
        pageWait = nil
        if let existing = taskIndex(index) {
            tasks[existing].state = .running
            tasks[existing].title = display
            tasks[existing].detail = ""
            tasks[existing].path = ""
            tasks[existing].pct = 0
            tasks[existing].bytes = 0
            tasks[existing].totalBytes = 0
            tasks[existing].phase = "queued"
            tasks[existing].errorCode = ""
            tasks[existing].hint = ""
        } else {
            tasks.append(DownloadTask(index: index, title: display, state: .running))
        }
    }

    private func taskIndex(_ index: Int) -> Int? {
        tasks.firstIndex { $0.index == index }
    }

    private func intValue(_ obj: [String: Any], _ key: String) -> Int {
        (obj[key] as? NSNumber)?.intValue ?? 0
    }

    private func int64Value(_ obj: [String: Any], _ key: String) -> Int64 {
        (obj[key] as? NSNumber)?.int64Value ?? 0
    }

    private func doubleValue(_ obj: [String: Any], _ key: String) -> Double {
        (obj[key] as? NSNumber)?.doubleValue ?? 0
    }

    private func boolValue(_ obj: [String: Any], _ key: String) -> Bool {
        (obj[key] as? NSNumber)?.boolValue ?? false
    }

    private func stringValue(_ obj: [String: Any], _ key: String) -> String {
        obj[key] as? String ?? ""
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
        child.arguments = ["status", "--json"]
        child.environment = cliEnvironment()
        let output = Pipe()
        child.standardOutput = output
        child.standardError = Pipe()
        child.terminationHandler = { [weak self] completed in
            let data = output.fileHandleForReading.readDataToEndOfFile()
            let text = String(data: data, encoding: .utf8) ?? ""
            DispatchQueue.main.async {
                self?.statusProcess = nil
                if completed.terminationStatus == 0 { self?.applyStatusLines(text) }
            }
        }
        do {
            try child.run()
            statusProcess = child
        } catch {
            statusProcess = nil
        }
    }

    private func applyStatusLines(_ text: String) {
        for line in text.split(separator: "\n") {
            guard let data = line.data(using: .utf8),
                  let raw = try? JSONSerialization.jsonObject(with: data),
                  let obj = raw as? [String: Any],
                  obj["t"] as? String == "status" else { continue }
            applyStatusJSON(obj)
            return
        }
    }

    private func applyStatusJSON(_ obj: [String: Any]) {
        var snapshot = StatusSnapshot()
        snapshot.engineProcess = stringValue(obj, "engine_process")
        snapshot.engineTag = stringValue(obj, "engine_tag")
        snapshot.engineRunning = boolValue(obj, "engine_running")
        snapshot.engine = snapshot.engineTag.isEmpty
            ? snapshot.engineProcess
            : "\(snapshot.engineProcess) · \(snapshot.engineTag)"
        snapshot.api = stringValue(obj, "api")
        snapshot.apiAlive = boolValue(obj, "api_alive")
        snapshot.proxy = stringValue(obj, "proxy")
        snapshot.proxyIsOurs = boolValue(obj, "proxy_is_ours")
        snapshot.cert = stringValue(obj, "cert")
        snapshot.certPresent = boolValue(obj, "cert_present")
        snapshot.certTrusted = boolValue(obj, "cert_trusted")
        snapshot.page = stringValue(obj, "page")
        snapshot.pageConnected = boolValue(obj, "page_connected")
        snapshot.resident = boolValue(obj, "resident")
        snapshot.startedAt = stringValue(obj, "started_at")
        if let raw = try? JSONSerialization.data(withJSONObject: obj, options: [.prettyPrinted, .sortedKeys]),
           let text = String(data: raw, encoding: .utf8) {
            snapshot.raw = text
        }
        if snapshot.proxyIsOurs {
            snapshot.pillText = "常驻中"
            snapshot.pillTone = .blue
        } else if snapshot.certTrusted {
            snapshot.pillText = "环境就绪"
            snapshot.pillTone = .green
        } else if !snapshot.certPresent {
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
