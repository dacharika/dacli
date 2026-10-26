import SwiftUI

// Original artwork, authored on a 400×340 pixel coordinate system. All primitive
// edges are snapped to whole logical points; no game sprites or assets are used.
struct FarmLandscape: View {
    var harvest: HarvestPhase? = nil
    var hasAdoption = false
    var body: some View {
        Canvas { context, size in
            var art = PixelFarmPainter(context: context, size: size, harvest: harvest, hasAdoption: hasAdoption)
            art.draw()
        }
        .accessibilityHidden(true)
    }
}

private struct PixelFarmPainter {
    var context: GraphicsContext
    let size: CGSize
    var harvest: HarvestPhase?
    var hasAdoption: Bool
    private var sx: CGFloat { size.width / 400 }
    private var sy: CGFloat { size.height / 340 }

    mutating func rect(_ x: CGFloat, _ y: CGFloat, _ w: CGFloat, _ h: CGFloat, _ color: UInt32) {
        let x1 = (x * sx).rounded(), y1 = (y * sy).rounded()
        let x2 = ((x + w) * sx).rounded(), y2 = ((y + h) * sy).rounded()
        context.fill(Path(CGRect(x: x1, y: y1, width: max(1, x2 - x1), height: max(1, y2 - y1))), with: .color(Color(hex: color)))
    }

    mutating func draw() {
        rect(0, 0, 400, 340, 0x83A94B)
        // Reproducible, sparse tile variations establish grass without noise blur.
        for index in 0..<1200 {
            let x = CGFloat((index * 73 + 7) % 400), y = CGFloat((index * 47 + 9) % 340)
            let color: UInt32 = index % 4 == 0 ? 0x94B755 : index % 4 == 1 ? 0x789D43 : 0x88AC4D
            rect(x, y, index % 3 == 0 ? 3 : 1, 1, color)
            if index % 7 == 0 { rect(x + 1, y - 1, 1, 3, 0x70963F) }
        }
        // Dark wooded northern boundary and a broken trail into the farm.
        rect(0, 0, 400, 17, 0x405E36)
        rect(0, 17, 400, 5, 0x547739)
        path()
        pond()
        fence(x: 14, y: 132, length: 114)
        fence(x: 272, y: 132, length: 114)
        house(x: 37, y: 46)
        barn(x: 283, y: 54)
        board(x: 232, y: 78)
        for x in stride(from: CGFloat(-8), through: 410, by: 30) {
            tree(x: x, y: -9 + CGFloat(Int(x) % 3) * 3, pine: Int(x) % 60 == 0)
        }
        for y in stride(from: CGFloat(56), through: 300, by: 48) {
            tree(x: -14, y: y, pine: true)
            tree(x: 380, y: y + 10, pine: false)
        }
        for (index, x) in [CGFloat(50), 153, 256].enumerated() {
            let stage = index == 0 && harvest != nil ? (harvest == .harvested ? -1 : harvest == .fallow ? -2 : -3) : index
            plot(x: x, y: 179, stage: stage)
        }
        workshopProps()
        if hasAdoption { plot(x: 153, y: 59, stage: -3) }
        // Vegetable crates, path stones, tools, and flowers give the farm scale.
        crate(x: 125, y: 101); crate(x: 137, y: 105)
        rect(126, 97, 3, 4, 0xC46B36); rect(131, 98, 4, 3, 0xD7A33B)
        for index in 0..<22 {
            let x = CGFloat((index * 67 + 34) % 365), y = CGFloat(258 + (index * 17) % 70)
            if x > 255 { continue }
            flower(x: x, y: y, purple: index % 3 == 0)
        }
        tree(x: 20, y: 268, pine: false)
        tree(x: 73, y: 294, pine: true)
        tree(x: 209, y: 278, pine: false)
        fence(x: 97, y: 314, length: 88)
        rect(176, 286, 7, 5, 0xAEB09A); rect(178, 284, 4, 2, 0xC4C6AC)
        rect(184, 289, 5, 3, 0x697D54)
        rect(23, 228, 7, 15, 0xB27C40); rect(21, 226, 11, 4, 0xE0AE60)
        rect(24, 229, 5, 2, 0x76502D)
    }

