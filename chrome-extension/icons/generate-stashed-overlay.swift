#!/usr/bin/env swift

// One-shot icon generator. Reads each `iconNN.png` in this directory
// and writes a sibling `iconNN-stashed.png` with a small green
// circle + white checkmark composited into the bottom-right corner.
// Run once when the source icons change; output PNGs are committed
// and loaded directly by background.js via chrome.action.setIcon.
//
// Usage:
//   cd chrome-extension/icons
//   swift generate-stashed-overlay.swift

import AppKit
import CoreGraphics

let sizes = [16, 48, 128]
// Use CWD rather than #filePath — `swift script.swift` from a shell
// reports a relative path for #filePath which strips to an empty
// dirname. The Bash invocation `cd icons && swift ...` lands CWD
// here, which is what we want.
let here = FileManager.default.currentDirectoryPath

// Apple system green. Matches the badge color we'd otherwise set
// via setBadgeBackgroundColor — keep the two in sync if either
// moves.
let greenR: CGFloat = 0x34 / 255.0
let greenG: CGFloat = 0xC7 / 255.0
let greenB: CGFloat = 0x59 / 255.0

func render(size: Int) {
    let source = "\(here)/icon\(size).png"
    let dest = "\(here)/icon\(size)-stashed.png"

    guard let img = NSImage(contentsOfFile: source) else {
        print("missing: \(source)")
        return
    }

    let pixel = CGSize(width: size, height: size)
    let colorSpace = CGColorSpaceCreateDeviceRGB()
    guard let ctx = CGContext(
        data: nil,
        width: size,
        height: size,
        bitsPerComponent: 8,
        bytesPerRow: 0,
        space: colorSpace,
        bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
    ) else {
        print("context failed: \(size)")
        return
    }

    // Draw the base icon. Use the NSImage's representation that
    // matches our target size if available; fall back to the
    // largest. drawInRect with .copy gives us a fresh pixel copy
    // without compositing artifacts from any background.
    let rect = CGRect(origin: .zero, size: pixel)
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(cgContext: ctx, flipped: false)
    img.draw(in: rect, from: .zero, operation: .copy, fraction: 1.0)
    NSGraphicsContext.restoreGraphicsState()

    // Badge geometry. Circle covers ~50% of the icon on the 16px
    // size (any smaller and the check disappears in the toolbar);
    // ~38% on the larger sizes where there's room to breathe.
    let badgePct: CGFloat = size <= 16 ? 0.56 : 0.42
    let badgeSize = floor(CGFloat(size) * badgePct)
    let pad: CGFloat = size <= 16 ? 0 : floor(CGFloat(size) * 0.03)
    let badgeRect = CGRect(
        x: CGFloat(size) - badgeSize - pad,
        y: pad,
        width: badgeSize,
        height: badgeSize
    )

    // White halo behind the circle so it reads cleanly against
    // dark briefcases (and against Chrome's dark toolbar too).
    // 1.5px on all sizes feels right; on 16px it shows as a thin
    // anti-aliased rim, on 128px it's a crisp outline.
    let haloInset: CGFloat = -max(CGFloat(size) / 64.0, 0.5)
    ctx.setFillColor(red: 1, green: 1, blue: 1, alpha: 1)
    ctx.fillEllipse(in: badgeRect.insetBy(dx: haloInset, dy: haloInset))

    // Green fill.
    ctx.setFillColor(red: greenR, green: greenG, blue: greenB, alpha: 1)
    ctx.fillEllipse(in: badgeRect)

    // White checkmark — two-segment polyline. Drawn in
    // badge-local coordinates then offset to badgeRect.origin so
    // the same proportions work at every size.
    let s = badgeSize
    let stroke: CGFloat = max(s * 0.18, 1.0)
    ctx.setStrokeColor(red: 1, green: 1, blue: 1, alpha: 1)
    ctx.setLineWidth(stroke)
    ctx.setLineCap(.round)
    ctx.setLineJoin(.round)
    let p1 = CGPoint(x: badgeRect.minX + s * 0.27, y: badgeRect.minY + s * 0.52)
    let p2 = CGPoint(x: badgeRect.minX + s * 0.45, y: badgeRect.minY + s * 0.34)
    let p3 = CGPoint(x: badgeRect.minX + s * 0.73, y: badgeRect.minY + s * 0.66)
    ctx.beginPath()
    ctx.move(to: p1)
    ctx.addLine(to: p2)
    ctx.addLine(to: p3)
    ctx.strokePath()

    guard let cgOut = ctx.makeImage() else {
        print("makeImage failed: \(size)")
        return
    }
    let rep = NSBitmapImageRep(cgImage: cgOut)
    rep.size = pixel
    guard let pngData = rep.representation(using: .png, properties: [:]) else {
        print("png encode failed: \(size)")
        return
    }
    let url = URL(fileURLWithPath: dest)
    do {
        try pngData.write(to: url)
        print("wrote \(dest)")
    } catch {
        print("write failed: \(dest): \(error)")
    }
}

for s in sizes { render(size: s) }
