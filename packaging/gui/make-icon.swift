#!/usr/bin/env swift
// Draws the wxdl app icon (squircle + download glyph) at every size macOS wants
// and packs the result into an .icns with iconutil.
//
//   swift packaging/gui/make-icon.swift [output.icns]

import AppKit
import Foundation

let output = CommandLine.arguments.count > 1
    ? URL(fileURLWithPath: CommandLine.arguments[1])
    : URL(fileURLWithPath: "build/AppIcon.icns")
let iconset = output.deletingLastPathComponent().appendingPathComponent("AppIcon.iconset")

let top = NSColor(srgbRed: 0.30, green: 0.47, blue: 1.00, alpha: 1)
let bottom = NSColor(srgbRed: 0.48, green: 0.23, blue: 0.90, alpha: 1)
let gradient = NSGradient(starting: top, ending: bottom)!

func drawArt() {
    let squircle = NSBezierPath(
        roundedRect: NSRect(x: 100, y: 100, width: 824, height: 824),
        xRadius: 185, yRadius: 185)

    NSGraphicsContext.saveGraphicsState()
    let shadow = NSShadow()
    shadow.shadowColor = NSColor.black.withAlphaComponent(0.28)
    shadow.shadowBlurRadius = 24
    shadow.shadowOffset = NSSize(width: 0, height: -12)
    shadow.set()
    bottom.setFill()
    squircle.fill()
    NSGraphicsContext.restoreGraphicsState()

    gradient.draw(in: squircle, angle: -62)

    // Soft top light so the tile reads as glass, not flat paint.
    NSGraphicsContext.saveGraphicsState()
    squircle.addClip()
    let gloss = NSGradient(colors: [
        NSColor.white.withAlphaComponent(0.20),
        NSColor.white.withAlphaComponent(0.0),
    ])!
    gloss.draw(in: NSRect(x: 100, y: 480, width: 824, height: 444), angle: -90)
    NSGraphicsContext.restoreGraphicsState()

    let disc = NSBezierPath(ovalIn: NSRect(x: 244, y: 244, width: 536, height: 536))
    NSColor.white.setFill()
    disc.fill()

    let glyph = NSBezierPath()
    glyph.appendRoundedRect(
        NSRect(x: 446, y: 440, width: 132, height: 236), xRadius: 52, yRadius: 52)
    let head = NSBezierPath()
    head.move(to: NSPoint(x: 512, y: 388))
    head.line(to: NSPoint(x: 372, y: 512))
    head.line(to: NSPoint(x: 652, y: 512))
    head.close()
    glyph.append(head)
    glyph.appendRoundedRect(
        NSRect(x: 390, y: 288, width: 244, height: 58), xRadius: 29, yRadius: 29)
    glyph.windingRule = .nonZero

    gradient.draw(in: glyph, angle: -62)
}

func render(pixels: Int) -> NSBitmapImageRep {
    let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    let context = NSGraphicsContext(bitmapImageRep: rep)!
    NSGraphicsContext.current = context
    context.cgContext.setShouldAntialias(true)
    context.cgContext.interpolationQuality = .high
    context.cgContext.scaleBy(x: CGFloat(pixels) / 1024, y: CGFloat(pixels) / 1024)
    drawArt()
    NSGraphicsContext.restoreGraphicsState()
    return rep
}

let variants: [(String, Int)] = [
    ("icon_16x16", 16),
    ("icon_16x16@2x", 32),
    ("icon_32x32", 32),
    ("icon_32x32@2x", 64),
    ("icon_128x128", 128),
    ("icon_128x128@2x", 256),
    ("icon_256x256", 256),
    ("icon_256x256@2x", 512),
    ("icon_512x512", 512),
    ("icon_512x512@2x", 1024),
]

try? FileManager.default.removeItem(at: iconset)
try FileManager.default.createDirectory(at: iconset, withIntermediateDirectories: true)

for (name, pixels) in variants {
    let rep = render(pixels: pixels)
    guard let data = rep.representation(using: .png, properties: [:]) else {
        fatalError("PNG encode failed for \(name)")
    }
    try data.write(to: iconset.appendingPathComponent("\(name).png"))
}

try FileManager.default.createDirectory(
    at: output.deletingLastPathComponent(), withIntermediateDirectories: true)

let iconutil = Process()
iconutil.executableURL = URL(fileURLWithPath: "/usr/bin/iconutil")
iconutil.arguments = ["-c", "icns", iconset.path, "-o", output.path]
try iconutil.run()
iconutil.waitUntilExit()
guard iconutil.terminationStatus == 0 else {
    fatalError("iconutil failed with status \(iconutil.terminationStatus)")
}
print(output.path)