    mutating func path() {
        rect(133, 106, 107, 27, 0xC5A268)
        rect(168, 87, 25, 245, 0xC5A268)
        rect(31, 144, 340, 28, 0xC5A268)
        rect(42, 148, 318, 20, 0xDDC086)
        rect(172, 96, 17, 244, 0xDDC086)
        rect(72, 113, 112, 13, 0xDDC086)
        rect(73, 104, 18, 51, 0xDDC086)
        rect(314, 112, 17, 44, 0xDDC086)
        for index in 0..<75 {
            let x = CGFloat(40 + (index * 43) % 320), y = CGFloat(150 + (index * 7) % 15)
            rect(x, y, 2, 1, 0xC8A773)
        }
        for index in 0..<40 { rect(CGFloat(174 + index % 12), CGFloat(100 + index * 6), 1, 2, 0xD1AE74) }
    }

    mutating func pond() {
        let steps: [(CGFloat, CGFloat, CGFloat, CGFloat)] = [(286, 251, 66, 7), (272, 258, 94, 9), (264, 267, 111, 34), (273, 301, 94, 13), (291, 314, 58, 6)]
        for (x, y, w, h) in steps { rect(x - 4, y - 4, w + 8, h + 8, 0x6C8E42) }
        for (x, y, w, h) in steps { rect(x - 1, y - 1, w + 2, h + 2, 0xC4AF6B) }
        for (x, y, w, h) in steps { rect(x, y, w, h, 0x4C9298) }
        rect(275, 267, 90, 29, 0x5DA5A4)
        rect(291, 257, 55, 3, 0x8CC4B2)
        rect(268, 275, 2, 19, 0x8CC4B2)
        for index in 0..<18 {
            rect(CGFloat(278 + (index * 17) % 78), CGFloat(268 + (index * 11) % 34), CGFloat(3 + index % 6), 1, index % 2 == 0 ? 0x85C4BB : 0x397F8C)
        }
        rect(325, 278, 10, 4, 0x3D754B); rect(327, 276, 6, 2, 0x6D9652)
        rect(330, 275, 2, 2, 0xF3D18A)
        rect(276, 300, 8, 3, 0x377C49)
        for x in [CGFloat(264), 270, 359, 365] { rect(x, 302, 1, 12, 0x54733B); rect(x - 1, 301, 3, 4, 0x886535) }
        // Small wooden dock.
        rect(293, 241, 20, 24, 0x704B31)
        for y in stride(from: CGFloat(242), through: 262, by: 5) { rect(294, y, 18, 3, 0xBC8D50) }
        rect(291, 240, 3, 26, 0x513C28); rect(312, 240, 3, 26, 0x513C28)
    }

    mutating func tree(x: CGFloat, y: CGFloat, pine: Bool) {
        rect(x + 9, y + 37, 27, 6, 0x65863B)
        rect(x + 19, y + 23, 7, 20, 0x6A482C)
        rect(x + 20, y + 25, 2, 17, 0x9C713B)
        if pine {
            for (i, width) in [14, 24, 34, 40].enumerated() {
                let yy = y + CGFloat(i * 8)
                rect(x + 23 - CGFloat(width) / 2, yy + 5, CGFloat(width), 9, 0x345B36)
                rect(x + 23 - CGFloat(width) / 2 + 3, yy + 2, CGFloat(width - 6), 9, 0x49753B)
                rect(x + 23 - CGFloat(width) / 2 + 5, yy + 3, CGFloat(width - 12), 3, 0x638A42)
            }
        } else {
            rect(x + 7, y + 6, 33, 23, 0x365E35)
            rect(x + 2, y + 13, 42, 14, 0x365E35)
            rect(x + 12, y, 22, 34, 0x365E35)
            rect(x + 9, y + 4, 28, 23, 0x4F7C3C)
            rect(x + 5, y + 12, 35, 12, 0x4F7C3C)
            rect(x + 14, y + 2, 17, 7, 0x72984A)
            rect(x + 8, y + 10, 15, 5, 0x658F43)
            rect(x + 26, y + 17, 10, 6, 0x628A40)
            rect(x + 13, y + 24, 9, 5, 0x416A34)
            rect(x + 30, y + 7, 4, 4, 0x82A451)
        }
    }

