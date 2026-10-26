import SwiftUI

// Shell availability is independent of a task's work state. These are local
// fixtures, not connection observations or authority decisions (#1152).
enum ShellState: String, CaseIterable, Identifiable {
    case ready, loading, empty, offline, stale, denied, unsupported
    var id: String { rawValue }
    var title: String {
        switch self {
        case .ready: "Ready"
        case .loading: "Loading"
        case .empty: "Empty workspace"
        case .offline: "Offline"
        case .stale: "Stale snapshot"
        case .denied: "Access denied"
        case .unsupported: "Unsupported protocol"
        }
    }
    var symbol: String {
        switch self {
        case .ready: "checkmark.circle"
        case .loading: "hourglass"
        case .empty: "tray"
        case .offline: "wifi.slash"
        case .stale: "clock.badge.exclamationmark"
        case .denied: "lock"
        case .unsupported: "exclamationmark.triangle"
        }
    }
    var message: String {
        switch self {
        case .ready: "Synthetic workspace · local fixtures only."
        case .loading: "Loading a new synthetic snapshot. Previous task details are hidden."
        case .empty: "This synthetic workspace has no tasks, attempts or evidence."
        case .offline: "Connection unavailable in this fixture. Retained content is a last synthetic snapshot; current work is unknown."
        case .stale: "Retained synthetic snapshot is stale. Current work and evidence freshness are unknown."
        case .denied: "Synthetic denied response. Task, attempt and evidence content has been cleared."
        case .unsupported: "Synthetic unsupported_version response. No compatible workspace content is available."
        }
    }
    // The existing operator/v1 golden error cases define these names. Unknown
    // codes are not silently treated as a successful or compatible snapshot.
    static func fixtureError(code: String) -> ShellState? {
        switch code {
        case "denied": .denied
        case "unsupported_version": .unsupported
        default: nil
        }
    }
    var retainsSnapshot: Bool { self == .offline || self == .stale }
}

struct ShellSelection {
    let farmID: String
    let taskID: String
    let taskKey: String
    let agentID: String
    let inspector: InspectorSelection
    let inspectorPresented: Bool

    func isValid(in farm: ProjectFarm) -> Bool {
        farm.id == farmID && (farm.tasks + farm.queuedTasks).contains {
            $0.id == taskID && $0.key == taskKey && ($0.agentID == agentID || inspector == .wardrobe)
        } && farm.agents.contains { $0.id == agentID }
    }
}

struct ShellAvailabilityView: View {
    let state: ShellState
    let hasSnapshot: Bool
    let restore: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Label(state.title, systemImage: state.symbol)
                .font(FarmStyle.title(26))
                .accessibilityAddTraits(.isHeader)
            Text(state.message).font(.system(size: 14)).fixedSize(horizontal: false, vertical: true)
            if state.retainsSnapshot && !hasSnapshot {
                Text("No snapshot is retained for this project. Task details remain hidden.")
                    .font(.system(size: 13, weight: .semibold))
            }
            Text("Synthetic preview · no live connection or authority")
                .font(.system(size: 12)).foregroundStyle(FarmStyle.muted)
            Button("Load synthetic workspace", action: restore)
                .buttonStyle(.borderedProminent)
                .help("Loads local sample records only; does not connect or sign in.")
        }
        .padding(32)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
        .background { ParchmentSurface() }
        .foregroundStyle(FarmStyle.ink)
        .accessibilityElement(children: .contain)
    }
}
