import Foundation
import SwiftUI

enum SampleState: String, CaseIterable, Identifiable {
    case running
    case waiting
    case blocked
    case disconnected
    case reviewNeeded
    case accepted

    var id: String { rawValue }

    var title: String {
        switch self {
        case .running: "Running"
        case .waiting: "Waiting"
        case .blocked: "Blocked"
        case .disconnected: "Disconnected"
        case .reviewNeeded: "Review needed"
        case .accepted: "Accepted"
        }
    }

    var symbol: String {
        switch self {
        case .running: "play.fill"
        case .waiting: "pause.fill"
        case .blocked: "exclamationmark"
        case .disconnected: "wifi.slash"
        case .reviewNeeded: "eye"
        case .accepted: "checkmark"
        }
    }

    var fixtureTaskKey: String {
        switch self {
        case .running, .disconnected, .accepted: "primary"
        case .waiting: "waiting"
        case .blocked, .reviewNeeded: "review"
        }
    }
}

enum AnimalSpecies: String {
    case fox = "Fox"
    case rabbit = "Rabbit"
    case raccoon = "Raccoon"
}

struct FarmAgent: Identifiable {
    let id: String
    let name: String
    let species: AnimalSpecies
}

struct FarmTask: Identifiable {
    let key: String
    let id: String
    let title: String
    let plotTitle: String
    let summary: String
    let baseState: SampleState
    let agentID: String
}

struct FarmEvidence: Identifiable {
    let id: String
    let title: String
    let detail: String
}

struct FarmNotice {
    let id: String
    let taskID: String
    let title: String
    let message: String
    let attemptID: String
}

struct OwnerAcceptance {
    let id: String
    let revision: String
    let digest: String
    let reviewReceipt: String
}

struct ProjectFarm: Identifiable {
    let id: String
    let title: String
    let projectName: String
    let code: String
    let tasks: [FarmTask]
    let agents: [FarmAgent]
    let notice: FarmNotice
    let acceptance: OwnerAcceptance
    let attentionLabel: String

    var attentionCount: Int { tasks.filter { $0.baseState == .reviewNeeded }.count }

    func task(for key: String) -> FarmTask {
        tasks.first(where: { $0.key == key }) ?? tasks[0]
    }

    func agent(for id: String) -> FarmAgent {
        agents.first(where: { $0.id == id }) ?? agents[0]
    }

    static let samples: [ProjectFarm] = [
        ProjectFarm(
            id: "wildflower",
            title: "Wildflower",
            projectName: "dacli core",
            code: "CORE",
            tasks: [
                FarmTask(key: "primary", id: "CORE-318", title: "Keep the garden path clear", plotTitle: "Garden path", summary: "Clear the path through the sample garden so each plot stays reachable.", baseState: .running, agentID: "core-mallow"),
                FarmTask(key: "waiting", id: "CORE-316", title: "Add a small lamp to the board", plotTitle: "Board lamp", summary: "Add a small lamp to the notice board scene after the board mount is ready.", baseState: .waiting, agentID: "core-pebble"),
                FarmTask(key: "review", id: "CORE-312", title: "Add evidence links", plotTitle: "Evidence links", summary: "Add evidence links to the delivery record. This sample has a review prerequisite.", baseState: .reviewNeeded, agentID: "core-fern")
            ],
            agents: [
                FarmAgent(id: "core-mallow", name: "Mallow", species: .fox),
                FarmAgent(id: "core-pebble", name: "Pebble", species: .rabbit),
                FarmAgent(id: "core-fern", name: "Fern", species: .raccoon)
            ],
            notice: FarmNotice(id: "NOTE-CORE-14", taskID: "CORE-312", title: "Review requested", message: "CORE-312 has sample evidence ready for a human look.", attemptID: "CORE-A203"),
            acceptance: OwnerAcceptance(id: "ACCEPT-CORE-08", revision: "CORE-r92", digest: "73a24f0b", reviewReceipt: "CORE-R078"),
            attentionLabel: "1 task needs review"
        ),
        ProjectFarm(
            id: "moonrise",
            title: "Moonrise",
            projectName: "operator UI",
            code: "UI",
            tasks: [
                FarmTask(key: "primary", id: "UI-084", title: "Tune the task inspector", plotTitle: "Inspector spacing", summary: "Tune spacing in the synthetic task inspector.", baseState: .running, agentID: "ui-mallow"),
                FarmTask(key: "waiting", id: "UI-082", title: "Label quiet states", plotTitle: "Quiet states", summary: "Give waiting states clear labels after the shared legend is ready.", baseState: .waiting, agentID: "ui-pebble"),
                FarmTask(key: "review", id: "UI-079", title: "Check evidence paths", plotTitle: "Evidence paths", summary: "Check the links between an attempt, its evidence, and review.", baseState: .reviewNeeded, agentID: "ui-fern")
            ],
            agents: [
                FarmAgent(id: "ui-mallow", name: "Mallow", species: .fox),
                FarmAgent(id: "ui-pebble", name: "Pebble", species: .rabbit),
                FarmAgent(id: "ui-fern", name: "Fern", species: .raccoon)
            ],
            notice: FarmNotice(id: "NOTE-UI-05", taskID: "UI-079", title: "Evidence needs a look", message: "UI-079 has sample evidence ready for a human look.", attemptID: "UI-A055"),
            acceptance: OwnerAcceptance(id: "ACCEPT-UI-02", revision: "UI-r24", digest: "86bf0c17", reviewReceipt: "UI-R019"),
            attentionLabel: "1 task needs review"
        )
    ]
}

struct FixtureProjection {
    let task: FarmTask
    let agent: FarmAgent
    let status: SampleState
    let summary: String
    let freshness: String
    let description: String
    let evidence: [FarmEvidence]
    let activity: [(title: String, detail: String, isComplete: Bool)]
    let ownerAcceptance: OwnerAcceptance?

