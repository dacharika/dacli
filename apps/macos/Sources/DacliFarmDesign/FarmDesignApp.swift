import AppKit
import SwiftUI

@main
struct DacliFarmDesignApp: App {
    var body: some Scene {
        WindowGroup {
            FarmDesignView()
                .frame(minWidth: 1024, minHeight: 660)
                .preferredColorScheme(.light)
        }
        .windowResizability(.contentMinSize)
        .defaultSize(width: 1280, height: 820)
        .commands {
            CommandMenu("Fixture") {
                Button("Compact window (1024×768)") {
                    resizeKeyWindow(width: 1024, height: 768)
                }
                Button("Wide window (1440×900)") {
                    resizeKeyWindow(width: 1440, height: 900)
                }
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
        } else {
            frame.origin.y += window.frame.height - height
        }
        window.setFrame(frame, display: true, animate: false)
    }
}

struct FarmDesignView: View {
    @Environment(\.accessibilityReduceMotion) private var accessibilityReduceMotion
    @Environment(\.colorSchemeContrast) private var systemContrast
    @State private var selectedFarmID = ProjectFarm.samples[0].id
    @State private var selectedTaskKey = "primary"
    @State private var selectedAgentID = ProjectFarm.samples[0].agents[0].id
    @State private var sampleState: SampleState = .running
    @State private var inspectorSelection: InspectorSelection? = .task
    @State private var inspectorPresented = true
    @State private var listMode = false
    @State private var highContrast = false
    @State private var allowMotion = true
    @FocusState private var focusedTaskID: String?

    private var farm: ProjectFarm {
        ProjectFarm.samples.first(where: { $0.id == selectedFarmID }) ?? ProjectFarm.samples[0]
    }

    private var projection: FixtureProjection {
        FixtureProjection.make(farm: farm, state: sampleState, selectedTaskKey: selectedTaskKey)
    }

    private var palette: FarmPalette {
        highContrast || systemContrast == .increased ? FarmPalette.garden.highContrast : FarmPalette.garden
    }

    private var motionAllowed: Bool {
        allowMotion && !accessibilityReduceMotion
    }

    var body: some View {
        NavigationSplitView {
            projectSidebar
        } detail: {
            detailAndInspector
        }
        .navigationSplitViewStyle(.balanced)
        .preferredColorScheme(.light)
        .tint(palette.creek)
        .background(palette.paper)
        .onChange(of: selectedFarmID) { _, newValue in
            let nextFarm = ProjectFarm.samples.first(where: { $0.id == newValue }) ?? ProjectFarm.samples[0]
            selectedTaskKey = sampleState.fixtureTaskKey
            selectedAgentID = nextFarm.task(for: selectedTaskKey).agentID
            inspectorSelection = .task
            inspectorPresented = true
        }
        .onChange(of: sampleState) { _, newState in
            selectedTaskKey = newState.fixtureTaskKey
            let currentTask = farm.task(for: selectedTaskKey)
            selectedAgentID = currentTask.agentID
            inspectorSelection = .task
        }
        .animation(motionAllowed ? .easeInOut(duration: 0.16) : nil, value: selectedTaskKey)
        .animation(motionAllowed ? .easeInOut(duration: 0.16) : nil, value: selectedFarmID)
    }

    private var detailAndInspector: some View {
        HSplitView {
            detailColumn
                .frame(minWidth: 440, maxWidth: .infinity, maxHeight: .infinity)
            if inspectorPresented {
                NativeInspector(
                    farm: farm,
                    sampleState: sampleState,
                    projection: projection,
                    selection: inspectorSelection ?? .task,
                    selectedAgentID: selectedAgentID,
                    palette: palette,
                    onClose: closeInspector,
                    onAgentSelected: {
                        inspectorSelection = .agent
                        inspectorPresented = true
                    }
                )
                .frame(minWidth: 270, idealWidth: 285, maxWidth: 320, maxHeight: .infinity)
            }
        }
        .frame(minWidth: inspectorPresented ? 715 : 440, maxWidth: .infinity, maxHeight: .infinity)
    }

