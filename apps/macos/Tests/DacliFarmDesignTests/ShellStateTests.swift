import Foundation
import XCTest
@testable import DacliFarmDesign

final class ShellStateTests: XCTestCase {
    @MainActor
    func testLoadingHidesRecordsThenRestoresExactValidSelection() async {
        let session = FarmSession()
        session.select(session.farm.task(for: "review"))
        session.open(.notice)
        session.selectShellState(.loading)
        XCTAssertNil(session.visibleProjection)
        XCTAssertTrue(session.activeTasks.isEmpty)
        XCTAssertEqual(session.selectedTaskKey, "")
        XCTAssertEqual(session.selectedAgentID, "")
        XCTAssertFalse(session.inspectorPresented)
        session.open(.task)
        XCTAssertFalse(session.inspectorPresented)
        session.selectShellState(.ready)
        XCTAssertEqual(session.visibleProjection?.task.id, "CORE-312")
        XCTAssertEqual(session.selectedAgentID, "core-fern")
        XCTAssertEqual(session.inspectorSelection, .notice)
    }

    @MainActor
    func testOfflineAndStaleRetainOnlyAnExistingSameProjectSnapshot() async {
        let session = FarmSession()
        session.select(session.farm.task(for: "review"))
        for state in [ShellState.offline, .stale] {
            session.selectShellState(state)
            XCTAssertEqual(session.visibleProjection?.task.id, "CORE-312")
            XCTAssertTrue(session.shellState.message.contains("unknown"))
        }
        session.selectFarm(ProjectFarm.samples[1])
        XCTAssertNil(session.visibleProjection)
        XCTAssertTrue(session.activeTasks.isEmpty)
        XCTAssertEqual(session.selectedTaskKey, "")
        XCTAssertEqual(session.selectedAgentID, "")
        session.selectShellState(.ready)
        XCTAssertEqual(session.visibleProjection?.task.id, "UI-084")
        XCTAssertEqual(session.selectedAgentID, "ui-mallow")
        session.selectShellState(.loading)
        session.selectShellState(.offline)
        XCTAssertNil(session.visibleProjection, "A new load is not a retained snapshot")
    }

    @MainActor
    func testEmptyDeniedAndUnsupportedDiscardDetailsAndOldSelection() async {
        for state in [ShellState.empty, .denied, .unsupported] {
            let session = FarmSession()
            session.select(session.farm.task(for: "review"))
            session.selectShellState(state)
            XCTAssertNil(session.visibleProjection, state.rawValue)
            XCTAssertTrue(session.activeTasks.isEmpty, state.rawValue)
            XCTAssertEqual(session.selectedTaskKey, "", state.rawValue)
            XCTAssertEqual(session.selectedAgentID, "", state.rawValue)
            XCTAssertFalse(session.inspectorPresented, state.rawValue)
            session.selectShellState(.stale)
            XCTAssertNil(session.visibleProjection, "Denied/empty/unsupported cannot become cached content")
            session.selectShellState(.ready)
            XCTAssertEqual(session.visibleProjection?.task.id, "CORE-318", "Old review selection must be discarded")
        }
    }

    @MainActor
    func testProjectChangeDuringLoadCannotRestoreForeignTaskOrEvidence() async {
        let session = FarmSession()
        session.select(session.farm.task(for: "review"))
        session.selectShellState(.loading)
        session.selectFarm(ProjectFarm.samples[1])
        XCTAssertNil(session.visibleProjection)
        session.selectShellState(.ready)
        XCTAssertEqual(session.visibleProjection?.task.id, "UI-084")
        XCTAssertEqual(session.selectedAgentID, "ui-mallow")
        XCTAssertTrue(session.visibleProjection?.evidence.allSatisfy { $0.id.contains("UI") } ?? false)
    }

    @MainActor
    func testRemovedAdoptionIsNotRestoredAndInvalidIDsAreRejected() async {
        let session = FarmSession()
        session.previewAdoption(role: .planner, state: .waiting)
        let adoptedID = session.projection.task.id
        session.selectShellState(.loading)
        session.removeAdoptionPreview()
        session.selectShellState(.ready)
        XCTAssertNotEqual(session.visibleProjection?.task.id, adoptedID)
        XCTAssertEqual(session.visibleProjection?.task.id, "CORE-318")
        XCTAssertEqual(session.selectedTaskKey, "primary")
        XCTAssertEqual(session.selectedAgentID, "core-mallow")
        let farm = session.farm
        for saved in [
            ShellSelection(farmID: "moonrise", taskID: "CORE-318", taskKey: "primary", agentID: "core-mallow", inspector: .task, inspectorPresented: true),
            ShellSelection(farmID: farm.id, taskID: "removed", taskKey: "primary", agentID: "core-mallow", inspector: .task, inspectorPresented: true),
            ShellSelection(farmID: farm.id, taskID: "CORE-318", taskKey: "review", agentID: "core-mallow", inspector: .task, inspectorPresented: true),
            ShellSelection(farmID: farm.id, taskID: "CORE-318", taskKey: "primary", agentID: "core-fern", inspector: .task, inspectorPresented: true)
        ] { XCTAssertFalse(saved.isValid(in: farm)) }
    }

    @MainActor
    func testWardrobeRestoresAnIndependentlySelectedValidAgent() async {
        let session = FarmSession()
        session.open(.wardrobe)
        session.selectedAgentID = "core-fern"
        session.selectShellState(.loading)
        session.selectShellState(.ready)
        XCTAssertEqual(session.selectedTaskKey, "primary")
        XCTAssertEqual(session.selectedAgentID, "core-fern")
        XCTAssertEqual(session.inspectorSelection, .wardrobe)
    }

    func testShellErrorFixturesMatchCanonicalProtocolCodes() throws {
        // Consume the repository's golden cases directly; no second authority or
        // copied protocol fixtures in the app bundle. This is not a live client.
        let repository = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
        for (name, expected) in [("revoked-denied", ShellState.denied), ("expired-denied", .denied), ("cross-workspace-denied", .denied), ("unsupported-version", .unsupported)] {
            let url = repository.appendingPathComponent("contracts/operator/v1/testdata/\(name).json")
            let fixture = try JSONDecoder().decode(ErrorFixture.self, from: Data(contentsOf: url))
            XCTAssertFalse(fixture.response.ok)
            XCTAssertEqual(ShellState.fixtureError(code: fixture.response.error.code), expected)
        }
        XCTAssertNil(ShellState.fixtureError(code: "unknown_future_error"))
    }
}

private struct ErrorFixture: Decodable {
    struct Response: Decodable {
        struct Failure: Decodable { let code: String }
        let ok: Bool
        let error: Failure
    }
    let response: Response
}