    mutating func house(x: CGFloat, y: CGFloat) {
        rect(x + 4, y + 57, 81, 7, 0x678641)
        rect(x + 10, y + 24, 67, 35, 0x705032)
        rect(x + 12, y + 26, 63, 31, 0xD5AE70)
        for yy in stride(from: CGFloat(29), through: 57, by: 7) { rect(x + 12, y + yy, 63, 1, 0xB98F57) }
        rect(x + 37, y + 36, 16, 23, 0x5B422B); rect(x + 39, y + 38, 12, 20, 0x99733C)
        rect(x + 48, y + 48, 2, 2, 0xF1D381)
        for xx in [CGFloat(19), 59] {
            rect(x + xx, y + 33, 12, 13, 0x68492E); rect(x + xx + 2, y + 35, 8, 9, 0x87B8AD)
            rect(x + xx + 5, y + 35, 1, 9, 0xE8C88C); rect(x + xx + 2, y + 39, 8, 1, 0xE8C88C)
            rect(x + xx - 2, y + 46, 16, 3, 0x9B703E)
        }
        // Stepped rust roof with shingle bands.
        for row in 0..<8 {
            let inset = CGFloat(7 - row) * 4
            rect(x + inset, y + CGFloat(row * 4), 87 - inset * 2, 5, row % 2 == 0 ? 0x9D4F35 : 0xB2633E)
            rect(x + inset, y + CGFloat(row * 4), 2, 4, 0x723D2F)
        }
        rect(x - 1, y + 31, 90, 4, 0x613F2C)
        rect(x + 65, y + 1, 9, 16, 0x816B52); rect(x + 63, y, 13, 3, 0xA18B68)
        rect(x + 29, y + 59, 33, 3, 0xA38551); rect(x + 32, y + 62, 27, 3, 0xC9AB71)
    }

    mutating func barn(x: CGFloat, y: CGFloat) {
        rect(x + 2, y + 57, 72, 6, 0x65863B)
        rect(x + 7, y + 21, 61, 38, 0x71432E)
        rect(x + 9, y + 22, 57, 35, 0xA76B3F)
        for xx in stride(from: CGFloat(11), through: 65, by: 7) { rect(x + xx, y + 24, 1, 32, 0x83502F) }
        rect(x + 27, y + 33, 22, 26, 0x5D3F2A); rect(x + 29, y + 35, 18, 23, 0x8A6136)
        rect(x + 37, y + 35, 2, 23, 0xD5AB68)
        for row in 0..<7 {
            let inset = CGFloat(6 - row) * 4
            rect(x + inset, y + CGFloat(row * 4), 76 - inset * 2, 5, row % 2 == 0 ? 0x526D62 : 0x638277)
        }
        rect(x - 1, y + 27, 78, 4, 0x3D5044)
        rect(x + 32, y + 12, 12, 12, 0xDAB77B); rect(x + 35, y + 15, 6, 6, 0x4A5D4D)
        crate(x: x + 72, y: y + 47)
    }

    mutating func board(x: CGFloat, y: CGFloat) {
        rect(x + 5, y + 8, 3, 30, 0x65452A); rect(x + 30, y + 8, 3, 30, 0x65452A)
        rect(x, y + 2, 39, 25, 0x68452B); rect(x + 3, y + 5, 33, 18, 0xB98445)
        rect(x + 6, y + 8, 11, 12, 0xF6DEA1); rect(x + 21, y + 10, 10, 10, 0xE2C88D)
        rect(x + 8, y + 10, 7, 1, 0xB09A6B); rect(x + 8, y + 13, 6, 1, 0xB09A6B)
        rect(x - 2, y, 43, 4, 0x885531)
    }

