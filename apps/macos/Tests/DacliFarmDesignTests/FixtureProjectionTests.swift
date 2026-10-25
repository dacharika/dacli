import XCTest
@testable import DacliFarmDesign

final class FixtureProjectionTests: XCTestCase {
    func testEachSampleStateSelectsItsMatchingTaskAndAgent() {
        for farm in ProjectFarm.samples {
            for state in SampleState.allCases {
                let taskKey = state.fixtureTaskKey
                let expectedTaskKey: String
                switch state {
                case .running, .disconnected, .accepted: expectedTaskKey = "primary"
                case .waiting: expectedTaskKey = "waiting"
                case .blocked, .reviewNeeded: expectedTaskKey = "review"
                }
                XCTAssertEqual(taskKey, expectedTaskKey)

                let projection = FixtureProjection.make(
                    farm: farm,
                    state: state,
                    selectedTaskKey: taskKey
                )

                XCTAssertEqual(projection.task.key, taskKey, "\(farm.code) \(state.title)")
                XCTAssertEqual(projection.status, state, "\(farm.code) \(state.title)")
                XCTAssertEqual(projection.agent.id, projection.task.agentID)
                XCTAssertEqual(projection.ownerAcceptance != nil, state == .accepted)
                XCTAssertFalse(projection.summary.contains("("), "\(farm.code) \(state.title)")

                for plot in farm.tasks {
                    let expectedStatus = plot.key == expectedTaskKey ? state : plot.baseState
                    XCTAssertEqual(
                        FixtureProjection.taskStatus(farm: farm, state: state, taskKey: plot.key),
                        expectedStatus,
                        "\(farm.code) \(state.title) \(plot.id)"
                    )
                }
            }
        }
    }

    func testFarmRecordsAndEvidenceStayProjectDistinct() {
        let core = ProjectFarm.samples[0]
        let ui = ProjectFarm.samples[1]

        func stableIDs(_ farm: ProjectFarm) -> Set<String> {
            Set(
                farm.tasks.map(\.id)
                    + farm.agents.map(\.id)
                    + [
                        farm.notice.id,
                        farm.notice.taskID,
                        farm.notice.attemptID,
                        farm.acceptance.id,
                        farm.acceptance.revision,
                        farm.acceptance.digest,
                        farm.acceptance.reviewReceipt
                    ]
            )
        }

        XCTAssertTrue(stableIDs(core).isDisjoint(with: stableIDs(ui)))

        for state in SampleState.allCases {
            let coreProjection = FixtureProjection.make(
                farm: core,
                state: state,
                selectedTaskKey: state.fixtureTaskKey
            )
            let uiProjection = FixtureProjection.make(
                farm: ui,
                state: state,
                selectedTaskKey: state.fixtureTaskKey
            )

            XCTAssertTrue(coreProjection.evidence.allSatisfy { $0.id.contains(core.code) })
            XCTAssertTrue(uiProjection.evidence.allSatisfy { $0.id.contains(ui.code) })
            XCTAssertTrue(
                Set(coreProjection.evidence.map(\.id))
                    .isDisjoint(with: Set(uiProjection.evidence.map(\.id)))
            )
        }
    }

    func testOwnerAcceptanceIsSeparateFromReviewReceipt() {
        for farm in ProjectFarm.samples {
            let projection = FixtureProjection.make(
                farm: farm,
                state: .accepted,
                selectedTaskKey: SampleState.accepted.fixtureTaskKey
            )

            XCTAssertEqual(projection.ownerAcceptance?.revision, farm.acceptance.revision)
            XCTAssertNotEqual(farm.acceptance.id, farm.acceptance.reviewReceipt)
            XCTAssertTrue(projection.description.contains(farm.acceptance.revision))
            XCTAssertTrue(projection.description.contains(farm.acceptance.digest))
            XCTAssertFalse(projection.description.contains("("))
            XCTAssertEqual(projection.evidence.map(\.id), [
                farm.acceptance.id,
                farm.acceptance.reviewReceipt
            ])
        }
    }
}
