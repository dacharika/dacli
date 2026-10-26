import SwiftUI

enum InspectorSelection: String, Identifiable {
    case task, agent, notice, storehouse, wardrobe
    var id: String { rawValue }
}

struct NativeInspector: View {
    @ObservedObject var session: FarmSession
    let farm: ProjectFarm
    let sampleState: SampleState
    let projection: FixtureProjection
    let selection: InspectorSelection
    let selectedAgentID: String
    let palette: FarmPalette
    let onClose: () -> Void
    let onAgentSelected: () -> Void
    @Environment(\.farmArtifactRender) private var artifactRender

    private var agent: FarmAgent { farm.agent(for: selectedAgentID) }
    private var title: String {
        switch selection {
        case .task: "Task journal"
        case .agent: "Farmhand"
        case .notice: "Notice board"
        case .storehouse: "Storehouse"
        case .wardrobe: "Wardrobe & shop"
        }
    }
    private var symbol: String {
        switch selection {
        case .task: "book.closed"
        case .agent: "person.crop.square"
        case .notice: "note.text"
        case .storehouse: "shippingbox"
        case .wardrobe: "tshirt"
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Image(systemName: symbol).font(.system(size: 14))
                Text(title).font(FarmStyle.title(16))
                Spacer()
                Button(action: onClose) {
                    Image(systemName: "xmark").font(.system(size: 11, weight: .bold)).frame(width: 26, height: 26)
                        .background(Color(hex: 0xE3C58E))
                        .overlay { Rectangle().strokeBorder(FarmStyle.honey, lineWidth: 1) }
                }.buttonStyle(.plain).accessibilityLabel("Close details").help("Close details")
            }
            .foregroundStyle(palette.ink)
            .padding(.horizontal, 19).padding(.vertical, 13)
            .background(Color(hex: 0xE8CF98))
            Rectangle().fill(FarmStyle.honey).frame(height: 1)
            if artifactRender {
                GeometryReader { geometry in
                    pageContents.frame(width: geometry.size.width, height: geometry.size.height, alignment: .topLeading).clipped()
                }
            } else {
                ScrollView { pageContents }
            }
            HStack(spacing: 5) {
                Image(systemName: "lock").font(.system(size: 10))
                Text("Read-only records").font(.system(size: 10, weight: .medium))
                Spacer()
                Text(farm.code).font(.system(size: 10, weight: .bold, design: .monospaced))
            }
            .foregroundStyle(FarmStyle.muted).padding(.horizontal, 21).padding(.vertical, 12)
            .overlay(alignment: .top) { Rectangle().fill(FarmStyle.honey.opacity(0.5)).frame(height: 1) }
        }
        .foregroundStyle(palette.ink)
        .background { ParchmentSurface() }
        .overlay(alignment: .leading) {
            HStack(spacing: 0) {
                Rectangle().fill(FarmStyle.darkWood).frame(width: 3)
                Rectangle().fill(FarmStyle.honey).frame(width: 2)
                Rectangle().fill(FarmStyle.wood.opacity(0.18)).frame(width: 4)
            }.allowsHitTesting(false)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(title) details inspector")
    }

    private var pageContents: some View {
        VStack(alignment: .leading, spacing: 19) {
            if selection == .task { taskPage }
            if selection == .agent { agentPage }
            if selection == .notice { noticePage }
            if selection == .storehouse { storehousePage }
            if selection == .wardrobe { FarmWardrobe(session: session) }
        }
        .padding(.horizontal, 23).padding(.top, 23).padding(.bottom, 25)
        .frame(maxWidth: .infinity, alignment: .leading)
        .fixedSize(horizontal: false, vertical: true)
    }