    mutating func fence(x: CGFloat, y: CGFloat, length: CGFloat) {
        rect(x, y + 5, length, 3, 0x855F35); rect(x, y + 12, length, 3, 0xBA914C)
        for xx in stride(from: x, through: x + length, by: 15) {
            rect(xx, y, 4, 21, 0x7A542F); rect(xx, y, 3, 17, 0xD3AA60); rect(xx, y, 3, 2, 0xEDCA83)
        }
    }

    mutating func plot(x: CGFloat, y: CGFloat, stage: Int) {
        rect(x - 3, y - 3, 88, 65, 0x708B3E)
        rect(x - 1, y - 1, 84, 61, 0xB18A4D)
        rect(x, y, 82, 58, 0x765035)
        for row in 0..<5 {
            rect(x + 2, y + CGFloat(row * 11 + 3), 78, 6, 0x8E5E3A)
            rect(x + 2, y + CGFloat(row * 11 + 9), 78, 2, 0x63452D)
            for col in 0..<7 {
                let xx = x + CGFloat(col * 11 + 7), yy = y + CGFloat(row * 11 + 4)
                if stage == -2 { continue }
                if stage == -1 { rect(xx, yy + 3, 2, 3, 0xC3A157); continue }
                if stage == -3 { rect(xx, yy + 3, 2, 3, 0x86A856); rect(xx - 2, yy + 2, 2, 1, 0xB5C775); continue }
                rect(xx, yy + 3, 2, 4, 0x385731)
                rect(xx - 3, yy, 4, 3, 0x599344); rect(xx + 2, yy - 2, 3, 4, 0x75AA4C)
                if stage == 2 { rect(xx - 1, yy - 1, 4, 4, 0xCC8D3C); rect(xx, yy - 2, 2, 2, 0xEBBC55) }
                if stage == 0 { rect(xx - 2, yy + 1, 4, 3, 0x88B258) }
            }
        }
    }

    mutating func workshopProps() {
        // A builder's bench, planner's map table and review shelf belong to
        // roles visually; none of these props grant capabilities.
        rect(121, 267, 25, 4, 0xA67842); rect(123, 271, 3, 12, 0x69482C); rect(141, 271, 3, 12, 0x69482C)
        rect(126, 264, 3, 4, 0x8D9690); rect(132, 263, 8, 3, 0xB9B69A)
        rect(211, 255, 21, 3, 0xA67842); rect(213, 258, 2, 12, 0x69482C); rect(228, 258, 2, 12, 0x69482C)
        rect(214, 251, 14, 5, 0xF4DCA2); rect(219, 252, 2, 3, 0x6F8C4C); rect(223, 253, 3, 1, 0x4C9298)
        rect(350, 226, 17, 3, 0x815B34); rect(351, 229, 2, 12, 0x69482C); rect(364, 229, 2, 12, 0x69482C)
        rect(354, 215, 10, 11, 0xE8CD92); rect(356, 218, 6, 1, 0xAC915D); rect(356, 221, 4, 1, 0xAC915D)
        for index in 0..<24 {
            rect(CGFloat(38 + index * 6), CGFloat(252 + index % 3), 4, 2, 0xA6A16A)
        }
        if harvest == .harvested {
            crate(x: 42, y: 239); crate(x: 55, y: 242)
            rect(44, 237, 7, 3, 0xE3BA57); rect(57, 239, 7, 3, 0xD68D3A)
        }
        // Seed sacks and a wheelbarrow on the southern trail.
        rect(236, 300, 9, 11, 0xC3A66A); rect(238, 298, 5, 3, 0x9B804D)
        rect(147, 293, 18, 6, 0x8F6840); rect(149, 290, 14, 4, 0xB6BD78); rect(151, 299, 4, 4, 0x4D4B36)
        rect(164, 292, 7, 2, 0xAD8E52)
    }

