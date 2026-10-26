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
    @Published var harvestPhase: HarvestPhase = .harvested
    @Published var appearances: [String: AgentAppearance] = [:]
    @Published var usagePreviews: [String: UsagePreview] = [:]
    @Published var adoptions: [String: AdoptionPreview] = [:]
    @Published private(set) var queueEmptyPreview = false
    @Published private(set) var shellState: ShellState = .ready
    private var snapshotFarmID: String? = ProjectFarm.samples[0].id
    private var restoration: ShellSelection?
    private var nextAdoptionSerial = 1

    var shellShowsContent: Bool {
        (shellState == .ready || shellState.retainsSnapshot) && snapshotFarmID == selectedFarmID
    }
    var visibleProjection: FixtureProjection? { shellShowsContent ? projection : nil }

    private func rememberSelection() {
        guard shellShowsContent,
              let task = (farm.tasks + farm.queuedTasks).first(where: { $0.key == selectedTaskKey }),
              farm.agents.contains(where: { $0.id == selectedAgentID }) else { return }
        restoration = ShellSelection(farmID: farm.id, taskID: task.id, taskKey: task.key,
            agentID: selectedAgentID, inspector: inspectorSelection, inspectorPresented: inspectorPresented)
    }

    func selectShellState(_ state: ShellState) {
        rememberSelection()
        shellState = state
        if state == .ready {
            snapshotFarmID = selectedFarmID
            if let saved = restoration, saved.isValid(in: farm) {
                selectedTaskKey = saved.taskKey
                selectedAgentID = saved.agentID
                inspectorSelection = saved.inspector
                inspectorPresented = saved.inspectorPresented
            } else {
                select(farm.task(for: sampleState.fixtureTaskKey))
            }
        } else if !state.retainsSnapshot || snapshotFarmID != selectedFarmID {
            snapshotFarmID = nil
            selectedTaskKey = ""
            selectedAgentID = ""
            inspectorPresented = false
            inspectorSelection = .task
            // Loading may restore a validated selection; terminal empty/denied/
            // unsupported responses invalidate it and cannot reveal old details.
            if state != .loading { restoration = nil }
        }
    }

    func previewAdoption(role: AgentRole, state: SampleState) {
        guard adoptions[selectedFarmID] == nil else { return }
        let base = ProjectFarm.samples.first(where: { $0.id == selectedFarmID }) ?? ProjectFarm.samples[0]
        let serial = nextAdoptionSerial
        nextAdoptionSerial += 1
        let name: String
        let title: String
        switch role {
        case .builder: name = "Clover"; title = "Repair a fence corner"
        case .planner: name = "Pip"; title = "Plan the orchard rows"
        case .reviewer: name = "Wren"; title = "Review the seed ledger"
        }
        let agent = FarmAgent(id: "\(base.id)-adopt-\(serial)", name: name, role: role, usage: AgentUsageSample(outputTokens: nil, attemptID: nil))
        let allowedState: SampleState = [.running, .waiting, .blocked, .reviewNeeded].contains(state) ? state : .waiting
        let task = FarmTask(key: "adoption-\(serial)", id: "\(base.code)-P\(String(format: "%03d", serial))", title: title, plotTitle: title, summary: "An independent open work item for this adoption preview.", baseState: allowedState, agentID: agent.id, kind: .task)
        adoptions[base.id] = AdoptionPreview(agent: agent, task: task)
        select(task)
        open(.agent)
    }
    func removeAdoptionPreview() {
        if let previous = adoptions.removeValue(forKey: selectedFarmID) {
            appearances.removeValue(forKey: previous.agent.id)
            usagePreviews.removeValue(forKey: previous.agent.id)
            if selectedAgentID == previous.agent.id { select(farm.task(for: sampleState.fixtureTaskKey)) }
        }
    }
    var selectedUsagePreview: UsagePreview {
        get { usagePreviews[selectedAgentID] ?? .recorded }
        set { usagePreviews[selectedAgentID] = newValue }
    }
    func usage(for agent: FarmAgent) -> AgentUsageSample {
        (usagePreviews[agent.id] ?? .recorded).usage(for: agent, code: farm.code)
    }

    var activeTasks: [FarmTask] { shellShowsContent ? farm.activeTasks(state: sampleState, harvest: harvestPhase) : [] }
    func appearance(for agent: FarmAgent) -> AgentAppearance { appearances[agent.id] ?? .roleDefault(agent) }
    func preview(_ appearance: AgentAppearance, for agent: FarmAgent) { appearances[agent.id] = appearance }
    func restoreAppearance(for agent: FarmAgent) { appearances.removeValue(forKey: agent.id) }
    func setQueuePreviewEmpty(_ empty: Bool) {
        queueEmptyPreview = empty
        if empty && sampleState == .accepted {
            harvestPhase = .fallow
            if selectedTaskKey == "next" { select(farm.task(for: "primary")) }
        }
    }
    func setHarvestPhase(_ phase: HarvestPhase) {
        harvestPhase = phase == .nextOpen && farm.nextOpenTask == nil ? .fallow : phase
        if harvestPhase == .nextOpen, let queued = farm.nextOpenTask { select(queued) }
    }
    func advanceHarvestPreview() {
        guard sampleState == .accepted else { return }
        if harvestPhase == .harvested { harvestPhase = .fallow }
        else if let queued = farm.nextOpenTask { harvestPhase = .nextOpen; select(queued) }
        else { harvestPhase = .fallow }
    }

    var farm: ProjectFarm {
        let base = ProjectFarm.samples.first(where: { $0.id == selectedFarmID }) ?? ProjectFarm.samples[0]
        return base.withQueuedWork(queueEmptyPreview ? [] : base.queuedTasks).adding(adoptions[selectedFarmID])
    }
    var projection: FixtureProjection { FixtureProjection.make(farm: farm, state: sampleState, selectedTaskKey: selectedTaskKey) }

    func select(_ task: FarmTask) {
        guard shellShowsContent, (farm.tasks + farm.queuedTasks).contains(where: { $0.id == task.id && $0.agentID == task.agentID }) else { return }
        selectedTaskKey = task.key
        selectedAgentID = task.agentID
        open(.task)
    }
    func open(_ selection: InspectorSelection) {
        guard shellShowsContent else { return }
        inspectorSelection = selection
        inspectorPresented = true
    }
    func selectFarm(_ farm: ProjectFarm) {
        guard ProjectFarm.samples.contains(where: { $0.id == farm.id }), farm.id != selectedFarmID else { return }
        selectedFarmID = farm.id
        snapshotFarmID = nil
        restoration = nil
        selectedTaskKey = ""
        selectedAgentID = ""
        inspectorPresented = false
        inspectorSelection = .task
        if shellState == .ready { selectShellState(.ready) }
    }
    func selectState(_ state: SampleState) {
        guard shellShowsContent else { return }
        sampleState = state
        harvestPhase = .harvested
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
                .environment(\.colorScheme, .light)
                .preferredColorScheme(.dark)
                // Native title text must use a dark-bar appearance independently
                // of the parchment content's light color scheme.
                .toolbarBackground(FarmStyle.darkWood, for: .windowToolbar)
                .toolbarBackground(.visible, for: .windowToolbar)
                .toolbarColorScheme(.dark, for: .windowToolbar)
        }
        .windowResizability(.contentMinSize)
        .defaultSize(width: 1440, height: 900)
        .commands {
            CommandMenu("Fixture") {
                Picker("Shell availability (synthetic)", selection: Binding(get: { session.shellState }, set: { session.selectShellState($0) })) {
                    ForEach(ShellState.allCases) { state in Text(state.title).tag(state) }
                }
                Divider()
                Picker("Sample state", selection: Binding(get: { session.sampleState }, set: { session.selectState($0) })) {
                    ForEach(SampleState.allCases) { state in Text(state.title).tag(state) }
                }.disabled(!session.shellShowsContent)
                Divider()
                Toggle("High contrast", isOn: $session.highContrast)
                Toggle("Farm animation", isOn: $session.allowMotion)
                Picker("Selected agent output sample", selection: Binding(get: { session.selectedUsagePreview }, set: { session.selectedUsagePreview = $0 })) {
                    ForEach(UsagePreview.allCases) { usage in Text(usage.rawValue).tag(usage) }
                }
                Picker("Accepted land preview", selection: Binding(get: { session.harvestPhase }, set: { session.setHarvestPhase($0) })) {
                    ForEach(HarvestPhase.allCases) { phase in Text(phase.rawValue).tag(phase) }
                }.disabled(session.sampleState != .accepted)
                Toggle("Empty queued backlog", isOn: Binding(get: { session.queueEmptyPreview }, set: { session.setQueuePreviewEmpty($0) }))
                Divider()
                Button("Compact window (1024×768)") { resizeFixtureWindow(width: 1024, height: 768) }
                Button("Wide window (1440×900)") { resizeFixtureWindow(width: 1440, height: 900) }
            }
            CommandMenu("Farm") {
                Button("Show farm") { session.listMode = false }.keyboardShortcut("1", modifiers: .command)
                Button("Open task journal") { session.listMode = true }.keyboardShortcut("2", modifiers: .command)
                Button("Toggle details") { if session.shellShowsContent { session.inspectorPresented.toggle() } }.keyboardShortcut("i", modifiers: [.command, .option])
            }
        }
    }

    private func resizeFixtureWindow(width: CGFloat, height: CGFloat) {
        // Accessibility-driven menu actions can arrive while the preview is not
        // key. Use this app's known principal window, or its sole visible one;
        // never guess among multiple windows when neither key nor main is known.
        let candidates = NSApp.windows.filter { $0.isVisible && $0.canBecomeKey && $0.canBecomeMain }
        let window: NSWindow?
        if let keyWindow = NSApp.keyWindow, candidates.contains(where: { $0 === keyWindow }) {
            window = keyWindow
        } else if let mainWindow = NSApp.mainWindow, candidates.contains(where: { $0 === mainWindow }) {
            window = mainWindow
        } else {
            window = candidates.count == 1 ? candidates[0] : nil
        }
        guard let window else { return }
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
    @Environment(\.scenePhase) private var scenePhase
    @FocusState private var focusedTaskID: String?

    private var farm: ProjectFarm { session.farm }
    private var palette: FarmPalette { session.highContrast || contrast == .increased ? .garden.highContrast : .garden }

    var body: some View {
        HStack(spacing: 0) {
            farmRail.frame(width: 174)
            VStack(spacing: 0) {
                toolbar
                if session.shellShowsContent {
                    HStack(spacing: 0) {
                        VStack(spacing: 0) {
                            if session.shellState.retainsSnapshot { retainedSnapshotBanner }
                            statusStrip
                            if session.listMode { journalList } else {
                                FarmCanvas(session: session, palette: palette, focusedTaskID: $focusedTaskID, motionAllowed: session.allowMotion && !reduceMotion && scenePhase == .active, artifactRender: artifactRender) { selection in
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
                            NativeInspector(session: session, farm: farm, sampleState: session.sampleState, projection: session.projection, selection: session.inspectorSelection, selectedAgentID: session.selectedAgentID, palette: palette, onClose: closeInspector, onAgentSelected: { session.selectedAgentID = session.projection.agent.id; session.open(.agent) })
                                .frame(width: 320)
                        }
                    }
                } else {
                    ShellAvailabilityView(state: session.shellState, hasSnapshot: false) { session.selectShellState(.ready) }
                }
                footer
            }
        }
        .background(FarmStyle.darkWood)
        .foregroundStyle(palette.ink)
        .tint(palette.creek)
        .environment(\.colorScheme, .light)
        .onChange(of: session.shellState) { _, _ in focusedTaskID = nil }
        .onChange(of: session.selectedFarmID) { _, _ in focusedTaskID = nil }
        .onChange(of: session.inspectorPresented) { _, presented in
            guard !presented, session.shellShowsContent else { return }
            focusedTaskID = nil
            DispatchQueue.main.async { focusedTaskID = (session.activeTasks.first(where: { $0.key == session.selectedTaskKey }) ?? session.activeTasks.first)?.id }
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
                .accessibilityLabel("\(project.title), \(project.projectName), synthetic project" + (session.shellShowsContent && project.id == session.selectedFarmID ? ", \(project.attentionLabel)" : ""))
            }
            Divider().overlay(FarmStyle.lightWood.opacity(0.3)).padding(17)
            Group {
                railLink("Task journal", symbol: "book.closed", shortcut: "⌘2", selected: session.listMode) { session.listMode = true }
                railLink("Notice board", symbol: "note.text", shortcut: "1", selected: session.inspectorSelection == .notice && session.inspectorPresented) { session.open(.notice) }
                railLink("Wardrobe & shop", symbol: "tshirt", shortcut: "", selected: session.inspectorSelection == .wardrobe && session.inspectorPresented) { session.open(.wardrobe) }
                railLink("Storehouse", symbol: "shippingbox", shortcut: "", selected: session.inspectorSelection == .storehouse && session.inspectorPresented) { session.open(.storehouse) }
            }.disabled(!session.shellShowsContent)
            Spacer(minLength: 12)
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 5) {
                    Image(systemName: "eye").font(.system(size: 11))
                    Text(session.shellShowsContent ? "1 task needs review" : "Attention unavailable").font(.system(size: 11, weight: .semibold))
                }.foregroundStyle(FarmStyle.gold)
                Text(session.shellShowsContent ? "Synthetic attention\nOpen the notice board." : "No task details loaded.")
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
            .disabled(!session.shellShowsContent)
            .help("Toggle details (⌥⌘I)").accessibilityLabel("Toggle details inspector")
        }
        .padding(.horizontal, 15).padding(.vertical, 12)
        .background { TimberSurface() }
        .overlay(alignment: .bottom) { Rectangle().fill(FarmStyle.darkWood).frame(height: 3) }
    }

    private var retainedSnapshotBanner: some View {
        Label(session.shellState.message, systemImage: session.shellState.symbol)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(FarmStyle.ink).padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(FarmStyle.cream)
            .accessibilityElement(children: .combine)
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
                Button { session.open(.wardrobe) } label: {
                    Label("Wardrobe & shop preview", systemImage: "tshirt").font(.system(size: 10, weight: .semibold)).foregroundStyle(FarmStyle.cream)
                }.buttonStyle(.plain)
            }
            HStack(spacing: 7) {
                ForEach(farm.agents) { agent in
                    let task = farm.currentTask(agentID: agent.id, state: session.sampleState, harvest: session.harvestPhase)
                    let state = FixtureProjection.taskStatus(farm: farm, state: session.sampleState, taskKey: task.key)
                    let appearance = session.appearance(for: agent)
                    let usage = session.usage(for: agent)
                    Button {
                        session.selectedAgentID = agent.id
                        session.selectedTaskKey = task.key
                        session.open(.agent)
                    } label: {
                        VStack(alignment: .leading, spacing: 5) {
                            HStack(spacing: 6) {
                                PixelAnimal(species: appearance.species, skin: appearance.skin).frame(width: 30, height: 36)
                                    .background(Color(hex: 0xD5B77E)).overlay { Rectangle().strokeBorder(FarmStyle.soil, lineWidth: 1) }
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(agent.name).font(.system(size: 11, weight: .bold)).lineLimit(1)
                                    Label(agent.role.rawValue, systemImage: agent.role.symbol).font(.system(size: 9, weight: .semibold)).lineLimit(1)
                                }
                                Spacer(minLength: 0)
                            }
                            Label(state.title, systemImage: state.symbol).font(.system(size: 9, weight: .semibold)).fixedSize(horizontal: false, vertical: true)
                            Text(usage.outputTokens.map { "\(usage.growth?.rawValue ?? "") · \($0.formatted(.number.notation(.compactName))) sample out" } ?? "Output usage unavailable")
                                .font(.system(size: 9)).lineLimit(2).fixedSize(horizontal: false, vertical: true)
                        }
                        .foregroundStyle(palette.ink).padding(7).frame(maxWidth: .infinity, alignment: .leading)
                        .background(agent.id == session.selectedAgentID ? FarmStyle.cream : Color(hex: 0xDCC18B))
                        .modifier(BevelFrame(selected: agent.id == session.selectedAgentID))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("\(agent.name), \(agent.role.rawValue), \(state.title), \(usage.caption), growth \(usage.growth?.rawValue ?? "unavailable"). Open agent details.")
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
                Text("\(session.activeTasks.count) open entries").font(.system(size: 11, weight: .medium)).foregroundStyle(FarmStyle.muted).padding(.top, 8)
            }.padding(24)
            HStack {
                Text("Task").frame(maxWidth: .infinity, alignment: .leading)
                Text("State").frame(width: 110, alignment: .leading)
            }.font(.system(size: 11, weight: .semibold)).foregroundStyle(FarmStyle.muted).padding(.horizontal, 24).padding(.bottom, 9)
            if artifactRender {
                VStack(spacing: 0) {
                    ForEach(session.activeTasks) { task in
                        journalRow(task).padding(.horizontal, 12)
                        Rectangle().fill(FarmStyle.honey.opacity(0.4)).frame(height: 1).padding(.horizontal, 24)
                    }
                    Spacer(minLength: 0)
                }
            } else {
                List(session.activeTasks) { task in
                    journalRow(task)
                        .listRowBackground(Color.clear).listRowSeparatorTint(FarmStyle.honey.opacity(0.4))
                }
                .listStyle(.plain).scrollContentBackground(.hidden)
            }
            if session.sampleState == .accepted {
                Button { session.open(.storehouse) } label: {
                    Label("1 completed delivery in the Storehouse", systemImage: "archivebox")
                        .font(.system(size: 12, weight: .semibold)).padding(.horizontal, 24).padding(.vertical, 8)
                }.buttonStyle(.plain)
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
                            Text("\(task.kind.rawValue) \(task.id) · Open").font(.system(size: 10, weight: .bold, design: .monospaced)).foregroundStyle(FarmStyle.muted)
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
                .accessibilityLabel("Open \(task.kind.rawValue) \(task.id), \(task.title), \(state.title). Open task details.")
    }

    private var footer: some View {
        HStack {
            Image(systemName: session.shellState == .ready && session.sampleState == .disconnected ? "wifi.slash" : session.shellState.symbol)
            if session.shellState == .ready {
                Text(session.sampleState == .disconnected ? "Synthetic · last sample 10:38 · provider unavailable" : "Synthetic workspace · Ready · snapshot 10:42")
            } else {
                Text("Synthetic workspace · \(session.shellState.title)" + (session.shellState.retainsSnapshot && session.shellShowsContent ? " · last snapshot 10:42 sample time" : ""))
            }
            Spacer()
            Text("Open work and harvest history")
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
