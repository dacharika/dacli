import AppKit
import SwiftUI

@MainActor
final class FarmSession: ObservableObject {
    @Published var selectedFarmID = ProjectFarm.samples[0].id
    @Published var selectedTaskKey = "primary"
    @Published var selectedAgentID = ProjectFarm.samples[0].agents[0].id
    @Published var sampleState: SampleState = .running
    @Published var inspectorSelection: InspectorSelection = .task
    @Published var inspectorPresented = true
    @Published var listMode = false
    @Published var highContrast = false
    @Published var allowMotion = true

    var farm: ProjectFarm { ProjectFarm.samples.first(where: { $0.id == selectedFarmID }) ?? ProjectFarm.samples[0] }
    var projection: FixtureProjection { FixtureProjection.make(farm: farm, state: sampleState, selectedTaskKey: selectedTaskKey) }

    func select(_ task: FarmTask) {
        selectedTaskKey = task.key
        selectedAgentID = task.agentID
        open(.task)
    }
    func open(_ selection: InspectorSelection) {
        inspectorSelection = selection
        inspectorPresented = true
    }
    func selectFarm(_ farm: ProjectFarm) {
        selectedFarmID = farm.id
        select(farm.task(for: sampleState.fixtureTaskKey))
    }
    func selectState(_ state: SampleState) {
        sampleState = state
        select(farm.task(for: state.fixtureTaskKey))
    }
}

@main
struct DacliFarmDesignApp: App {
    @StateObject private var session = FarmSession()

    init() {
        // A development artifact export, not a screen capture or live-window test.
        // Runs the same pure SwiftUI view without interacting with any native app.
        FarmPreviewExport.runIfRequested()
    }

    var body: some Scene {
        WindowGroup {
            FarmDesignView(session: session)
                .frame(minWidth: 1024, minHeight: 660)
                .preferredColorScheme(.light)
        }
        .windowResizability(.contentMinSize)
        .defaultSize(width: 1440, height: 900)
        .commands {
            CommandMenu("Fixture") {
                Picker("Sample state", selection: Binding(get: { session.sampleState }, set: { session.selectState($0) })) {
                    ForEach(SampleState.allCases) { state in Text(state.title).tag(state) }
                }
                Divider()
                Toggle("High contrast", isOn: $session.highContrast)
                Toggle("Selection motion", isOn: $session.allowMotion)
                Divider()
                Button("Compact window (1024×768)") { resizeKeyWindow(width: 1024, height: 768) }
                Button("Wide window (1440×900)") { resizeKeyWindow(width: 1440, height: 900) }
            }
            CommandMenu("Farm") {
                Button("Show farm") { session.listMode = false }.keyboardShortcut("1", modifiers: .command)
                Button("Open task journal") { session.listMode = true }.keyboardShortcut("2", modifiers: .command)
                Button("Toggle details") { session.inspectorPresented.toggle() }.keyboardShortcut("i", modifiers: [.command, .option])
            }
        }
    }

    private func resizeKeyWindow(width: CGFloat, height: CGFloat) {
        guard let window = NSApp.keyWindow else { return }
        var frame = window.frame
        let visibleFrame = (window.screen ?? NSScreen.main)?.visibleFrame
        frame.size = CGSize(width: width, height: height)
        if let visibleFrame {
            frame.origin.x = max(visibleFrame.minX, min(frame.origin.x, visibleFrame.maxX - frame.width))
            frame.origin.y = max(visibleFrame.minY, min(frame.origin.y, visibleFrame.maxY - frame.height))
        }
        window.setFrame(frame, display: true, animate: false)
    }
}