    mutating func crate(x: CGFloat, y: CGFloat) {
        rect(x, y, 12, 11, 0x77502D); rect(x + 1, y + 1, 10, 8, 0xB98B48)
        rect(x + 1, y + 4, 10, 1, 0x7E5932); rect(x + 5, y + 1, 1, 8, 0xE2B66A)
    }
    mutating func flower(x: CGFloat, y: CGFloat, purple: Bool) {
        rect(x + 1, y + 2, 1, 5, 0x4E7938); rect(x - 1, y + 4, 2, 1, 0x5B853D)
        rect(x, y, 3, 3, purple ? 0xB498AC : 0xEAC76C); rect(x + 1, y + 1, 1, 1, 0xF8E4A3)
    }
}

struct FarmCanvas: View {
    @ObservedObject var session: FarmSession
    let palette: FarmPalette
    @FocusState.Binding var focusedTaskID: String?
    let motionAllowed: Bool
    let artifactRender: Bool
    let onInspect: (InspectorSelection) -> Void
    private var farm: ProjectFarm { session.farm }

    var body: some View {
        GeometryReader { geometry in
            ZStack(alignment: .topLeading) {
                FarmLandscape(harvest: session.sampleState == .accepted ? session.harvestPhase : nil, hasAdoption: session.adoptions[farm.id] != nil)
                FarmResidents(farm: farm, state: session.sampleState, harvest: session.harvestPhase, appearances: session.appearances, usages: Dictionary(uniqueKeysWithValues: farm.agents.map { ($0.id, session.usage(for: $0)) }), selectedAgentID: session.selectedAgentID, motionAllowed: motionAllowed, artifactRender: artifactRender) { agent in
                    session.selectedAgentID = agent.id
                    session.selectedTaskKey = farm.currentTask(agentID: agent.id, state: session.sampleState, harvest: session.harvestPhase).key
                    onInspect(.agent)
                }
                worldButton("Notice board", systemImage: "note.text", x: 0.60, y: 0.355, size: geometry.size) { onInspect(.notice) }
                worldButton("Storehouse", systemImage: "shippingbox", x: 0.82, y: 0.355, size: geometry.size) { onInspect(.storehouse) }
                ForEach(Array(farm.landTasks(state: session.sampleState, harvest: session.harvestPhase).enumerated()), id: \.offset) { index, item in
                    if let task = item {
                        let state = FixtureProjection.taskStatus(farm: farm, state: session.sampleState, taskKey: task.key)
                        TaskPlotSign(task: task, state: state, selected: session.selectedTaskKey == task.key, width: max(92, min(130, geometry.size.width * 0.225)), palette: palette) { select(task) }
                            .modifier(TaskKeyboardFocus(taskID: task.id, palette: palette, focusedTaskID: $focusedTaskID) { select(task) })
                            .position(x: geometry.size.width * (index == 3 ? 0.49 : 0.225 + CGFloat(index) * 0.2575), y: geometry.size.height * (index == 3 ? 0.18 : 0.485))
                            .accessibilityLabel("\(task.kind.rawValue) plot \(task.id), \(task.title), \(state == .accepted ? "Done, harvested" : "Open, " + state.title). Select for evidence.")
                    } else {
                        fallowSign.position(x: geometry.size.width * 0.225, y: geometry.size.height * 0.485)
                    }
                }
                VStack(alignment: .leading, spacing: 3) {
                    Text("\(farm.title) Farm").font(FarmStyle.title(14))
                    Text("\(session.activeTasks.count) open work plots · \(farm.projectName)").font(.system(size: 11, weight: .medium))
                }
                .frame(width: 155, alignment: .leading)
                .foregroundStyle(FarmStyle.cream).padding(.horizontal, 12).padding(.vertical, 9)
                .background { TimberSurface(dark: true) }.modifier(BevelFrame()).padding(14)
                VStack(alignment: .leading, spacing: 6) {
                    if session.sampleState == .accepted {
                        HStack(spacing: 6) {
                            Text("Sample lifecycle: \(session.harvestPhase.rawValue)").font(.system(size: 10, weight: .bold))
                            if session.harvestPhase == .harvested || (session.harvestPhase == .fallow && farm.nextOpenTask != nil) {
                                Button(session.harvestPhase == .harvested ? "Free land" : "Plant queued issue") { session.advanceHarvestPreview() }
                                    .font(.system(size: 10, weight: .bold)).buttonStyle(.bordered).controlSize(.mini)
                            } else if farm.nextOpenTask == nil {
                                Text("Backlog empty").font(.system(size: 10, weight: .semibold))
                            }
                        }.padding(7).background(FarmStyle.cream).overlay { Rectangle().strokeBorder(FarmStyle.wood, lineWidth: 1) }
                    }
                    HStack(spacing: 4) {
                        Image(systemName: "cursorarrow")
                        Text(artifactRender ? "Sample walking pose · farmhands are selectable" : motionAllowed ? "Click a farmhand or plot · agents are moving" : "Select a farmhand or plot · motion paused")
                    }.font(.system(size: 10, weight: .medium)).padding(.horizontal, 9).padding(.vertical, 6)
                        .background(FarmStyle.cream.opacity(0.95)).overlay { Rectangle().strokeBorder(FarmStyle.wood, lineWidth: 1) }
                }
                .foregroundStyle(palette.ink).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading).padding(12)
            }
            .clipped().overlay { Rectangle().strokeBorder(FarmStyle.darkWood, lineWidth: 3).allowsHitTesting(false) }
        }
        .accessibilityElement(children: .contain).accessibilityLabel("\(farm.title) task farm, \(session.activeTasks.count) open work plots")
    }

    @ViewBuilder
    private var fallowSign: some View {
        if farm.nextOpenTask != nil {
            Button { session.advanceHarvestPreview() } label: { FallowLandSign(empty: false, palette: palette) }
                .buttonStyle(.plain).accessibilityLabel("Fallow land. Preview the next queued open issue on this freed plot.")
        } else {
            FallowLandSign(empty: true, palette: palette)
                .accessibilityElement(children: .combine).accessibilityLabel("Fallow land. Backlog empty; no queued open work.")
        }
    }

    private func select(_ task: FarmTask) { session.select(task); onInspect(.task) }
    private func worldButton(_ title: String, systemImage: String, x: CGFloat, y: CGFloat, size: CGSize, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Label(title, systemImage: systemImage).font(.system(size: 10, weight: .semibold)).foregroundStyle(palette.ink)
                .padding(5).background(FarmStyle.cream).overlay { Rectangle().strokeBorder(FarmStyle.wood, lineWidth: 1) }
        }.buttonStyle(.plain).position(x: size.width * x, y: size.height * y)
    }
}

