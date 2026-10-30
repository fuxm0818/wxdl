import AppKit
import SwiftUI

@main
struct WxdlApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            ContentView(model: model)
                .frame(minWidth: 980, minHeight: 660)
                .onAppear {
                    delegate.model = model
                    model.start()
                }
        }
        .commands {
            CommandGroup(replacing: .newItem) {}
        }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    weak var model: AppModel?
    private var forceTerminate = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        DispatchQueue.main.async {
            guard let window = NSApp.windows.first else { return }
            window.setContentSize(NSSize(width: 1180, height: 780))
            window.center()
            window.makeKeyAndOrderFront(nil)
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let model, !forceTerminate else { return .terminateNow }
        let handled = model.prepareForTermination { [weak self] allow in
            NSApp.reply(toApplicationShouldTerminate: allow)
            if !allow {
                DispatchQueue.main.async { self?.explainRestoreFailure() }
            }
        }
        return handled ? .terminateLater : .terminateNow
    }

    private func explainRestoreFailure() {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "没能恢复系统代理"
        alert.informativeText = "系统代理可能还指向 wxdl，直接退出会断网。可以重试恢复，确认网络正常后再退出。"
        alert.addButton(withTitle: "重试恢复")
        alert.addButton(withTitle: "仍然退出")
        NSApp.activate(ignoringOtherApps: true)
        if alert.runModal() == .alertFirstButtonReturn {
            DispatchQueue.main.async { NSApp.terminate(nil) }
        } else {
            forceTerminate = true
            DispatchQueue.main.async { NSApp.terminate(nil) }
        }
    }
}
