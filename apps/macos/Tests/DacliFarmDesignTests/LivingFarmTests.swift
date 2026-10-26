import XCTest
@testable import DacliFarmDesign

final class LivingFarmTests: XCTestCase {
    func testRoleDefaultsAreDistinctAndUsageUnknownHasNoGrowth() {
        XCTAssertEqual(Set(AgentRole.allCases.map(\.defaultSpecies)).count, AgentRole.allCases.count)
        XCTAssertEqual(AgentRole.builder.defaultSpecies, .fox)
        XCTAssertEqual(AgentRole.planner.defaultSpecies, .rabbit)
        XCTAssertEqual(AgentRole.reviewer.defaultSpecies, .raccoon)
        XCTAssertNil(GrowthStage.from(outputTokens: nil))
        XCTAssertNil(GrowthStage.from(outputTokens: -1))
        XCTAssertEqual(GrowthStage.from(outputTokens: 0), .sprout)
        XCTAssertEqual(GrowthStage.from(outputTokens: 4_999), .sprout)
        XCTAssertEqual(GrowthStage.from(outputTokens: 5_000), .growing)
        XCTAssertEqual(GrowthStage.from(outputTokens: 24_999), .growing)
        XCTAssertEqual(GrowthStage.from(outputTokens: 25_000), .seasoned)
        for farm in ProjectFarm.samples {
            XCTAssertNil(farm.agents[1].usage.growth)
            XCTAssertNil(farm.agents[1].usage.attemptID)
            XCTAssertEqual(farm.agents[0].usage.attemptID, "\(farm.code)-A208")
        }
    }

    func testOnlyAcceptedWorkFreesLandAndReplacementHasNewIdentity() {
        for farm in ProjectFarm.samples {
            for state in SampleState.allCases {
                let active = farm.activeTasks(state: state, harvest: .fallow)
                XCTAssertEqual(active.count, state == .accepted ? 2 : 3, "\(state)")
                XCTAssertEqual(farm.landTasks(state: state, harvest: .fallow)[0] == nil, state == .accepted)
            }
            let history = farm.task(for: "primary")
            XCTAssertEqual(farm.landTasks(state: .accepted, harvest: .harvested)[0]?.id, history.id)
            let replacement = farm.activeTasks(state: .accepted, harvest: .nextOpen)[0]
            XCTAssertNotEqual(replacement.id, history.id)
            XCTAssertEqual(replacement.key, "next")
            XCTAssertEqual(replacement.kind, .issue)
            XCTAssertEqual(farm.task(for: "primary").id, history.id)
            XCTAssertFalse(farm.activeTasks(state: .accepted, harvest: .nextOpen).contains { $0.id == history.id })
            let projected = FixtureProjection.make(farm: farm, state: .accepted, selectedTaskKey: "next")
            XCTAssertEqual(projected.task.id, replacement.id)
            XCTAssertEqual(projected.status, .waiting)
            XCTAssertNil(projected.ownerAcceptance)
        }
    }

    @MainActor
    func testSkinAndGrowthPreviewCannotChangeRoleWorkOrAcceptance() async {
        let session = FarmSession()
        session.selectState(.accepted)
        let original = session.farm.agents[0]
        let originalAcceptance = session.projection.ownerAcceptance?.id
        let task = session.projection.task.id
        session.preview(AgentAppearance(species: .owl, skin: .owl), for: original)
        session.selectedUsagePreview = .seasoned
        XCTAssertEqual(session.appearance(for: original).species, .owl)
        XCTAssertEqual(session.farm.agents[0].role, .builder)
        XCTAssertEqual(session.farm.agents[0].id, original.id)
        XCTAssertEqual(session.projection.task.id, task)
        XCTAssertEqual(session.projection.ownerAcceptance?.id, originalAcceptance)
        XCTAssertEqual(session.usage(for: original).growth, .seasoned)
        XCTAssertEqual(session.farm.agents[0].usage.outputTokens, 18_400)
        session.selectedUsagePreview = .unavailable
        XCTAssertNil(session.usage(for: original).growth)
        session.restoreAppearance(for: original)
        XCTAssertEqual(session.appearance(for: original), .roleDefault(original))
    }