private struct TaskPlotSign: View {
    let task: FarmTask
    let state: SampleState
    let selected: Bool
    let width: CGFloat
    let palette: FarmPalette
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(spacing: 3) {
                Text("\(state == .accepted ? "Done" : "Open") \(task.kind.rawValue) \(task.id)").font(.system(size: 8, weight: .bold, design: .monospaced))
                Text(task.plotTitle).font(.system(size: 12, weight: .bold)).lineLimit(2).multilineTextAlignment(.center)
                Label(state == .accepted ? "Harvested" : state.title, systemImage: state.symbol).font(.system(size: 10, weight: .semibold)).fixedSize(horizontal: false, vertical: true)
            }
            .foregroundStyle(palette.ink)
            .padding(.horizontal, 7).padding(.vertical, 6)
            .frame(width: width).frame(minHeight: 61)
            .background(selected ? FarmStyle.cream : Color(hex: 0xE8CB8D))
            .modifier(BevelFrame(selected: selected))
            .overlay(alignment: .top) {
                if selected {
                    Text("▾").font(.system(size: 22, weight: .black)).foregroundStyle(FarmStyle.cream).offset(y: -25)
                }
            }
        }.buttonStyle(.plain)
    }
}

private struct FallowLandSign: View {
    let empty: Bool
    let palette: FarmPalette
    var body: some View {
        VStack(spacing: 4) {
            Label("Fallow land", systemImage: "leaf")
            Text(empty ? "No queued open work" : "Ready for a queued issue").font(.system(size: 10))
        }.font(.system(size: 12, weight: .bold)).foregroundStyle(palette.ink).padding(9)
            .background(FarmStyle.cream).modifier(BevelFrame())
    }
}