    static func make(farm: ProjectFarm, state: SampleState, selectedTaskKey: String) -> FixtureProjection {
        let taskKey = state.fixtureTaskKey
        let task = farm.task(for: selectedTaskKey)
        let focusedTask = selectedTaskKey == taskKey
        let focusedStatus = taskStatus(farm: farm, state: state, taskKey: task.key)
        let agent = farm.agent(for: task.agentID)
        let acceptance = farm.acceptance

        let evidence: [FarmEvidence]
        let activity: [(title: String, detail: String, isComplete: Bool)]
        if focusedTask {
            switch state {
            case .running:
                evidence = [
                    FarmEvidence(id: "\(farm.code)-A208", title: "Run summary", detail: "Attempt \(farm.code)-A208 · local sample"),
                    FarmEvidence(id: "FILES-\(farm.code)-2", title: "Changed files", detail: "2 files · synthetic")
                ]
                activity = [("Attempt started", "10:38 · sample time", true), ("Working on task", "Synthetic snapshot", false)]
            case .waiting:
                evidence = [
                    FarmEvidence(id: "\(farm.code)-DEP", title: "Dependency note", detail: "Waiting on \(farm.task(for: "primary").id) · synthetic"),
                    FarmEvidence(id: "\(farm.code)-WAIT", title: "Attempt receipt", detail: "No run yet · sample")
                ]
                activity = [("Task opened", "10:12 · sample time", true), ("Waiting for a dependency", "Dependency in this fixture", false)]
            case .blocked:
                evidence = [
                    FarmEvidence(id: "\(farm.code)-BLOCK", title: "Block note", detail: "Missing input · synthetic"),
                    FarmEvidence(id: "\(farm.code)-A203", title: "Previous attempt", detail: "Attempt \(farm.code)-A203 · local sample")
                ]
                activity = [("Attempt paused", "10:21 · sample time", true), ("Needs an input", "No action taken by the fixture", false)]
            case .disconnected:
                evidence = [
                    FarmEvidence(id: "\(farm.code)-A208", title: "Last run summary", detail: "Attempt \(farm.code)-A208 · local sample"),
                    FarmEvidence(id: "\(farm.code)-CONNECTION", title: "Connection note", detail: "Unavailable · synthetic")
                ]
                activity = [("Last sample seen", "10:38 · sample time", true), ("Provider disconnected", "Freshness is not live", false)]
            case .reviewNeeded:
                evidence = [
                    FarmEvidence(id: "\(farm.code)-A203", title: "Run summary", detail: "Attempt \(farm.code)-A203 · local sample"),
                    FarmEvidence(id: "FILES-\(farm.code)-4", title: "Changed files", detail: "4 files · synthetic")
                ]
                activity = [("Attempt finished", "10:33 · sample time", true), ("Review requested", "No review decision in this fixture", false)]
            case .accepted:
                evidence = [
                    FarmEvidence(id: acceptance.id, title: "Owner acceptance", detail: "\(acceptance.revision) · sha256:\(acceptance.digest)"),
                    FarmEvidence(id: acceptance.reviewReceipt, title: "Review receipt", detail: "\(acceptance.reviewReceipt) · separate record")
                ]
                activity = [
                    ("Review decision recorded", "\(acceptance.reviewReceipt) · separate record", true),
                    ("Owner accepted \(acceptance.revision)", "sha256:\(acceptance.digest) · synthetic", true)
                ]
            }
        } else {
            evidence = [
                FarmEvidence(id: task.id, title: "Task record", detail: "\(task.id) · synthetic"),
                FarmEvidence(id: "\(farm.code)-LINKS", title: "Evidence links", detail: "Illustrative · no live artifact")
            ]
            activity = [("Task recorded", "Synthetic sample", true), ("No live activity", "Fixture only", false)]
        }

        let description: String
        if focusedTask && state == .accepted {
            description = "This sample shows owner acceptance for \(acceptance.revision) with digest sha256:\(acceptance.digest). Its review receipt is a separate record."
        } else if focusedTask && state == .disconnected {
            description = "The last sample is still visible. A disconnected provider does not imply the task stopped."
        } else if focusedTask && state == .blocked {
            description = "This task is blocked on a missing synthetic input. The fixture does not take action or change authority."
        } else if focusedTask && state == .reviewNeeded {
            description = "The sample attempt has evidence attached and needs a reviewer decision. This screen does not record that decision."
        } else {
            description = task.summary
        }

        let summary: String
        if focusedTask {
            switch state {
            case .running: summary = "\(agent.name) is tending a task"
            case .waiting: summary = "\(agent.name) is waiting for a dependency"
            case .blocked: summary = "\(agent.name) is blocked on a missing input"
            case .disconnected: summary = "\(agent.name)'s provider connection is unavailable"
            case .reviewNeeded: summary = "\(agent.name)'s delivery is waiting for review"
            case .accepted: summary = "The project owner accepted a delivery revision"
            }
        } else {
            summary = "\(farm.title) fixture · \(task.id) · \(focusedStatus.title.lowercased())"
        }
        let freshness = state == .disconnected ? "Last synthetic sample · 10:38" : "Synthetic snapshot · 10:42 sample time"
        return FixtureProjection(task: task, agent: agent, status: focusedStatus, summary: summary, freshness: freshness, description: description, evidence: evidence, activity: activity, ownerAcceptance: focusedTask && state == .accepted ? acceptance : nil)
    }

    static func taskStatus(farm: ProjectFarm, state: SampleState, taskKey: String) -> SampleState {
        guard state.fixtureTaskKey == taskKey else {
            return farm.task(for: taskKey).baseState
        }
        return state
    }
}