    private var taskPage: some View {
        Group {
            HStack {
                reference("\(projection.task.kind.rawValue) \(projection.task.id)")
                Spacer(minLength: 2)
                StateBadge(state: projection.status)
            }
            Text(projection.task.title).font(FarmStyle.title(23)).lineSpacing(3).fixedSize(horizontal: false, vertical: true)
            Text(projection.description).font(.system(size: 13)).lineSpacing(4).fixedSize(horizontal: false, vertical: true)
            rule
            Button(action: onAgentSelected) {
                HStack(spacing: 11) {
                    PixelAnimal(species: session.appearance(for: projection.agent).species, skin: session.appearance(for: projection.agent).skin).frame(width: 42, height: 42)
                        .background(Color(hex: 0xE5CE9B)).overlay { Rectangle().strokeBorder(FarmStyle.honey, lineWidth: 1) }
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Assigned \(projection.agent.role.rawValue.lowercased())").font(.system(size: 10, weight: .medium)).foregroundStyle(FarmStyle.muted)
                        Text(projection.agent.name).font(.system(size: 13, weight: .bold))
                    }
                    Spacer()
                    Image(systemName: "chevron.right").font(.system(size: 11, weight: .semibold)).foregroundStyle(FarmStyle.muted)
                }.contentShape(Rectangle())
            }.buttonStyle(.plain).accessibilityLabel("Assigned agent \(projection.agent.name). Open agent details.")
            rule
            evidence(projection.evidence)
            rule
            activity(projection.activity)
            if let acceptance = projection.ownerAcceptance { acceptanceStamp(acceptance) }
        }
    }

    private var agentPage: some View {
        Group {
            HStack(alignment: .center, spacing: 15) {
                PixelAnimal(species: session.appearance(for: agent).species, skin: session.appearance(for: agent).skin).frame(width: 72, height: 70)
                    .background(Color(hex: 0xE3CD98)).modifier(BevelFrame())
                VStack(alignment: .leading, spacing: 5) {
                    Text(agent.name).font(FarmStyle.title(25))
                    Label(agent.role.rawValue, systemImage: agent.role.symbol).font(.system(size: 12)).foregroundStyle(FarmStyle.muted)
                }
            }
            reference(agent.id)
            if agent.id.contains("-adopt-") {
                Text("Adoption preview · new agent").font(.system(size: 11, weight: .semibold)).foregroundStyle(FarmStyle.muted)
            }
            usagePanel
            Button { session.open(.wardrobe) } label: {
                Label("Open wardrobe & shop preview", systemImage: "tshirt").font(.system(size: 12, weight: .semibold))
            }.buttonStyle(.bordered).controlSize(.small)
            let task = farm.currentTask(agentID: agent.id, state: sampleState, harvest: session.harvestPhase)
            Group {
                let state = FixtureProjection.taskStatus(farm: farm, state: sampleState, taskKey: task.key)
                StateBadge(state: state)
                rule
                heading(state == .accepted ? "Harvested delivery" : "Current task", symbol: "leaf")
                reference("\(task.kind.rawValue) \(task.id) · \(state == .accepted ? "Done" : "Open")")
                Text(task.title).font(FarmStyle.title(19)).fixedSize(horizontal: false, vertical: true)
                Text(task.summary).font(.system(size: 13)).lineSpacing(4).fixedSize(horizontal: false, vertical: true)
            }
            rule
            Text("\(agent.role.rawValue) defaults to a \(agent.species.rawValue.lowercased()). Skins change appearance without changing this agent’s role or work. Adopt a new animal to add another agent.")
                .font(.system(size: 12)).foregroundStyle(FarmStyle.muted).lineSpacing(3)
        }
    }

    private var noticePage: some View {
        Group {
            StateBadge(state: .reviewNeeded)
            Text(farm.notice.title).font(FarmStyle.title(25)).fixedSize(horizontal: false, vertical: true)
            reference(farm.notice.id)
            Text(farm.notice.message).font(.system(size: 13)).lineSpacing(4)
            rule
            evidence([
                FarmEvidence(id: farm.notice.taskID, title: "Task", detail: farm.notice.taskID),
                FarmEvidence(id: farm.notice.attemptID, title: "Attempt", detail: farm.notice.attemptID)
            ])
            rule
            heading("Awaiting review", symbol: "eye")
            Text("The delivery has evidence attached. A review decision and owner acceptance are separate records.")
                .font(.system(size: 13)).lineSpacing(4).fixedSize(horizontal: false, vertical: true)
        }
    }

    private var storehousePage: some View {
        Group {
            Text("The harvest record").font(FarmStyle.title(25))
            Text("Owner acceptance belongs to a specific revision and digest.")
                .font(.system(size: 13)).lineSpacing(4).fixedSize(horizontal: false, vertical: true)
            acceptanceStamp(farm.acceptance)
            heading("Harvested work", symbol: "archivebox")
            reference("\(farm.task(for: "primary").kind.rawValue) \(farm.task(for: "primary").id) · Done")
            Text(farm.task(for: "primary").title).font(.system(size: 13, weight: .semibold))
            Text("Completed work stays in history. A new open issue uses a different ID when it takes the freed land.").font(.system(size: 12)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
            rule
            heading("Review receipt", symbol: "doc.text.magnifyingglass")
            reference(farm.acceptance.reviewReceipt)
            Text("A separate review record. A review receipt alone does not mean the owner accepted the delivery.")
                .font(.system(size: 12)).lineSpacing(4).foregroundStyle(FarmStyle.muted)
            rule
            reference(farm.acceptance.id)
        }
    }

    private var usagePanel: some View {
        let usage = session.usage(for: agent)
        return VStack(alignment: .leading, spacing: 8) {
            heading("Growth from output", symbol: "leaf")
            Text(usage.growth?.rawValue ?? "Growth unavailable").font(FarmStyle.title(19))
            Text(usage.caption).font(.system(size: 12, weight: .semibold))
            if let attempt = usage.attemptID {
                reference(attempt)
                Text("Attributed output in this sample. Growth is cosmetic; it does not grant a role, permission, or acceptance.")
                    .font(.system(size: 11)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
            } else {
                Text("No attributed output sample is available. A neutral sprite is shown; no growth stage is inferred.")
                    .font(.system(size: 11)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
            }
        }.padding(12).background(Color(hex: 0xE5D9AA)).overlay { Rectangle().strokeBorder(FarmStyle.honey.opacity(0.6), lineWidth: 1) }
    }

    private func evidence(_ items: [FarmEvidence]) -> some View {
        VStack(alignment: .leading, spacing: 13) {
            heading("Evidence", symbol: "paperclip")
            ForEach(items) { item in
                HStack(alignment: .top, spacing: 9) {
                    Image(systemName: "doc.text").font(.system(size: 15)).foregroundStyle(FarmStyle.wood).padding(.top, 2)
                    VStack(alignment: .leading, spacing: 4) {
                        Text(item.title).font(.system(size: 12, weight: .bold))
                        Text(item.detail).font(.system(size: 11)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
                            .textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            Text("Illustrative references").font(.system(size: 10)).foregroundStyle(FarmStyle.muted)
        }
    }
    private func activity(_ items: [(title: String, detail: String, isComplete: Bool)]) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            heading("Activity", symbol: "clock")
            ForEach(Array(items.enumerated()), id: \.offset) { entry in
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: entry.element.isComplete ? "checkmark.circle.fill" : "circle")
                        .font(.system(size: 11)).foregroundStyle(palette.creek).padding(.top, 2)
                    VStack(alignment: .leading, spacing: 4) {
                        Text(entry.element.title).font(.system(size: 12, weight: .semibold)).fixedSize(horizontal: false, vertical: true)
                        Text(entry.element.detail).font(.system(size: 10)).foregroundStyle(FarmStyle.muted).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
        }
    }
    private func acceptanceStamp(_ acceptance: OwnerAcceptance) -> some View {
        VStack(alignment: .leading, spacing: 9) {
            Label("Owner accepted", systemImage: "checkmark.seal").font(FarmStyle.title(17)).foregroundStyle(palette.creek)
            Text(acceptance.revision).font(.system(size: 14, weight: .bold, design: .monospaced))
            Text("sha256:\(acceptance.digest)").font(.system(size: 11, design: .monospaced)).textSelection(.enabled)
            Text("Revision and digest are bound to this record.").font(.system(size: 11)).foregroundStyle(FarmStyle.muted)
        }
        .padding(14).frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(hex: 0xE4DBAA))
        .overlay { Rectangle().strokeBorder(palette.creek.opacity(0.6), style: StrokeStyle(lineWidth: 1, dash: [4, 3])) }
    }
    private func heading(_ title: String, symbol: String) -> some View {
        Label(title, systemImage: symbol).font(FarmStyle.title(14)).foregroundStyle(palette.ink)
    }
    private func reference(_ text: String) -> some View {
        Text(text).font(.system(size: 11, weight: .semibold, design: .monospaced)).foregroundStyle(FarmStyle.muted).textSelection(.enabled)
    }
    private var rule: some View { Rectangle().fill(FarmStyle.honey.opacity(0.42)).frame(height: 1) }
}
