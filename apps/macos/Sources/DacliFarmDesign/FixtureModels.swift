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

enum AnimalSpecies: String, CaseIterable, Identifiable {
    var id: String { rawValue }
    case fox = "Fox"
    case rabbit = "Rabbit"
    case raccoon = "Raccoon"
    case owl = "Owl"
    case cat = "Cat"
    case bear = "Bear"
}

enum AgentRole: String, CaseIterable {
    case builder = "Builder", planner = "Planner", reviewer = "Reviewer"
    var defaultSpecies: AnimalSpecies {
        switch self { case .builder: .fox; case .planner: .rabbit; case .reviewer: .raccoon }
    }
    var symbol: String {
        switch self { case .builder: "hammer"; case .planner: "map"; case .reviewer: "doc.text.magnifyingglass" }
    }
}

enum GrowthStage: String {
    case sprout = "Sprout", growing = "Growing", seasoned = "Seasoned"
    static func from(outputTokens: Int?) -> GrowthStage? {
        guard let outputTokens, outputTokens >= 0 else { return nil }
        if outputTokens < 5_000 { return .sprout }
        return outputTokens < 25_000 ? .growing : .seasoned
    }
    var pixelSize: CGFloat { switch self { case .sprout: 2; case .growing: 3; case .seasoned: 4 } }
}

struct AgentUsageSample {
    let outputTokens: Int?
    let attemptID: String?
    var growth: GrowthStage? { GrowthStage.from(outputTokens: outputTokens) }
    var caption: String {
        guard let outputTokens else { return "Output usage unavailable" }
        return "\(outputTokens.formatted()) output tokens · sample"
    }
}

struct FarmAgent: Identifiable {
    let id: String
    let name: String
    let role: AgentRole
    let usage: AgentUsageSample
    var species: AnimalSpecies { role.defaultSpecies }
}

enum WorkKind: String { case task = "Task", issue = "Issue" }
enum HarvestPhase: String, CaseIterable, Identifiable {
    case harvested = "Harvested", fallow = "Fallow land", nextOpen = "Next open issue"
    var id: String { rawValue }
}

struct FarmTask: Identifiable {
    let key: String
    let id: String
    let title: String
    let plotTitle: String
    let summary: String
    let baseState: SampleState
    let agentID: String
    var kind: WorkKind = .task
}

enum CosmeticSkin: String, CaseIterable, Identifiable {
    case classic = "Work clothes", meadow = "Meadow scarf", midnight = "Starlight coat", rain = "Rainy day", owl = "Owl costume", cat = "Cat costume", bear = "Bear costume"
    var id: String { rawValue }
    var sampleOwned: Bool { self == .classic || self == .meadow }
    var speciesOverride: AnimalSpecies? {
        switch self { case .owl: .owl; case .cat: .cat; case .bear: .bear; default: nil }
    }
    var color: UInt32 {
        switch self { case .classic: 0x5A7E58; case .meadow: 0xB6724F; case .midnight: 0x716B9F; case .rain: 0xD3AB4C; case .owl: 0x716B9F; case .cat: 0xB6724F; case .bear: 0x5A7E58 }
    }
}

struct AgentAppearance: Equatable {
    var species: AnimalSpecies
    var skin: CosmeticSkin = .classic
    static func roleDefault(_ agent: FarmAgent) -> AgentAppearance { AgentAppearance(species: agent.species) }
    func isPreviewOnly(for agent: FarmAgent) -> Bool { species != agent.species || !skin.sampleOwned }
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
    let queuedTasks: [FarmTask]
    let agents: [FarmAgent]
    let notice: FarmNotice
    let acceptance: OwnerAcceptance
    let attentionLabel: String

    var attentionCount: Int { tasks.filter { $0.baseState == .reviewNeeded }.count }

    func task(for key: String) -> FarmTask {
        (tasks + queuedTasks).first(where: { $0.key == key }) ?? tasks[0]
    }

    func agent(for id: String) -> FarmAgent {
        agents.first(where: { $0.id == id }) ?? agents[0]
    }

    var nextOpenTask: FarmTask? {
        queuedTasks.first { item in
            item.baseState != .accepted && item.id.hasPrefix("\(code)-") && agents.contains { $0.id == item.agentID }
        }
    }

    func activeTasks(state: SampleState, harvest: HarvestPhase) -> [FarmTask] {
        guard state == .accepted else { return tasks }
        let remaining = tasks.filter { $0.key != "primary" }
        return harvest == .nextOpen ? nextOpenTask.map { [$0] + remaining } ?? remaining : remaining
    }

    func landTasks(state: SampleState, harvest: HarvestPhase) -> [FarmTask?] {
        guard state == .accepted else { return tasks.map(Optional.some) }
        let first: FarmTask? = harvest == .harvested ? task(for: "primary") : harvest == .nextOpen ? nextOpenTask : nil
        return [first, task(for: "waiting"), task(for: "review")] + tasks.filter { $0.key.hasPrefix("adoption-") }.map(Optional.some)
    }

