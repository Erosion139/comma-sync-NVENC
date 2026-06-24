import AppKit

let S: CGFloat = 1024
let img = NSImage(size: NSSize(width: S, height: S))
img.lockFocus()
let ctx = NSGraphicsContext.current!.cgContext

// Rounded "squircle" tile with a blue gradient (matches the app's accent car).
let tile = CGRect(x: 0, y: 0, width: S, height: S)
let clip = CGPath(roundedRect: tile, cornerWidth: S * 0.225, cornerHeight: S * 0.225, transform: nil)
ctx.addPath(clip); ctx.clip()
let cs = CGColorSpaceCreateDeviceRGB()
let grad = CGGradient(colorsSpace: cs,
    colors: [NSColor(red: 0.20, green: 0.55, blue: 1.0, alpha: 1).cgColor,
             NSColor(red: 0.06, green: 0.28, blue: 0.80, alpha: 1).cgColor] as CFArray,
    locations: [0, 1])!
ctx.drawLinearGradient(grad, start: CGPoint(x: 0, y: S), end: CGPoint(x: S, y: 0), options: [])

// Helper: draw an SF Symbol tinted solid white into a rect.
func drawWhiteSymbol(_ name: String, in rect: NSRect, pointSize: CGFloat) {
    guard let base = NSImage(systemSymbolName: name, accessibilityDescription: nil) else { return }
    let conf = NSImage.SymbolConfiguration(pointSize: pointSize, weight: .semibold)
    guard let sym = base.withSymbolConfiguration(conf) else { return }
    let white = NSImage(size: sym.size)
    white.lockFocus()
    sym.draw(in: NSRect(origin: .zero, size: sym.size))
    NSColor.white.set()
    NSRect(origin: .zero, size: sym.size).fill(using: .sourceAtop)
    white.unlockFocus()
    // fit, preserving aspect, centered in rect
    let a = sym.size.width / sym.size.height
    var w = rect.width, h = w / a
    if h > rect.height { h = rect.height; w = h * a }
    let dst = NSRect(x: rect.midX - w/2, y: rect.midY - h/2, width: w, height: h)
    white.draw(in: dst)
}

// Car on the left.
drawWhiteSymbol("car.side.fill", in: NSRect(x: S*0.10, y: S*0.36, width: S*0.56, height: S*0.30), pointSize: 400)

// A comma (the comma.ai mark) to the right: a round head + a tapering curved tail.
NSColor.white.setFill()
let cx = S * 0.78, cy = S * 0.555
let r: CGFloat = S * 0.072

// Head: full disc.
NSBezierPath(ovalIn: NSRect(x: cx - r, y: cy - r, width: 2*r, height: 2*r)).fill()

// Tail: sweeps from under the head down to a sharp point at lower-left.
let tail = NSBezierPath()
let tl = NSPoint(x: cx - r*0.62, y: cy + r*0.1)   // top-left, joins head
let tr = NSPoint(x: cx + r*0.82, y: cy - r*0.15)  // top-right, joins head
let pt = NSPoint(x: cx - r*0.35, y: cy - r*3.05)  // sharp point
tail.move(to: tl)
tail.line(to: tr)
tail.curve(to: pt,                                 // outer (right) edge, curving inward
           controlPoint1: NSPoint(x: cx + r*1.05, y: cy - r*1.8),
           controlPoint2: NSPoint(x: cx + r*0.15, y: cy - r*2.85))
tail.curve(to: tl,                                 // inner (left) edge back up
           controlPoint1: NSPoint(x: cx - r*1.0, y: cy - r*1.7),
           controlPoint2: NSPoint(x: cx - r*0.92, y: cy - r*0.5))
tail.close()
tail.fill()

img.unlockFocus()

// Write PNG.
let tiff = img.tiffRepresentation!
let rep = NSBitmapImageRep(data: tiff)!
let png = rep.representation(using: .png, properties: [:])!
try! png.write(to: URL(fileURLWithPath: "/tmp/commasync/icon_1024.png"))
print("wrote /tmp/commasync/icon_1024.png")
