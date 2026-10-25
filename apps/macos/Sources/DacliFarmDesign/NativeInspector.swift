import SwiftUI

enum InspectorSelection: String, Identifiable {
    case task
    case agent
    case notice
    case storehouse

    var id: String { rawValue }
}

struct NativeInspector: View {
    let farm: ProjectFarm
    let sampleState: SampleState
    let projection: FixtureProjection
    let selection: InspectorSelection
    let selectedAgentID: String
    let palette: FarmPalette
    let onClose: () -> Void
    let onAgentSelected: () -> Void

    private var selectedAgent: FarmAgent {
        farm.agent(for: selectedAgentID)
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 12) {
                VStack(alignment: .leading, spacing: 5) {
                    Text(eyebrow)
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(palette.creek)
                    Text(title)
                        .font(.system(size: 18, weight: .bold))
                        .fixedSize(horizontal: false, vertical: true)
                        .foregroundStyle(palette.ink)
                }
                Spacer(minLength: 0)
                Button(action: onClose) {
                    Image(systemName: "xmark")
                        .font(.system(size: 12, weight: .bold))
                        .frame(width: 30, height: 30)
                }
                .buttonStyle(.bordered)
                .help("Close inspector")
                .accessibilityLabel("Close details")
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)
            .background(palette.paper)