    func currentTask(agentID: String, state: SampleState, harvest: HarvestPhase) -> FarmTask {
        if state == .accepted && harvest == .nextOpen && agentID == agents[0].id, let nextOpenTask { return nextOpenTask }
        return tasks.first(where: { $0.agentID == agentID }) ?? tasks[0]
    }

    func adding(_ adoption: AdoptionPreview?) -> ProjectFarm {
        guard let adoption else { return self }
        return ProjectFarm(id: id, title: title, projectName: projectName, code: code, tasks: tasks + [adoption.task], queuedTasks: queuedTasks, agents: agents + [adoption.agent], notice: notice, acceptance: acceptance, attentionLabel: attentionLabel)
    }

    func withQueuedWork(_ queue: [FarmTask]) -> ProjectFarm {
        ProjectFarm(id: id, title: title, projectName: projectName, code: code, tasks: tasks, queuedTasks: queue, agents: agents, notice: notice, acceptance: acceptance, attentionLabel: attentionLabel)
    }

    static let samples: [ProjectFarm] = [
        ProjectFarm(
            id: "wildflower",
            title: "Wildflower",
            projectName: "dacli core",
            code: "CORE",
            tasks: [
                FarmTask(key: "primary", id: "CORE-318", title: "Keep the garden path clear", plotTitle: "Garden path", summary: "Clear the path through the sample garden so each plot stays reachable.", baseState: .running, agentID: "core-mallow"),
                FarmTask(key: "waiting", id: "CORE-316", title: "Add a small lamp to the board", plotTitle: "Board lamp", summary: "Add a small lamp to the notice board scene after the board mount is ready.", baseState: .waiting, agentID: "core-pebble", kind: .issue),
                FarmTask(key: "review", id: "CORE-312", title: "Add evidence links", plotTitle: "Evidence links", summary: "Add evidence links to the delivery record. This sample has a review prerequisite.", baseState: .reviewNeeded, agentID: "core-fern")
            ],
            queuedTasks: [
                FarmTask(key: "next", id: "CORE-321", title: "Repair the little bridge", plotTitle: "Bridge repair", summary: "A queued open issue takes the freed plot. The harvested delivery stays in the Storehouse.", baseState: .waiting, agentID: "core-mallow", kind: .issue)
            ],
            agents: [
                FarmAgent(id: "core-mallow", name: "Mallow", role: .builder, usage: AgentUsageSample(outputTokens: 18_400, attemptID: "CORE-A208")),
                FarmAgent(id: "core-pebble", name: "Pebble", role: .planner, usage: AgentUsageSample(outputTokens: nil, attemptID: nil)),
                FarmAgent(id: "core-fern", name: "Fern", role: .reviewer, usage: AgentUsageSample(outputTokens: 31_200, attemptID: "CORE-A203"))
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
                FarmTask(key: "waiting", id: "UI-082", title: "Label quiet states", plotTitle: "Quiet states", summary: "Give waiting states clear labels after the shared legend is ready.", baseState: .waiting, agentID: "ui-pebble", kind: .issue),
                FarmTask(key: "review", id: "UI-079", title: "Check evidence paths", plotTitle: "Evidence paths", summary: "Check the links between an attempt, its evidence, and review.", baseState: .reviewNeeded, agentID: "ui-fern")
            ],
            queuedTasks: [
                FarmTask(key: "next", id: "UI-087", title: "Label the harvest record", plotTitle: "Harvest labels", summary: "A queued open issue takes the freed plot. The harvested delivery stays in the Storehouse.", baseState: .waiting, agentID: "ui-mallow", kind: .issue)
            ],
            agents: [
                FarmAgent(id: "ui-mallow", name: "Mallow", role: .builder, usage: AgentUsageSample(outputTokens: 18_400, attemptID: "UI-A208")),
                FarmAgent(id: "ui-pebble", name: "Pebble", role: .planner, usage: AgentUsageSample(outputTokens: nil, attemptID: nil)),
                FarmAgent(id: "ui-fern", name: "Fern", role: .reviewer, usage: AgentUsageSample(outputTokens: 31_200, attemptID: "UI-A203"))
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

enum UsagePreview: String, CaseIterable, Identifiable {
    case recorded = "Recorded sample", unavailable = "Usage unavailable", sprout = "3,000 sample output", growing = "18,400 sample output", seasoned = "42,000 sample output"
    var id: String { rawValue }
    func usage(for agent: FarmAgent, code: String) -> AgentUsageSample {
        switch self {
        case .recorded: return agent.usage
        case .unavailable: return AgentUsageSample(outputTokens: nil, attemptID: nil)
        case .sprout: return AgentUsageSample(outputTokens: 3_000, attemptID: "\(code)-USAGE-PREVIEW")
        case .growing: return AgentUsageSample(outputTokens: 18_400, attemptID: "\(code)-USAGE-PREVIEW")
        case .seasoned: return AgentUsageSample(outputTokens: 42_000, attemptID: "\(code)-USAGE-PREVIEW")
        }
    }
}

struct AdoptionPreview {
    let agent: FarmAgent
    let task: FarmTask
}
