import AppKit
import ApplicationServices
import ScreenCaptureKit
import Darwin

struct ComputerError: Error { let message: String }
func fail(_ message: String) throws -> Never { throw ComputerError(message: message) }
func getAttribute(_ element: AXUIElement, _ name: String) -> CFTypeRef? {
    var value: CFTypeRef?
    return AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success ? value : nil
}
func getFrame(_ element: AXUIElement) -> CGRect? {
    guard let p = getAttribute(element, kAXPositionAttribute), let s = getAttribute(element, kAXSizeAttribute),
          CFGetTypeID(p) == AXValueGetTypeID(), CFGetTypeID(s) == AXValueGetTypeID() else { return nil }
    var point = CGPoint.zero, size = CGSize.zero
    guard AXValueGetValue(p as! AXValue, .cgPoint, &point), AXValueGetValue(s as! AXValue, .cgSize, &size) else { return nil }
    return CGRect(origin: point, size: size)
}
func getSelection(_ element: CFTypeRef?) -> String? {
    guard let element = element, CFGetTypeID(element) == AXUIElementGetTypeID(),
          let value = getAttribute(element as! AXUIElement, kAXSelectedTextRangeAttribute), CFGetTypeID(value) == AXValueGetTypeID() else { return nil }
    var range = CFRange()
    guard AXValueGetValue(value as! AXValue, .cfRange, &range) else { return nil }
    return "\(range.location):\(range.length)"
}
func getLabel(_ element: AXUIElement) -> String {
    for attribute in [kAXTitleAttribute, kAXDescriptionAttribute, kAXValueAttribute] {
        if let text = getAttribute(element, attribute) as? String, !text.isEmpty { return String(text.prefix(160)) }
    }
    return ""
}

@MainActor final class Desktop {
    var actionStarted = false
    var lockFD: Int32 = -1
    var owner = ""
    var observationID = ""
    var appID = ""
    var windowID: CGWindowID = 0
    var frame = CGRect.zero
    var imageWidth = 0, imageHeight = 0
    var elements: [AXUIElement] = []
    var elementFrames: [CGRect] = []
    var elementLabels: [String] = []
    var image = ""
    var focusedElement: CFTypeRef?
    var selectedRange: String?

