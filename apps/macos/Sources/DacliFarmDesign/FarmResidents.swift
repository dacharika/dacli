import SwiftUI

// Only this small subtree ticks. Terrain, native task controls, journal and
// inspector are outside the 12 fps schedule; motion never changes work records.
struct FarmResidents: View {
    let farm: ProjectFarm
    let state: SampleState
    let harvest: HarvestPhase
    let appearances: [String: AgentAppearance]
    let usages: [String: AgentUsageSample]
    let selectedAgentID: String
    let motionAllowed: Bool
    let artifactRender: Bool
    let onSelect: (FarmAgent) -> Void
    @Environment(\.farmRenderTime) private var renderTime

    var body: some View {
        if artifactRender {
            residentFrame(time: renderTime, moving: true)
        } else {
            TimelineView(.animation(minimumInterval: 1.0 / 12.0, paused: !motionAllowed)) { context in
                residentFrame(time: motionAllowed ? context.date.timeIntervalSinceReferenceDate : 0, moving: motionAllowed)
            }
        }
    }

    private func residentFrame(time: Double, moving: Bool) -> some View {
        GeometryReader { geometry in
            Canvas { context, size in
                for (index, agent) in farm.agents.enumerated() {
                    let task = farm.currentTask(agentID: agent.id, state: state, harvest: harvest)
                    let activity = FixtureProjection.taskStatus(farm: farm, state: state, taskKey: task.key)
                    let pose = ResidentMotion.sample(index: index, state: activity, time: time, moving: moving)
                    let appearance = appearances[agent.id] ?? .roleDefault(agent)
                    let growth = (usages[agent.id] ?? agent.usage).growth
                    let cell = growth?.pixelSize ?? 3
                    let center = CGPoint(x: pose.x * size.width, y: pose.y * size.height + pose.bob)
                    let origin = CGPoint(x: (center.x - cell * 8).rounded(), y: (center.y - cell * 7).rounded())
                    let shadow = CGRect(x: center.x - cell * 6, y: origin.y + cell * 13, width: cell * 12, height: 5)
                    context.fill(Path(ellipseIn: shadow), with: .color(Color.black.opacity(0.16)))
                    if selectedAgentID == agent.id {
                        context.stroke(Path(ellipseIn: shadow.insetBy(dx: -4, dy: -2)), with: .color(FarmStyle.gold), lineWidth: 2)
                    }
                    if pose.walking {
                        for step in 0..<3 {
                            let trail = CGRect(x: center.x - CGFloat(step * 7 + 10), y: origin.y + cell * 14 + CGFloat(step % 2 * 3), width: 3, height: 2)
                            context.fill(Path(trail), with: .color(Color(hex: 0xCDB77B).opacity(0.65 - Double(step) * 0.15)))
                        }
                    }
                    PixelSprite.draw(context: &context, species: appearance.species, skin: appearance.skin, origin: origin, cell: cell, gait: pose.walking ? pose.gait : 0, happy: activity == .accepted)
                    drawRoleProp(context: &context, role: agent.role, origin: CGPoint(x: origin.x + cell * 15, y: origin.y + cell * 9), working: pose.working, beat: pose.gait)
                    drawEmote(context: &context, state: activity, center: CGPoint(x: center.x + cell * 5, y: origin.y - 16), time: time, moving: moving)
                }
            }
            .contentShape(Rectangle())
            .onTapGesture { location in
                // Hit-test against the same frame used to draw the residents, so
                // pointer targets follow sprites; keyboard focus stays in inventory.
                let hit = farm.agents.enumerated().first { index, agent in
                    let task = farm.currentTask(agentID: agent.id, state: state, harvest: harvest)
                    let activity = FixtureProjection.taskStatus(farm: farm, state: state, taskKey: task.key)
                    let pose = ResidentMotion.sample(index: index, state: activity, time: time, moving: moving)
                    let point = CGPoint(x: pose.x * geometry.size.width, y: pose.y * geometry.size.height + pose.bob)
                    let cell = (usages[agent.id] ?? agent.usage).growth?.pixelSize ?? 3
                    return abs(location.x - point.x) <= cell * 9 + 6 && abs(location.y - point.y) <= cell * 8 + 6
                }
                if let hit { onSelect(hit.element) }
            }
        }
        .accessibilityHidden(true)
    }