    private var projectSidebar: some View {
        List(selection: $selectedFarmID) {
            Section("Project farms") {
                ForEach(ProjectFarm.samples) { project in
                    HStack(spacing: 10) {
                        ZStack {
                            RoundedRectangle(cornerRadius: 7)
                                .fill(project.id == "wildflower" ? Color(red: 0.87, green: 0.92, blue: 0.79) : Color(red: 0.85, green: 0.90, blue: 0.91))
                            Image(systemName: project.id == "wildflower" ? "camera.macro" : "moon.stars.fill")
                                .foregroundStyle(palette.creek)
                        }
                        .frame(width: 32, height: 32)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(project.title)
                                .font(.system(size: 13, weight: .semibold))
                                .lineLimit(1)
                                .minimumScaleFactor(0.9)
                            Text(project.projectName)
                                .font(.system(size: 12))
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .minimumScaleFactor(0.9)
                        }
                        Spacer(minLength: 2)
                        Text("\(project.attentionCount)")
                            .font(.system(size: 12, weight: .bold, design: .monospaced))
                            .foregroundStyle(palette.ink)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 3)
                            .background(palette.gold.opacity(0.42), in: Capsule())
                            .accessibilityLabel(project.attentionLabel)
                    }
                    .tag(project.id)
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("\(project.title), \(project.projectName), \(project.attentionLabel)")
                }
            }
        }
        .listStyle(.sidebar)
        .navigationSplitViewColumnWidth(min: 180, ideal: 200, max: 260)
        .navigationTitle("Patchwork Farms")
        .safeAreaInset(edge: .bottom) {
            HStack(spacing: 6) {
                Image(systemName: "leaf.fill").foregroundStyle(palette.creek)
                Text("Two synthetic project farms")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(palette.ink.opacity(0.78))
            }
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var detailColumn: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 13) {
                    statusSummary
                    if listMode {
                        accessibleTaskList
                    } else {
                        sceneHeader
                        FarmCanvas(
                            farm: farm,
                            sampleState: sampleState,
                            palette: palette,
                            selectedTaskKey: $selectedTaskKey,
                            selectedAgentID: $selectedAgentID,
                            focusedTaskID: $focusedTaskID,
                            onInspect: openInspector
                        )
                    }
                    noticeAndStorehouse
                }
                .padding(18)
                .frame(maxWidth: 1120, alignment: .topLeading)
                .frame(maxWidth: .infinity, alignment: .topLeading)
            }
            .frame(maxHeight: .infinity)
        }
        .background(Color(red: 0.92, green: 0.95, blue: 0.92))
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 12) {
            headerTitle
            HStack(spacing: 8) {
                samplePicker
                viewPicker
                Spacer(minLength: 0)
                displayMenu
                inspectorToggle
            }
            .controlSize(.small)
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 14)
        .background(palette.paper)
    }

    private var headerTitle: some View {
        HStack(alignment: .top, spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                Text("\(farm.title) Farm")
                    .font(.system(size: 23, weight: .bold))
                    .foregroundStyle(palette.ink)
                Text(farm.projectName)
                    .font(.system(size: 12))
                    .foregroundStyle(palette.ink.opacity(0.78))
            }
            Spacer(minLength: 0)
            Label("Synthetic preview", systemImage: "circle.dotted")
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(palette.ink.opacity(0.78))
                .fixedSize()
                .padding(.top, 5)
                .help("Local sample data. This preview is not connected to a live workspace.")
        }
    }

    private var samplePicker: some View {
        HStack(spacing: 5) {
            Text("Sample")
                .font(.system(size: 12, weight: .medium))
                .foregroundStyle(palette.ink)
            Picker("Sample state", selection: $sampleState) {
                ForEach(SampleState.allCases) { state in
                    Text(state.title).tag(state)
                }
            }
            .labelsHidden()
            .frame(width: 124)
        }
        .help("Changes the local synthetic snapshot only.")
    }

    private var viewPicker: some View {
        Picker("Farm view", selection: $listMode) {
            Text("Garden").tag(false)
            Text("List").tag(true)
        }
        .labelsHidden()
        .pickerStyle(.segmented)
        .frame(width: 118)
        .help("Show task plots or the equivalent task list.")
    }

    private var displayMenu: some View {
        Menu {
            Toggle("High contrast", isOn: $highContrast)
            Toggle("Selection motion", isOn: $allowMotion)
            Divider()
            Text("macOS accessibility settings take precedence")
        } label: {
            Image(systemName: "slider.horizontal.3")
                .frame(width: 22, height: 24)
        }
        .menuIndicator(.hidden)
        .menuStyle(.borderlessButton)
        .frame(width: 28, height: 28)
        .help("Display options")
        .accessibilityLabel("Display options")
    }

    private var inspectorToggle: some View {
        Button {
            if inspectorPresented {
                closeInspector()
            } else {
                openInspector(.task)
            }
        } label: {
            Image(systemName: "sidebar.right")
                .frame(width: 28, height: 26)
        }
        .buttonStyle(.borderless)
        .help(inspectorPresented ? "Hide details" : "Show details")
        .accessibilityLabel(inspectorPresented ? "Hide details inspector" : "Show details inspector")
    }

    private var statusSummary: some View {
        HStack(spacing: 10) {
            Image(systemName: projection.status.symbol)
                .font(.system(size: 11, weight: .bold))
                .foregroundStyle(palette.ink)
                .frame(width: 25, height: 25)
                .background(statusColor(projection.status).opacity(0.25), in: RoundedRectangle(cornerRadius: 6))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 3) {
                Text(projection.summary)
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(palette.ink)
                    .fixedSize(horizontal: false, vertical: true)
                Text(projection.freshness)
                    .font(.system(size: 11))
                    .foregroundStyle(palette.ink.opacity(0.78))
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 5)
            Text(projection.status.title)
                .font(.system(size: 12, weight: .bold))
                .foregroundStyle(palette.ink)
                .padding(.horizontal, 9)
                .padding(.vertical, 5)
                .background(statusColor(projection.status).opacity(0.25), in: Capsule())
                .fixedSize()
        }
        .padding(.horizontal, 11)
        .padding(.vertical, 8)
        .background(palette.paper, in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(palette.outline.opacity(0.18)))
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(projection.summary). \(projection.status.title). \(projection.freshness). Synthetic sample.")
    }

    private var sceneHeader: some View {
        HStack {
            Label(farm.projectName, systemImage: "mappin.and.ellipse")
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(palette.ink)
            Spacer()
            Label("Running", systemImage: "play.fill").labelStyle(.titleAndIcon).foregroundStyle(palette.creek)
            Label("Waiting", systemImage: "pause.fill").labelStyle(.titleAndIcon).foregroundStyle(Color(red: 0.50, green: 0.38, blue: 0.14))
            Label("Review", systemImage: "eye.fill").labelStyle(.titleAndIcon).foregroundStyle(Color(red: 0.49, green: 0.30, blue: 0.43))
        }
        .font(.system(size: 11, weight: .medium))
        .padding(.horizontal, 3)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(farm.projectName) project farm. Task legend: running, waiting, review.")
    }

    private var accessibleTaskList: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Text("Tasks in \(farm.title)")
                        .font(.system(size: 17, weight: .bold))
                        .foregroundStyle(palette.ink)
                    Text("Equivalent task navigation · synthetic sample")
                        .font(.system(size: 12))
                        .foregroundStyle(palette.ink.opacity(0.78))
                }
                Spacer()
                Label("List view", systemImage: "list.bullet")
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(palette.creek)
            }
            List(farm.tasks) { task in
                let item = FixtureProjection.make(farm: farm, state: sampleState, selectedTaskKey: task.key)
                Button {
                    selectTask(task)
                } label: {
                    HStack(spacing: 10) {
                        Text(task.id)
                            .font(.system(size: 11, weight: .bold, design: .monospaced))
                            .foregroundStyle(palette.ink.opacity(0.80))
                            .frame(width: 82, alignment: .leading)
                        Text(task.title)
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(palette.ink)
                            .lineLimit(1)
                        Spacer(minLength: 4)
                        Text(item.status.title)
                            .font(.system(size: 12, weight: .bold))
                            .foregroundStyle(palette.ink)
                            .padding(.horizontal, 9)
                            .padding(.vertical, 4)
                            .background(statusColor(item.status).opacity(0.25), in: Capsule())
                    }
                    .padding(.vertical, 4)
                }
                .buttonStyle(.plain)
                .modifier(TaskKeyboardFocus(taskID: task.id, palette: palette, focusedTaskID: $focusedTaskID) {
                    selectTask(task)
                })
                .accessibilityLabel("\(task.id), \(task.title), \(item.status.title). Select for task and evidence details.")
            }
            .listStyle(.inset)
            .frame(minHeight: 240, maxHeight: 360)
        }
        .padding(14)
        .frame(maxWidth: .infinity, minHeight: 340, alignment: .topLeading)
        .background(palette.paper, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(palette.outline.opacity(0.18)))
    }

    private var noticeAndStorehouse: some View {
        HStack(spacing: 10) {
            SummaryCard(
                eyebrow: "On the notice board",
                title: farm.notice.title,
                detail: farm.notice.message,
                actionTitle: "Read note",
                palette: palette
            ) {
                openInspector(.notice)
            }
            SummaryCard(
                eyebrow: "In the storehouse",
                title: "Owner acceptance",
                detail: "Owner accepted \(farm.acceptance.revision) · sha256:\(farm.acceptance.digest)",
                actionTitle: "View record",
                palette: palette
            ) {
                openInspector(.storehouse)
            }
        }
    }

    private func selectTask(_ task: FarmTask) {
        selectedTaskKey = task.key
        selectedAgentID = task.agentID
        openInspector(.task)
    }

    private func openInspector(_ selection: InspectorSelection) {
        focusedTaskID = nil
        inspectorSelection = selection
        inspectorPresented = true
    }

    private func closeInspector() {
        // Reset the binding before the inspector disappears so selecting the same
        // task again is a new focus request after the split view updates.
        focusedTaskID = nil
        inspectorPresented = false
        DispatchQueue.main.async {
            focusedTaskID = farm.task(for: selectedTaskKey).id
        }
    }
}

private struct SummaryCard: View {
    let eyebrow: String
    let title: String
    let detail: String
    let actionTitle: String
    let palette: FarmPalette
    let action: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(eyebrow).font(.system(size: 12, weight: .semibold)).foregroundStyle(palette.ink.opacity(0.82))
            Text(title).font(.system(size: 13, weight: .bold)).foregroundStyle(palette.ink)
            Text(detail)
                .font(.system(size: 12))
                .foregroundStyle(palette.ink.opacity(0.82))
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
            Button(actionTitle, action: action)
                .font(.system(size: 12, weight: .semibold))
                .buttonStyle(.link)
                .accessibilityHint("Opens synthetic \(title.lowercased()) details.")
        }
        .padding(11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.paper, in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(palette.outline.opacity(0.18)))
    }
}

#Preview {
    FarmDesignView()
        .frame(width: 1280, height: 820)
}
