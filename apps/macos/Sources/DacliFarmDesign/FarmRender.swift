import AppKit
import SwiftUI

// Deterministic native view rendering for development artifact review. These PNGs
// are not actual window captures and do not verify focus or operating-system UI.
@MainActor
enum FarmPreviewExport {
    static func runIfRequested() {
        #if DEBUG
        let arguments = CommandLine.arguments
        guard let index = arguments.firstIndex(of: "--render-preview"), arguments.count > index + 1 else { return }
        let directory = URL(fileURLWithPath: arguments[index + 1], isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let variants: [(String, CGFloat, CGFloat, SampleState, Bool, InspectorSelection)] = [
                ("farm-wide", 1440, 878, .running, false, .task),
                ("farm-compact", 1024, 746, .running, false, .task),
                ("journal-wide", 1440, 878, .reviewNeeded, true, .task),
                ("agent-wide", 1440, 878, .waiting, false, .agent),
                ("acceptance-wide", 1440, 878, .accepted, false, .storehouse),
                ("disconnected-compact", 1024, 746, .disconnected, false, .task)
            ]
            for (name, width, height, state, list, selection) in variants {
                let session = FarmSession()
                session.selectState(state)
                session.listMode = list
                session.inspectorSelection = selection
                let view = FarmDesignView(session: session).frame(width: width, height: height).environment(\.colorScheme, .light).environment(\.farmArtifactRender, true)
                let renderer = ImageRenderer(content: view)
                renderer.scale = 2
                guard let image = renderer.nsImage,
                      let tiff = image.tiffRepresentation,
                      let representation = NSBitmapImageRep(data: tiff),
                      let data = representation.representation(using: .png, properties: [:]) else {
                    throw NSError(domain: "FarmPreviewExport", code: 1, userInfo: [NSLocalizedDescriptionKey: "Could not render \(name)"])
                }
                try data.write(to: directory.appendingPathComponent("\(name).png"))
                print("Native view render: \(directory.appendingPathComponent(name).path).png")
            }
            exit(0)
        } catch {
            fputs("Preview export failed: \(error)\n", stderr)
            exit(1)
        }
        #endif
    }
}