            Divider()

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    identityRow
                    Divider()
                    if selection == .task {
                        taskContents
                    } else if selection == .agent {
                        agentContents
                    } else if selection == .notice {
                        noticeContents
                    } else {
                        storehouseContents
                    }
                    authorityNote
                }
                .padding(16)
            }
            .background(palette.paper)

            Divider()
            HStack {
                Text("Read-only sample")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(palette.ink.opacity(0.78))
                Spacer()
                Text(farm.projectName)
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(palette.ink)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .background(palette.paper)
        }
        .frame(minWidth: 270, idealWidth: 305, maxWidth: 340)
        .background(palette.paper)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Task and evidence inspector")
    }

    private var eyebrow: String {
        switch selection {
        case .task: "Task plot"
        case .agent: "Agent identity"
        case .notice: "Notice board"
        case .storehouse: "Storehouse"
        }
    }

    private var title: String {
        switch selection {
        case .task: projection.task.title
        case .agent: selectedAgent.name
        case .notice: farm.notice.title
        case .storehouse: "Owner acceptance recorded"
        }
    }

    @ViewBuilder
    private var identityRow: some View {
        HStack(alignment: .center) {
            Text(selection == .task ? projection.task.id : selection == .notice ? farm.notice.id : selection == .storehouse ? farm.acceptance.id : selectedAgent.id)
                .font(.system(size: 12, weight: .bold, design: .monospaced))
                .foregroundStyle(palette.ink)
                .textSelection(.enabled)
            Spacer()
            Label(selection == .notice ? "Review needed" : selection == .storehouse ? "Owner accepted" : (selection == .agent ? agentStatus.title : projection.status.title), systemImage: statusSymbol)
                .font(.system(size: 12, weight: .semibold))
                .padding(.horizontal, 9)
                .padding(.vertical, 5)
                .foregroundStyle(palette.ink)
                .background(statusColor(selection == .notice ? .reviewNeeded : selection == .storehouse ? .accepted : selection == .agent ? agentStatus : projection.status).opacity(0.22), in: Capsule())
        }
        .accessibilityElement(children: .combine)
    }

    private var statusSymbol: String {
        switch selection {
        case .notice: SampleState.reviewNeeded.symbol
        case .storehouse: SampleState.accepted.symbol
        case .agent: agentStatus.symbol
        case .task: projection.status.symbol
        }
    }

    private var agentStatus: SampleState {
        guard let task = farm.tasks.first(where: { $0.agentID == selectedAgent.id }) else { return .waiting }
        return FixtureProjection.make(farm: farm, state: sampleState, selectedTaskKey: task.key).status
    }

    private var taskContents: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(projection.description)
                .font(.system(size: 13))
                .foregroundStyle(palette.ink)
                .fixedSize(horizontal: false, vertical: true)

            VStack(alignment: .leading, spacing: 8) {
                sectionHeading("Assigned agent")
                Button(action: onAgentSelected) {
                    HStack(spacing: 10) {
                        PixelAnimal(species: projection.agent.species)
                            .frame(width: 36, height: 36)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(projection.agent.name).font(.system(size: 13, weight: .semibold))
                            Text(projection.agent.species.rawValue)
                                .font(.system(size: 12))
                                .foregroundStyle(palette.ink.opacity(0.78))
                        }
                        Spacer()
                        Image(systemName: "chevron.right")
                            .font(.system(size: 11, weight: .semibold))
                    }
                    .padding(9)
                    .background(.white.opacity(0.75), in: RoundedRectangle(cornerRadius: 8))
                    .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.outline.opacity(0.25)))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Assigned agent \(projection.agent.name), \(projection.agent.species.rawValue). Select agent details.")
            }

            evidenceSection(projection.evidence)
            activitySection(projection.activity)
        }
    }

    private var agentContents: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("\(selectedAgent.species.rawValue) agent · \(selectedAgent.id)")
                .font(.system(size: 13, weight: .semibold))
                .foregroundStyle(palette.ink)
                .textSelection(.enabled)
            Text("Species, name, and appearance are cosmetic. They do not set permissions or capabilities.")
                .font(.system(size: 13))
                .foregroundStyle(palette.ink)
                .fixedSize(horizontal: false, vertical: true)
            if let task = farm.tasks.first(where: { $0.agentID == selectedAgent.id }) {
                VStack(alignment: .leading, spacing: 5) {
                    sectionHeading("Related task")
                    Text("\(task.id) · \(task.title)")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(palette.ink)
                    Text(FixtureProjection.make(farm: farm, state: sampleState, selectedTaskKey: task.key).status.title)
                        .font(.system(size: 12))
                        .foregroundStyle(palette.ink.opacity(0.82))
                }
            }
        }
    }

    private var noticeContents: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(farm.notice.message)
                .font(.system(size: 13))
                .foregroundStyle(palette.ink)
                .fixedSize(horizontal: false, vertical: true)
            evidenceSection([
                FarmEvidence(id: farm.notice.taskID, title: "Task link", detail: "\(farm.notice.taskID) · synthetic task"),
                FarmEvidence(id: farm.notice.attemptID, title: "Attempt summary", detail: "\(farm.notice.attemptID) · local sample")
            ])
            activitySection([
                ("Review requested", "\(farm.notice.taskID) · synthetic note", true),
                ("Awaiting a decision", "No review decision is recorded here", false)
            ])
        }
    }

    private var storehouseContents: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("The project owner accepted revision \(farm.acceptance.revision) with digest sha256:\(farm.acceptance.digest). The review receipt is separate; neither state comes from a farm action.")
                .font(.system(size: 13))
                .foregroundStyle(palette.ink)
                .fixedSize(horizontal: false, vertical: true)
            evidenceSection([
                FarmEvidence(id: farm.acceptance.id, title: "Owner acceptance", detail: "\(farm.acceptance.revision) · sha256:\(farm.acceptance.digest)"),
                FarmEvidence(id: farm.acceptance.reviewReceipt, title: "Review receipt", detail: "\(farm.acceptance.reviewReceipt) · separate record")
            ])
            activitySection([
                ("Review decision recorded", "\(farm.acceptance.reviewReceipt) · separate record", true),
                ("Owner accepted \(farm.acceptance.revision)", "sha256:\(farm.acceptance.digest) · synthetic", true)
            ])
        }
    }

    private var authorityNote: some View {
        Label {
            Text("Farm actions only select records. dacli controls task changes and approvals.")
                .font(.system(size: 12))
                .fixedSize(horizontal: false, vertical: true)
        } icon: {
            Image(systemName: "lock.fill").font(.system(size: 11))
        }
        .foregroundStyle(palette.ink.opacity(0.78))
        .padding(.top, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func sectionHeading(_ text: String) -> some View {
        Text(text)
            .font(.system(size: 12, weight: .bold))
            .foregroundStyle(palette.creek)
    }

    private func evidenceSection(_ evidence: [FarmEvidence]) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            sectionHeading("Evidence")
            ForEach(evidence) { item in
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: "doc.text")
                        .font(.system(size: 13))
                        .foregroundStyle(palette.creek)
                        .frame(width: 24, height: 26)
                        .background(palette.sageLight.opacity(0.48), in: RoundedRectangle(cornerRadius: 5))
                    VStack(alignment: .leading, spacing: 2) {
                        Text(item.title).font(.system(size: 13, weight: .semibold)).foregroundStyle(palette.ink)
                        Text(item.detail)
                            .font(.system(size: 12))
                            .foregroundStyle(palette.ink.opacity(0.80))
                            .textSelection(.enabled)
                    }
                    Spacer(minLength: 0)
                }
                .padding(7)
                .background(.white.opacity(0.78), in: RoundedRectangle(cornerRadius: 7))
            }
            Text("Illustrative references; no live artifacts.")
                .font(.system(size: 12))
                .foregroundStyle(palette.ink.opacity(0.78))
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func activitySection(_ activity: [(title: String, detail: String, isComplete: Bool)]) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            sectionHeading("Activity")
            ForEach(Array(activity.enumerated()), id: \.offset) { entry in
                let item = entry.element
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: item.isComplete ? "checkmark.circle.fill" : "circle")
                        .font(.system(size: 12))
                        .foregroundStyle(item.isComplete ? palette.creek : palette.gold)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(item.title).font(.system(size: 12, weight: .semibold)).foregroundStyle(palette.ink)
                        Text(item.detail).font(.system(size: 12)).foregroundStyle(palette.ink.opacity(0.78))
                    }
                }
            }
        }
    }
}
