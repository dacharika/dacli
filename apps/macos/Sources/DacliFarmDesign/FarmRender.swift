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
                ("disconnected-compact", 1024, 746, .disconnected, false, .task),
                ("wardrobe-wide", 1440, 878, .running, false, .wardrobe),
                ("skins-wide", 1440, 878, .running, false, .wardrobe),
                ("adoption-compact", 1024, 746, .running, false, .agent),
                ("fallow-compact", 1024, 746, .accepted, false, .storehouse),
                ("next-issue-wide", 1440, 878, .accepted, false, .task),
                ("motion-frame-b", 1440, 878, .running, false, .task),
                ("empty-backlog-compact", 1024, 746, .accepted, false, .storehouse),
                ("blocked-wide", 1440, 878, .blocked, false, .agent)
            ]
            for (name, width, height, state, list, selection) in variants {
                let session = FarmSession()
                session.selectState(state)
                session.listMode = list
                session.inspectorSelection = selection
                if name == "skins-wide" { session.preview(AgentAppearance(species: .owl, skin: .owl), for: session.farm.agents[0]) }
                if name == "adoption-compact" { session.previewAdoption(role: .planner, state: .running) }
                if name == "fallow-compact" { session.harvestPhase = .fallow }
                if name == "next-issue-wide", let queued = session.farm.nextOpenTask { session.harvestPhase = .nextOpen; session.select(queued) }
                if name == "empty-backlog-compact" { session.setQueuePreviewEmpty(true) }
                let view = FarmDesignView(session: session).frame(width: width, height: height).environment(\.colorScheme, .light).environment(\.farmArtifactRender, true).environment(\.farmRenderSkins, name == "skins-wide").environment(\.farmRenderTime, name == "motion-frame-b" ? 6 : 2)
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