    private func drawEmote(context: inout GraphicsContext, state: SampleState, center: CGPoint, time: Double, moving: Bool) {
        let label: String
        switch state {
        case .running: label = moving && Int(time) % 4 < 2 ? "✦" : "…"
        case .waiting: label = "?"
        case .blocked: label = "!"
        case .disconnected: label = "×"
        case .reviewNeeded: label = "⌕"
        case .accepted: label = "♥"
        }
        let frame = CGRect(x: center.x - 13, y: center.y - 10, width: 26, height: 20)
        context.fill(Path(frame.offsetBy(dx: 1, dy: 2)), with: .color(FarmStyle.darkWood.opacity(0.3)))
        context.fill(Path(frame), with: .color(FarmStyle.cream))
        context.stroke(Path(frame), with: .color(statusColor(state)), lineWidth: 1)
        context.fill(Path(CGRect(x: center.x - 5, y: center.y + 10, width: 4, height: 4)), with: .color(FarmStyle.cream))
        context.draw(Text(label).font(.system(size: 15, weight: .black)).foregroundColor(statusColor(state)), at: center)
        if state == .accepted && moving {
            for spark in 0..<4 {
                let angle = time * 1.8 + Double(spark) * .pi / 2
                let x = center.x + CGFloat(cos(angle)) * 24
                let y = center.y + CGFloat(sin(angle)) * 16
                context.fill(Path(CGRect(x: x.rounded(), y: y.rounded(), width: 3, height: 3)), with: .color(FarmStyle.gold))
            }
        }
    }

    private func drawRoleProp(context: inout GraphicsContext, role: AgentRole, origin: CGPoint, working: Bool, beat: Int) {
        let shift: CGFloat = working && beat == 1 ? -4 : 0
        func block(_ x: CGFloat, _ y: CGFloat, _ width: CGFloat, _ height: CGFloat, _ color: UInt32) {
            context.fill(Path(CGRect(x: (origin.x + x).rounded(), y: (origin.y + y + shift).rounded(), width: width, height: height)), with: .color(Color(hex: color)))
        }
        switch role {
        case .builder:
            block(1, 0, 3, 13, 0x93643A); block(-2, -1, 10, 5, 0x78888A); block(-2, -2, 8, 2, 0xBDCBBC)
            if working { block(10, -5, 3, 3, 0xF2CA67); block(14, -10, 2, 2, 0xFFF0C9) }
        case .planner:
            block(0, 0, 14, 11, 0x604127); block(1, 1, 12, 9, 0xF2D594)
            block(4, 2, 2, 5, 0x64835B); block(7, 6, 4, 2, 0x4C9298); block(2, 8, 3, 1, 0xB98448)
        case .reviewer:
            block(0, -2, 12, 16, 0x93643A); block(1, 0, 10, 12, 0xFFF0C9)
            block(3, 3, 6, 1, 0xAD9667); block(3, 6, 5, 1, 0xAD9667); block(3, 9, 4, 1, 0x547849)
        }
    }
}

struct ResidentMotion {
    let x: CGFloat
    let y: CGFloat
    let gait: Int
    let bob: CGFloat
    let walking: Bool
    let working: Bool

    static func sample(index: Int, state: SampleState, time: Double, moving: Bool) -> ResidentMotion {
        let anchors: [(CGFloat, CGFloat)] = [(0.235, 0.785), (0.485, 0.79), (0.775, 0.735), (0.475, 0.36)]
        let anchor = anchors[min(max(index, 0), 3)]
        guard moving else { return ResidentMotion(x: anchor.0, y: anchor.1, gait: 0, bob: 0, walking: false, working: false) }
        let duration: Double = state == .waiting ? 24 : state == .reviewNeeded ? 18 : 12
        let phase = (time + Double(index) * 3).truncatingRemainder(dividingBy: duration) / duration
        let moves = state == .running ? phase < 0.72 : state == .waiting ? phase < 0.23 : state == .reviewNeeded ? phase < 0.4 : state == .accepted
        var x = anchor.0, y = anchor.1
        if moves {
            let travel = phase * 4
            let segment = Int(travel) % 4
            let progress = CGFloat(travel - floor(travel))
            let points: [(CGFloat, CGFloat)] = [(-0.04, 0), (0.035, 0), (0.035, 0.035), (-0.04, 0.035)]
            let a = points[segment], b = points[(segment + 1) % 4]
            x += a.0 + (b.0 - a.0) * progress
            y += a.1 + (b.1 - a.1) * progress
        }
        let gait = Int(time * (state == .waiting ? 3 : 5)) % 2
        let bob: CGFloat = state == .accepted ? CGFloat(abs(sin(time * 3))) * -4 : moves ? CGFloat(gait) * -1 : 0
        return ResidentMotion(x: x, y: y, gait: gait, bob: bob, walking: moves, working: state == .running && !moves)
    }
}

struct PixelAnimal: View {
    let species: AnimalSpecies
    var skin: CosmeticSkin = .classic
    var body: some View {
        Canvas { context, size in
            let cell = max(1, floor(min(size.width / 16, size.height / 16)))
            let origin = CGPoint(x: floor((size.width - cell * 16) / 2), y: floor((size.height - cell * 14) / 2))
            PixelSprite.draw(context: &context, species: species, skin: skin, origin: origin, cell: cell, gait: 0, happy: false)
        }.accessibilityHidden(true)
    }
}