    @MainActor
    func testAdoptionAddsIndependentAgentAndWorkWithoutReusingAnIdentity() async {
        let session = FarmSession()
        let originalAgentIDs = Set(session.farm.agents.map(\.id))
        let originalTaskIDs = Set(session.farm.tasks.map(\.id))
        session.previewAdoption(role: .planner, state: .running)
        let adopted = session.farm.agent(for: session.selectedAgentID)
        let adoptedTask = session.projection.task
        XCTAssertEqual(session.farm.agents.count, 4)
        XCTAssertEqual(session.activeTasks.count, 4)
        XCTAssertEqual(adopted.role, .planner)
        XCTAssertEqual(adopted.species, .rabbit)
        XCTAssertFalse(originalAgentIDs.contains(adopted.id))
        XCTAssertFalse(originalTaskIDs.contains(adoptedTask.id))
        XCTAssertEqual(adoptedTask.agentID, adopted.id)
        XCTAssertEqual(session.projection.status, .running)
        XCTAssertNil(session.projection.ownerAcceptance)
        XCTAssertNil(adopted.usage.outputTokens)
        session.selectState(.accepted)
        XCTAssertEqual(session.activeTasks.count, 3)
        XCTAssertTrue(session.farm.landTasks(state: .accepted, harvest: .harvested).contains { $0?.id == adoptedTask.id })
        XCTAssertTrue(session.activeTasks.contains { $0.id == adoptedTask.id })
        session.selectState(.running)
        session.selectFarm(ProjectFarm.samples[1])
        XCTAssertEqual(session.farm.agents.count, 3)
        session.selectFarm(ProjectFarm.samples[0])
        XCTAssertEqual(session.farm.agents.count, 4)
        session.removeAdoptionPreview()
        XCTAssertEqual(session.farm.agents.count, 3)
        session.previewAdoption(role: .reviewer, state: .accepted)
        XCTAssertNotEqual(session.selectedAgentID, adopted.id)
        XCTAssertNotEqual(session.projection.task.id, adoptedTask.id)
        // A shop/adoption preview cannot mint accepted authority.
        XCTAssertEqual(session.projection.status, .waiting)
        XCTAssertNil(session.projection.ownerAcceptance)
    }

    @MainActor
    func testHarvestPreviewDoesNotChangeAReviewIntoAcceptance() async {
        let session = FarmSession()
        session.selectState(.reviewNeeded)
        session.advanceHarvestPreview()
        XCTAssertEqual(session.harvestPhase, .harvested)
        XCTAssertEqual(session.activeTasks.count, 3)
        XCTAssertNil(session.projection.ownerAcceptance)
        session.selectState(.accepted)
        XCTAssertEqual(session.activeTasks.count, 2)
        session.advanceHarvestPreview()
        XCTAssertEqual(session.harvestPhase, .fallow)
        session.advanceHarvestPreview()
        XCTAssertEqual(session.harvestPhase, .nextOpen)
        XCTAssertEqual(session.projection.task.id, "CORE-321")
        XCTAssertEqual(session.activeTasks.count, 3)
        XCTAssertNil(session.projection.ownerAcceptance)
        XCTAssertEqual(session.farm.acceptance.revision, "CORE-r92")
    }

    @MainActor
    func testEmptyQueueLeavesFallowLandWithoutInventingWork() async {
        let session = FarmSession()
        session.selectState(.accepted)
        session.setQueuePreviewEmpty(true)
        session.advanceHarvestPreview()
        session.setHarvestPhase(.nextOpen)
        XCTAssertEqual(session.harvestPhase, .fallow)
        XCTAssertNil(session.farm.nextOpenTask)
        XCTAssertEqual(session.activeTasks.count, 2)
        XCTAssertNil(session.farm.landTasks(state: .accepted, harvest: .fallow)[0])
        XCTAssertFalse(session.activeTasks.contains { $0.key == "next" })
        XCTAssertEqual(session.farm.acceptance.revision, "CORE-r92")
        let core = ProjectFarm.samples[0]
        let foreign = ProjectFarm.samples[1].queuedTasks
        XCTAssertNil(core.withQueuedWork(foreign).nextOpenTask)
        let done = FarmTask(key: "queued-done", id: "CORE-CLOSED", title: "Closed", plotTitle: "Closed", summary: "Closed fixture", baseState: .accepted, agentID: core.agents[0].id)
        XCTAssertNil(core.withQueuedWork([done]).nextOpenTask)
    }

    func testWalkingPathsAdvanceStayBoundedAndStopForReducedMotion() {
        for index in 0..<4 {
            var positions = Set<String>()
            for time in stride(from: 0.0, to: 48.0, by: 0.5) {
                let pose = ResidentMotion.sample(index: index, state: .running, time: time, moving: true)
                XCTAssertTrue((0.05...0.95).contains(pose.x))
                XCTAssertTrue((0.1...0.9).contains(pose.y))
                positions.insert("\(pose.x),\(pose.y)")
                for state in SampleState.allCases {
                    let still = ResidentMotion.sample(index: index, state: state, time: time, moving: false)
                    let origin = ResidentMotion.sample(index: index, state: state, time: 0, moving: false)
                    XCTAssertEqual(still.x, origin.x)
                    XCTAssertEqual(still.y, origin.y)
                    XCTAssertEqual(still.bob, 0)
                    XCTAssertFalse(still.walking)
                }
            }
            XCTAssertGreaterThan(positions.count, 10)
        }
        let blockedA = ResidentMotion.sample(index: 0, state: .blocked, time: 1, moving: true)
        let blockedB = ResidentMotion.sample(index: 0, state: .blocked, time: 8, moving: true)
        XCTAssertEqual(blockedA.x, blockedB.x)
        XCTAssertEqual(blockedA.y, blockedB.y)
    }
}