    func acquire(_ runID: String) throws {
        guard !runID.isEmpty else { try fail("owner RunID is required") }
        if owner == runID { return }
        guard owner.isEmpty else { try fail("desktop_busy: another Run owns the desktop") }
        let path = NSTemporaryDirectory() + "deepagent-desktop-\(getuid()).lock"
        let fd = open(path, O_CREAT | O_RDWR | O_NOFOLLOW, mode_t(0o600))
        guard fd >= 0 else { try fail("cannot open desktop lock at \(path): \(String(cString: strerror(errno)))") }
        guard flock(fd, LOCK_EX | LOCK_NB) == 0 else { close(fd); try fail("desktop_busy: another Worker owns the desktop") }
        lockFD = fd; owner = runID
    }
    func release(_ runID: String) {
        guard owner == runID else { return }
        flock(lockFD, LOCK_UN); close(lockFD); lockFD = -1; owner = ""
    }
    func checkPermissions() throws {
        guard AXIsProcessTrusted() else { try fail("Enable Accessibility for deepagent-computer (or the app launching Worker) in System Settings > Privacy & Security") }
        guard CGPreflightScreenCaptureAccess() else { try fail("Enable Screen Recording for deepagent-computer (or the app launching Worker) in System Settings > Privacy & Security") }
    }
    func getWindow(_ bundleID: String) async throws -> (SCWindow, AXUIElement) {
        guard let app = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).first,
              NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier else { try fail("stale_observation: requested app is not in front; open it again") }
        let axApp = AXUIElementCreateApplication(app.processIdentifier)
        guard let window = getAttribute(axApp, kAXFocusedWindowAttribute), CFGetTypeID(window) == AXUIElementGetTypeID(),
              let rect = getFrame(window as! AXUIElement) else { try fail("application has no accessible focused window") }
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: true)
        let candidates = content.windows.filter { $0.owningApplication?.processID == app.processIdentifier && $0.windowLayer == 0 && $0.frame.width > 0 && $0.frame.height > 0 }
        guard let scWindow = candidates.min(by: { abs($0.frame.minX-rect.minX)+abs($0.frame.minY-rect.minY) < abs($1.frame.minX-rect.minX)+abs($1.frame.minY-rect.minY) }) else { try fail("application window is unavailable") }
        return (scWindow, window as! AXUIElement)
    }
    func capture(_ window: SCWindow) async throws -> String {
        let filter = SCContentFilter(desktopIndependentWindow: window)
        let config = SCStreamConfiguration()
        let scale = min(1, 1280 / max(window.frame.width, window.frame.height))
        config.width = max(1, Int(window.frame.width * scale)); config.height = max(1, Int(window.frame.height * scale))
        config.showsCursor = false; config.ignoreShadowsSingleWindow = true
        let cgImage = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
        let bitmap = NSBitmapImageRep(cgImage: cgImage)
        guard let png = bitmap.representation(using: .png, properties: [:]) else { try fail("PNG encoding failed") }
        return png.base64EncodedString()
    }
    func observe(_ bundleID: String) async throws -> [String: Any] {
        let (window, axWindow) = try await getWindow(bundleID)
        let png = try await capture(window)
        elements = []; elementFrames = []; elementLabels = []
        var descriptions: [[String: Any]] = [], text: [String] = [], visited = 0
        let scale = min(1, 1280 / max(window.frame.width, window.frame.height))
        func walk(_ element: AXUIElement, _ depth: Int) {
            guard depth < 20 && visited < 1500 && elements.count < 150 else { return }
            visited += 1
            let label = getLabel(element)
            if !label.isEmpty && text.joined().count < 8000 { text.append(label) }
            let role = getAttribute(element, kAXRoleAttribute) as? String ?? ""
            let interactive = [kAXButtonRole, kAXTextFieldRole, kAXTextAreaRole, kAXCheckBoxRole, kAXRadioButtonRole, kAXPopUpButtonRole, "AXLink"].contains(role)
            if interactive, let rect = getFrame(element), rect.width > 0, rect.height > 0, window.frame.contains(rect) {
                elements.append(element); elementFrames.append(rect); elementLabels.append(label)
                descriptions.append(["element_id": elements.count, "role": role, "name": label, "x": (rect.minX-window.frame.minX)*scale, "y": (rect.minY-window.frame.minY)*scale, "width": rect.width*scale, "height": rect.height*scale])
            }
            let children = getAttribute(element, kAXChildrenAttribute) as? [AXUIElement] ?? []
            for child in children { walk(child, depth+1) }
        }
        walk(axWindow, 0)
        observationID = UUID().uuidString; appID = bundleID; windowID = window.windowID; frame = window.frame
        let axApp = AXUIElementCreateApplication(window.owningApplication!.processID)
        focusedElement = getAttribute(axApp, kAXFocusedUIElementAttribute)
        selectedRange = getSelection(focusedElement)
        imageWidth = max(1, Int(frame.width*scale)); imageHeight = max(1, Int(frame.height*scale)); image = png
        return ["observation_id": observationID, "app": bundleID, "text": String(text.joined(separator: "\n").prefix(8000)), "width": imageWidth, "height": imageHeight, "elements": descriptions, "png": png]
    }
    func postKey(_ code: CGKeyCode, _ flags: CGEventFlags = []) {
        for down in [true, false] {
            let event = CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: down)
            event?.flags = flags; event?.post(tap: .cghidEventTap)
        }
    }
    func click(_ point: CGPoint) {
        for kind in [CGEventType.leftMouseDown, CGEventType.leftMouseUp] {
            CGEvent(mouseEventSource: nil, mouseType: kind, mouseCursorPosition: point, mouseButton: .left)?.post(tap: .cghidEventTap)
        }
    }
    func perform(_ request: [String: Any]) async throws -> [String: Any] {
        actionStarted = false
        let operation = request["operation"] as? String ?? ""
        let runID = request["owner"] as? String ?? ""
        if operation == "ping" { return ["text":"helper ready"] }
        if operation == "release" { release(runID); return [:] }
        if operation == "acquire" { try acquire(runID); return [:] }
        try checkPermissions()
        if operation == "check" { return ["text": "permissions available"] }
        try acquire(runID)
        let bundleID = request["app"] as? String ?? ""
        guard !bundleID.isEmpty else { try fail("app bundle ID is required") }
        if operation == "open_app" {
            guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else { try fail("application not found") }
            let config = NSWorkspace.OpenConfiguration(); config.activates = true
            actionStarted = true
            let app = try await NSWorkspace.shared.openApplication(at: url, configuration: config)
            app.activate(options: [])
            return try await observe(bundleID)
        }
        if operation == "observe" { return try await observe(bundleID) }
        let (window, _) = try await getWindow(bundleID)
        guard request["observation_id"] as? String == observationID, !observationID.isEmpty,
              bundleID == appID, window.windowID == windowID, window.frame == frame else { try fail("stale_observation: observe the app again") }
        let elementID = request["element_id"] as? Int ?? 0
        guard elementID >= 0 && elementID <= elements.count else { try fail("invalid element_id") }
        if elementID > 0 {
            guard getFrame(elements[elementID-1]) == elementFrames[elementID-1], getLabel(elements[elementID-1]) == elementLabels[elementID-1] else { try fail("stale_observation: element changed") }
        } else if operation == "click" {
            guard try await capture(window) == image else { try fail("stale_observation: screen changed") }
        }
        if elementID == 0 && (operation == "type_text" || operation == "press_key") {
            let axApp = AXUIElementCreateApplication(window.owningApplication!.processID)
            let focused = getAttribute(axApp, kAXFocusedUIElementAttribute)
            let sameFocus = focused == nil && focusedElement == nil || (focused != nil && focusedElement != nil && CFEqual(focused!, focusedElement!))
            let range = getSelection(focused)
            guard sameFocus && range == selectedRange else { try fail("stale_observation: keyboard focus or selection changed") }
        }
        guard NSWorkspace.shared.frontmostApplication?.bundleIdentifier == bundleID else { try fail("stale_observation: frontmost app changed") }
        switch operation {
        case "click":
            actionStarted = true
            if elementID > 0 {
                let result = AXUIElementPerformAction(elements[elementID-1], kAXPressAction as CFString)
                if result == .actionUnsupported || result == .notImplemented {
                    let rect = elementFrames[elementID-1]; click(CGPoint(x: rect.midX, y: rect.midY))
                } else if result != .success { try fail("AX click failed: \(result.rawValue)") }
            } else {
                guard let x = request["x"] as? Double, let y = request["y"] as? Double, x >= 0 && y >= 0 && x < Double(imageWidth) && y < Double(imageHeight) else { try fail("coordinates outside screenshot") }
                click(CGPoint(x: frame.minX+x*frame.width/Double(imageWidth), y: frame.minY+y*frame.height/Double(imageHeight)))
            }
        case "type_text":
            guard let value = request["text"] as? String, value.utf16.count <= 10000 else { try fail("text is required, maximum 10000 characters") }
            if elementID > 0 {
                let result = AXUIElementSetAttributeValue(elements[elementID-1], kAXFocusedAttribute as CFString, kCFBooleanTrue)
                guard result == .success else { try fail("cannot focus text element") }
            }
            actionStarted = true
            let chars = Array(value.utf16)
            for down in [true, false] {
                let event = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: down)
                // 上一个快捷键可能留下 Command 标志；普通文字必须清空修饰键。
                event?.flags = []
                event?.keyboardSetUnicodeString(stringLength: chars.count, unicodeString: chars); event?.post(tap: .cghidEventTap)
            }
        case "press_key":
            let key = request["key"] as? String ?? ""
            let codes: [String: CGKeyCode] = ["Enter":36,"Tab":48,"Escape":53,"Backspace":51,"ArrowLeft":123,"ArrowRight":124,"ArrowDown":125,"ArrowUp":126,"Command+A":0,"Command+C":8,"Command+V":9,"Command+S":1,"Command+Z":6,"Command+N":45]
            guard let code = codes[key] else { try fail("unsupported desktop key") }
            actionStarted = true
            postKey(code, key.hasPrefix("Command+") ? .maskCommand : [])
        case "scroll":
            let dx = request["delta_x"] as? Int32 ?? 0, dy = request["delta_y"] as? Int32 ?? 0
            actionStarted = true
            let point = CGPoint(x: frame.midX, y: frame.midY)
            CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: point, mouseButton: .left)?.post(tap: .cghidEventTap)
            CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 2, wheel1: -dy, wheel2: -dx, wheel3: 0)?.post(tap: .cghidEventTap)
        default: try fail("unknown desktop operation")
        }
        return try await observe(bundleID)
    }
}

@main struct ComputerHelper {
    @MainActor static func main() async {
        // 命令行程序也要先连接窗口服务，避免首次截图触发 CGS_REQUIRE_INIT 崩溃。
        _ = NSApplication.shared
        if CommandLine.arguments.contains("--request-permissions") {
            let options = [kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary
            _ = AXIsProcessTrustedWithOptions(options); _ = CGRequestScreenCaptureAccess(); return
        }
        let desktop = Desktop()
        while let line = readLine() {
            do {
                guard let data = line.data(using: .utf8), let request = try JSONSerialization.jsonObject(with: data) as? [String: Any] else { try fail("invalid request") }
                let reply = try await desktop.perform(request)
                let bytes = try JSONSerialization.data(withJSONObject: reply, options: [.sortedKeys])
                print(String(decoding: bytes, as: UTF8.self)); fflush(stdout)
            } catch {
                let message = (error as? ComputerError)?.message ?? error.localizedDescription
                let bytes = try! JSONSerialization.data(withJSONObject: ["error":message,"outcome_unknown":desktop.actionStarted])
                print(String(decoding: bytes, as: UTF8.self)); fflush(stdout)
            }
        }
        desktop.release(desktop.owner)
    }
}
