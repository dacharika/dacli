import XCTest
@testable import DacliFarmDesign

final class FarmSessionTests: XCTestCase {
    @MainActor
    func testSwitchingFarmReplacesAgentAndAcceptanceIdentity() async {
        let session = FarmSession()
        session.selectState(.accepted)
        let coreAgent = session.selectedAgentID
        let coreAcceptance = session.projection.ownerAcceptance?.id
        session.selectFarm(ProjectFarm.samples[1])

        XCTAssertEqual(session.farm.id, "moonrise")
        XCTAssertNotEqual(session.selectedAgentID, coreAgent)
        XCTAssertEqual(session.selectedAgentID, "ui-mallow")
        XCTAssertEqual(session.projection.task.id, "UI-084")
        XCTAssertNotEqual(session.projection.ownerAcceptance?.id, coreAcceptance)
        XCTAssertEqual(session.projection.ownerAcceptance?.revision, "UI-r24")
        XCTAssertEqual(session.projection.ownerAcceptance?.digest, "86bf0c17")
        XCTAssertTrue(session.projection.evidence.allSatisfy { $0.id.contains("UI") })
    }

    @MainActor
    func testReviewSnapshotDoesNotCarryAcceptedAuthorityForward() async {
        let session = FarmSession()
        session.selectState(.accepted)
        XCTAssertNotNil(session.projection.ownerAcceptance)
        session.selectState(.reviewNeeded)

        XCTAssertEqual(session.projection.task.id, "CORE-312")
        XCTAssertEqual(session.selectedAgentID, "core-fern")
        XCTAssertEqual(session.projection.status, .reviewNeeded)
        XCTAssertNil(session.projection.ownerAcceptance)
        XCTAssertFalse(session.projection.evidence.contains { $0.id == session.farm.acceptance.id })
        XCTAssertEqual(session.inspectorSelection, .task)
    }
}