enum PixelSprite {
    static func rows(_ species: AnimalSpecies) -> [String] {
        switch species {
        case .fox:
            ["..RR........RR..", "..RLR......RLR..", "..RLLR....RLLR..", "..RRRRRRRRRRRR..", "...RRRRRRRRRR...", "...RMRRRRMRRR...", "...RDDRRDDDRR...", "....DDDMDDD.....", "....RDDDGRR.....", "....RGGGGGRR.RR.", "....RGGGGGRRRRL.", "....RRGRGRR.RLD.", "....RRR.RRR..DD.", "...MMM..MMM....."]
        case .rabbit:
            ["...CC....CC.....", "...CK....KC.....", "...CKC..CKC.....", "...CKC..CKC.....", "...CCCCCCCC.....", "..CCCCCCCCCC....", "..CCMCCCMCCC....", "..CCCCDCCCCC....", "...CDDDDDCC.....", "....CBBBBC......", "...CBBBBBBC.....", "..DCBBBBBBCD....", "...CCB..BCC.....", "..MMM....MMM...."]
        case .raccoon:
            ["..SS........SS..", "..SDS......SDS..", "..SSSSSSSSSSSS..", "...SSSSSSSSSS...", "..SMMMMMMMMMMS..", "..SMDMSSMDMMMS..", "..SSDDDMDDDSS...", "...SSDDDDDSS....", "....SAAAASS.....", "....SAAAASS.SS..", "...SSAAAASSSMMS.", "....SSASASS.SSS.", "....SSS.SSS.MMS.", "...MMM..MMM....."]
        case .owl:
            ["...NN......NN...", "...NNNNNNNNNN...", "..NNDDNNDDNNNN..", "..NDDDDDDDDNNN..", "..NDMDDMDMDNNN..", "..NDDDDDDDDNNN..", "...NDDLLDDNNN...", "....NNLLNNNN....", "...NBBBBBBNN....", "..NNBBBBBBNNN...", "..NNBBBBBBNNN...", "...NBBBBBBNN....", "....NN..NN......", "...LLL..LLL....."]
        case .cat:
            ["...TT......TT...", "...TKT....TKT...", "...TTTTTTTTTT...", "..TTTTTTTTTTTT..", "..TTMTTTTMTTTT..", "..TTDDTTDDTTTT..", "...DDDKDDDDD....", "....TDDDDDTT....", "....TGGGGTT.....", "....TGGGGGTT..T.", "...TTGGGGGTT.TT.", "....TTGTGTTTTT..", "....TTT.TTT.....", "...MMM..MMM....."]
        case .bear:
            ["...NN......NN...", "..NDNN....NNDN..", "..NNNNNNNNNNNN..", "..NNNNNNNNNNNN..", "..NNMNNNNMNNNN..", "..NNDDDDDDNNNN..", "...DDDMDDDDNN...", "....NDDDDNN.....", "...NNAAAANNN....", "..NNNAAAANNNN...", "..DNNAAAANNND...", "...NNAAAANNN....", "....NNN.NNN.....", "...MMM..MMM....."]
        }
    }

    static func draw(context: inout GraphicsContext, species: AnimalSpecies, skin: CosmeticSkin, origin: CGPoint, cell: CGFloat, gait: Int, happy: Bool) {
        var colors: [Character: UInt32] = ["R": 0xC46C37, "L": 0xECA857, "D": 0xF9DFA9, "M": 0x3D3027, "C": 0xDDCCAA, "K": 0xC78A8D, "S": 0x879087, "G": 0x5A7E58, "B": 0x7F8FA2, "A": 0xAF7654, "N": 0x836344, "T": 0xC49C72]
        if skin != .classic { for code: Character in ["G", "B", "A"] { colors[code] = skin.color } }
        for (y, row) in rows(species).enumerated() {
            for (x, character) in row.enumerated() {
                guard let color = colors[character] else { continue }
                let footShift: CGFloat = y >= 12 && gait == 1 ? (x < 8 ? -1 : 1) : 0
                let rect = CGRect(x: origin.x + CGFloat(x) * cell, y: origin.y + CGFloat(y) * cell + footShift * cell, width: cell, height: cell)
                context.fill(Path(rect), with: .color(Color(hex: color)))
            }
        }
        if skin == .rain {
            context.fill(Path(CGRect(x: origin.x + 3 * cell, y: origin.y + 3 * cell, width: 10 * cell, height: 2 * cell)), with: .color(Color(hex: 0xD3AB4C)))
        }
        if happy {
            context.fill(Path(CGRect(x: origin.x + 5 * cell, y: origin.y + 7 * cell, width: cell, height: cell)), with: .color(Color(hex: 0xC78A8D)))
            context.fill(Path(CGRect(x: origin.x + 10 * cell, y: origin.y + 7 * cell, width: cell, height: cell)), with: .color(Color(hex: 0xC78A8D)))
        }
    }
}