struct FarmDesignView: View {
    @ObservedObject var session: FarmSession
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.colorSchemeContrast) private var contrast
    @Environment(\.farmArtifactRender) private var artifactRender
    @FocusState private var focusedTaskID: String?

    private var farm: ProjectFarm { session.farm }
    private var palette: FarmPalette { session.highContrast || contrast == .increased ? .garden.highContrast : .garden }

    var body: some View {
        HStack(spacing: 0) {
            farmRail.frame(width: 174)
            VStack(spacing: 0) {
                toolbar
                HStack(spacing: 0) {
                    VStack(spacing: 0) {
                        statusStrip
                        if session.listMode { journalList } else {
                            FarmCanvas(farm: farm, sampleState: session.sampleState, palette: palette, selectedTaskKey: $session.selectedTaskKey, selectedAgentID: $session.selectedAgentID, focusedTaskID: $focusedTaskID) { selection in
                                focusedTaskID = nil
                                session.open(selection)
                            }
                            .padding(10)
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                        }
                        agentInventory
                    }
                    .frame(minWidth: 440, maxWidth: .infinity, maxHeight: .infinity)
                    .background { TimberSurface() }
                    if session.inspectorPresented {
                        NativeInspector(farm: farm, sampleState: session.sampleState, projection: session.projection, selection: session.inspectorSelection, selectedAgentID: session.selectedAgentID, palette: palette, onClose: closeInspector, onAgentSelected: { session.open(.agent) })
                            .frame(width: 320)
                    }
                }
                footer
            }
        }
        .background(FarmStyle.darkWood)
        .foregroundStyle(palette.ink)
        .tint(palette.creek)
        .preferredColorScheme(.light)
        .onChange(of: session.inspectorPresented) { _, presented in
            guard !presented else { return }
            focusedTaskID = nil
            DispatchQueue.main.async { focusedTaskID = farm.task(for: session.selectedTaskKey).id }
        }
        .animation(session.allowMotion && !reduceMotion ? .easeInOut(duration: 0.14) : nil, value: session.listMode)
    }

    private var farmRail: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 7) {
                HStack(spacing: 7) {
                    Image(systemName: "leaf.fill").font(.system(size: 19)).foregroundStyle(FarmStyle.gold)
                    Text("dacli").font(FarmStyle.title(29)).foregroundStyle(FarmStyle.cream)
                }
                Text("A little room to grow.").font(.system(size: 11)).foregroundStyle(Color(hex: 0xD3B98D))
            }.padding(.horizontal, 17).padding(.top, 24).padding(.bottom, 26)
            Text("Your farms").font(FarmStyle.title(13)).foregroundStyle(Color(hex: 0xDEBC82)).padding(.horizontal, 17).padding(.bottom, 12)
            ForEach(ProjectFarm.samples) { project in
                Button {
                    focusedTaskID = nil
                    session.selectFarm(project)
                } label: {
                    HStack(spacing: 8) {
                        Image(systemName: project.id == "wildflower" ? "sun.max.fill" : "moon.stars.fill")
                            .font(.system(size: 15)).frame(width: 21)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(project.title).font(.system(size: 13, weight: .bold))
                            Text(project.projectName).font(.system(size: 11))
                        }
                        Spacer(minLength: 0)
                        if project.id == session.selectedFarmID { Image(systemName: "chevron.right").font(.system(size: 9, weight: .bold)) }
                    }
                    .foregroundStyle(project.id == session.selectedFarmID ? FarmStyle.ink : FarmStyle.cream)
                    .padding(.horizontal, 11).padding(.vertical, 13)
                    .background(project.id == session.selectedFarmID ? FarmStyle.paper : .clear)
                    .overlay(alignment: .leading) { if project.id == session.selectedFarmID { Rectangle().fill(FarmStyle.gold).frame(width: 3) } }
                }.buttonStyle(.plain).padding(.horizontal, 7).padding(.bottom, 5)
                .accessibilityLabel("\(project.title), \(project.projectName), \(project.attentionLabel)")
            }
            Divider().overlay(FarmStyle.lightWood.opacity(0.3)).padding(17)
            railLink("Task journal", symbol: "book.closed", shortcut: "⌘2", selected: session.listMode) { session.listMode = true }
            railLink("Notice board", symbol: "note.text", shortcut: "1", selected: session.inspectorSelection == .notice && session.inspectorPresented) { session.open(.notice) }
            railLink("Storehouse", symbol: "shippingbox", shortcut: "", selected: session.inspectorSelection == .storehouse && session.inspectorPresented) { session.open(.storehouse) }
            Spacer(minLength: 12)
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 5) {
                    Image(systemName: "eye").font(.system(size: 11))
                    Text("1 task needs review").font(.system(size: 11, weight: .semibold))
                }.foregroundStyle(FarmStyle.gold)
                Text("Open the notice board\nto see the evidence.")
                    .font(.system(size: 11)).lineSpacing(3).foregroundStyle(Color(hex: 0xD3B98D))
            }.padding(15)
            Rectangle().fill(FarmStyle.lightWood.opacity(0.25)).frame(height: 1)
            HStack(spacing: 5) {
                Image(systemName: "circle.dotted")
                Text("Synthetic preview")
            }
            .font(.system(size: 10, weight: .medium)).foregroundStyle(Color(hex: 0xD3B98D))
            .padding(.horizontal, 15).padding(.vertical, 14)
            .help("Local read-only sample data. This farm is not connected to a workspace.")
        }
        .frame(maxHeight: .infinity)
        .background { TimberSurface(dark: true) }
        .overlay(alignment: .trailing) { Rectangle().fill(FarmStyle.lightWood.opacity(0.5)).frame(width: 2) }
    }

    private func railLink(_ title: String, symbol: String, shortcut: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Image(systemName: symbol).frame(width: 17)
                Text(title)
                Spacer(minLength: 0)
                Text(shortcut).font(.system(size: 10)).foregroundStyle(FarmStyle.gold)
            }
            .font(.system(size: 12, weight: selected ? .bold : .medium))
            .foregroundStyle(FarmStyle.cream)
            .padding(.horizontal, 15).padding(.vertical, 10)
            .background(selected ? FarmStyle.honey.opacity(0.3) : .clear)
        }.buttonStyle(.plain)
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            Text(farm.title).font(FarmStyle.title(18)).foregroundStyle(FarmStyle.cream)
            Text("/ \(farm.projectName)").font(.system(size: 12)).foregroundStyle(Color(hex: 0xE0C393))
            Spacer(minLength: 6)
            HStack(spacing: 0) {
                Button { session.listMode = false } label: { Label("Farm", systemImage: "leaf").font(.system(size: 12, weight: .semibold)) }
                    .buttonStyle(FarmButtonStyle(selected: !session.listMode))
                Button { session.listMode = true } label: { Label("Journal", systemImage: "book.closed").font(.system(size: 12, weight: .semibold)) }
                    .buttonStyle(FarmButtonStyle(selected: session.listMode))
            }
            Button {
                if session.inspectorPresented { closeInspector() } else { session.open(.task) }
            } label: {
                Image(systemName: "sidebar.right").font(.system(size: 14)).frame(width: 18)
            }.buttonStyle(FarmButtonStyle(selected: session.inspectorPresented))
            .help("Toggle details (⌥⌘I)").accessibilityLabel("Toggle details inspector")
        }
        .padding(.horizontal, 15).padding(.vertical, 12)
        .background { TimberSurface() }
        .overlay(alignment: .bottom) { Rectangle().fill(FarmStyle.darkWood).frame(height: 3) }
    }

    private var statusStrip: some View {
        HStack(spacing: 8) {
            Image(systemName: session.projection.status.symbol)
                .font(.system(size: 12, weight: .bold)).foregroundStyle(statusColor(session.projection.status))
            Text(session.projection.summary).font(.system(size: 12, weight: .semibold)).fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 1)
            StateBadge(state: session.projection.status)
        }
        .foregroundStyle(palette.ink)
        .padding(.horizontal, 13).padding(.vertical, 9)
        .background(palette.paper)
        .overlay(alignment: .bottom) { Rectangle().fill(FarmStyle.honey).frame(height: 1) }
        .accessibilityElement(children: .combine)
    }

    private var agentInventory: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack {
                Text("Farmhands").font(FarmStyle.title(13)).foregroundStyle(FarmStyle.cream)
                Spacer()
                Text("Select an agent to see their work").font(.system(size: 10)).foregroundStyle(Color(hex: 0xE2C99E))
            }
            HStack(spacing: 7) {
                ForEach(farm.agents) { agent in
                    let task = farm.tasks.first(where: { $0.agentID == agent.id }) ?? farm.tasks[0]
                    let state = FixtureProjection.taskStatus(farm: farm, state: session.sampleState, taskKey: task.key)
                    Button {
                        session.selectedAgentID = agent.id
                        session.selectedTaskKey = task.key
                        session.open(.agent)
                    } label: {
                        HStack(spacing: 6) {
                            PixelAnimal(species: agent.species).frame(width: 40, height: 42)
                                .background(Color(hex: 0xD5B77E))
                                .overlay { Rectangle().strokeBorder(FarmStyle.soil, lineWidth: 1) }
                            VStack(alignment: .leading, spacing: 4) {
                                Text(agent.name).font(.system(size: 12, weight: .bold))
                                Label(state.title, systemImage: state.symbol).font(.system(size: 10, weight: .medium)).lineLimit(1)
                            }
                            Spacer(minLength: 0)
                        }
                        .foregroundStyle(palette.ink)
                        .padding(6).frame(maxWidth: .infinity)
                        .background(agent.id == session.selectedAgentID ? FarmStyle.cream : Color(hex: 0xDCC18B))
                        .modifier(BevelFrame(selected: agent.id == session.selectedAgentID))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("\(agent.name), \(agent.species.rawValue), \(state.title). Open agent details.")
                }
            }
        }
        .padding(.horizontal, 11).padding(.top, 2).padding(.bottom, 13)
    }

    private var journalList: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 5) {
                    Text("Task journal").font(FarmStyle.title(26))
                    Text("Every plot has a story. Here is the record.").font(.system(size: 12)).foregroundStyle(FarmStyle.muted)
                }
                Spacer()
                Text("3 entries").font(.system(size: 11, weight: .medium)).foregroundStyle(FarmStyle.muted).padding(.top, 8)
            }.padding(24)
            HStack {
                Text("Task").frame(maxWidth: .infinity, alignment: .leading)
                Text("State").frame(width: 110, alignment: .leading)
            }.font(.system(size: 11, weight: .semibold)).foregroundStyle(FarmStyle.muted).padding(.horizontal, 24).padding(.bottom, 9)
            if artifactRender {
                VStack(spacing: 0) {
                    ForEach(farm.tasks) { task in
                        journalRow(task).padding(.horizontal, 12)
                        Rectangle().fill(FarmStyle.honey.opacity(0.4)).frame(height: 1).padding(.horizontal, 24)
                    }
                    Spacer(minLength: 0)
                }
            } else {
                List(farm.tasks) { task in
                    journalRow(task)
                        .listRowBackground(Color.clear).listRowSeparatorTint(FarmStyle.honey.opacity(0.4))
                }
                .listStyle(.plain).scrollContentBackground(.hidden)
            }
            HStack {
                Image(systemName: "keyboard")
                Text("Tab to a task · Space or Return opens details")
            }.font(.system(size: 11)).foregroundStyle(FarmStyle.muted).padding(24)
        }
        .background { ParchmentSurface() }
        .modifier(BevelFrame()).padding(10)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func journalRow(_ task: FarmTask) -> some View {
                let state = FixtureProjection.taskStatus(farm: farm, state: session.sampleState, taskKey: task.key)
                return Button { select(task) } label: {
                    HStack(alignment: .center, spacing: 10) {
                        Image(systemName: task.key == "review" ? "doc.text.magnifyingglass" : "leaf")
                            .font(.system(size: 19)).foregroundStyle(statusColor(state)).frame(width: 28)
                        VStack(alignment: .leading, spacing: 5) {
                            Text(task.id).font(.system(size: 10, weight: .bold, design: .monospaced)).foregroundStyle(FarmStyle.muted)
                            Text(task.title).font(.system(size: 13, weight: .semibold)).fixedSize(horizontal: false, vertical: true)
                            Text(farm.agent(for: task.agentID).name).font(.system(size: 11)).foregroundStyle(FarmStyle.muted)
                        }
                        Spacer(minLength: 4)
                        StateBadge(state: state).frame(width: 110, alignment: .leading)
                    }
                    .foregroundStyle(palette.ink)
                    .padding(.horizontal, 12).padding(.vertical, 15)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(session.selectedTaskKey == task.key ? FarmStyle.cream : .clear)
                    .overlay(alignment: .leading) { if session.selectedTaskKey == task.key { Rectangle().fill(FarmStyle.honey).frame(width: 3) } }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .modifier(TaskKeyboardFocus(taskID: task.id, palette: palette, focusedTaskID: $focusedTaskID) { select(task) })
                .accessibilityLabel("\(task.id), \(task.title), \(state.title). Open task details.")
    }

    private var footer: some View {
        HStack {
            Image(systemName: session.sampleState == .disconnected ? "wifi.slash" : "clock")
            Text(session.sampleState == .disconnected ? "Last sample · 10:38 · connection unavailable" : "Snapshot · 10:42")
            Spacer()
            Text("Farm actions select records")
        }
        .font(.system(size: 10, weight: .medium)).foregroundStyle(Color(hex: 0xE0C393))
        .padding(.horizontal, 13).padding(.vertical, 7)
        .background(FarmStyle.darkWood)
    }
    private func select(_ task: FarmTask) {
        focusedTaskID = nil
        session.select(task)
    }
    private func closeInspector() {
        focusedTaskID = nil
        session.inspectorPresented = false
    }
}

extension FarmStyle { static let soil = Color(hex: 0x885A36) }
