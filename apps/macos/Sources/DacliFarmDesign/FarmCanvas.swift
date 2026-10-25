import SwiftUI

struct FarmPalette {
    let ink: Color
    let creek: Color
    let sage: Color
    let sageLight: Color
    let sky: Color
    let gold: Color
    let berry: Color
    let paper: Color
    let soil: Color
    let outline: Color

    static let garden = FarmPalette(
        ink: Color(red: 0.10, green: 0.23, blue: 0.24),
        creek: Color(red: 0.09, green: 0.30, blue: 0.30),
        sage: Color(red: 0.62, green: 0.75, blue: 0.55),
        sageLight: Color(red: 0.79, green: 0.86, blue: 0.69),
        sky: Color(red: 0.82, green: 0.92, blue: 0.90),
        gold: Color(red: 0.95, green: 0.78, blue: 0.37),
        berry: Color(red: 0.68, green: 0.30, blue: 0.25),
        paper: Color(red: 1.00, green: 0.99, blue: 0.94),
        soil: Color(red: 0.48, green: 0.36, blue: 0.24),
        outline: Color(red: 0.22, green: 0.40, blue: 0.37)
    )

    var highContrast: FarmPalette {
        FarmPalette(
            ink: Color(red: 0.02, green: 0.12, blue: 0.13),
            creek: Color(red: 0.03, green: 0.24, blue: 0.24),
            sage: Color(red: 0.53, green: 0.69, blue: 0.45),
            sageLight: Color(red: 0.81, green: 0.90, blue: 0.67),
            sky: Color(red: 0.80, green: 0.94, blue: 0.91),
            gold: Color(red: 0.98, green: 0.75, blue: 0.20),
            berry: Color(red: 0.58, green: 0.18, blue: 0.13),
            paper: .white,
            soil: Color(red: 0.39, green: 0.26, blue: 0.13),
            outline: Color(red: 0.03, green: 0.19, blue: 0.17)
        )
    }
}

// Plain macOS buttons only receive activation focus when system Keyboard Navigation
// is enabled. Task navigation also consumes keyboard input, so its explicit focus
// target must work without changing that preference (native review, issue #1130).
struct TaskKeyboardFocus: ViewModifier {
    let taskID: String
    let palette: FarmPalette
    @FocusState.Binding var focusedTaskID: String?
    let action: () -> Void

    func body(content: Content) -> some View {
        content
            .focusable(interactions: .edit)
            .focused($focusedTaskID, equals: taskID)
            .onKeyPress(keys: [.space, .return], phases: .down) { press in
                guard press.modifiers.intersection([.command, .control, .option]).isEmpty else {
                    return .ignored
                }
                action()
                return .handled
            }
            .overlay {
                RoundedRectangle(cornerRadius: 8)
                    .stroke(focusedTaskID == taskID ? palette.creek : .clear, style: StrokeStyle(lineWidth: 2, dash: [4, 3]))
                    .padding(-3)
                    .allowsHitTesting(false)
            }
    }
}

struct FarmCanvas: View {
    let farm: ProjectFarm
    let sampleState: SampleState
    let palette: FarmPalette
    @Binding var selectedTaskKey: String
    @Binding var selectedAgentID: String
    @FocusState.Binding var focusedTaskID: String?
    let onInspect: (InspectorSelection) -> Void

    var body: some View {
        ZStack {
            FarmBackdrop(palette: palette)
            VStack(spacing: 10) {
                HStack(alignment: .top) {
                    BuildingButton(title: "Notice board", count: "2", symbol: "note.text", palette: palette) {
                        onInspect(.notice)
                    }
                    Spacer()
                    BuildingButton(title: "Storehouse", count: "1", symbol: "shippingbox.fill", palette: palette) {
                        onInspect(.storehouse)
                    }
                }
                Spacer(minLength: 8)
                HStack(alignment: .bottom, spacing: 9) {
                    ForEach(farm.tasks) { task in
                        PlotButton(
                            task: task,
                            state: taskState(task),
                            selected: selectedTaskKey == task.key,
                            palette: palette,
                            focusedTaskID: $focusedTaskID
                        ) {
                            selectedTaskKey = task.key
                            selectedAgentID = task.agentID
                            onInspect(.task)
                        }
                    }
                }
                Spacer(minLength: 5)
                HStack(alignment: .bottom, spacing: 12) {
                    ForEach(farm.agents) { agent in
                        AgentButton(
                            agent: agent,
                            status: agentStatus(agent),
                            selected: selectedAgentID == agent.id,
                            palette: palette
                        ) {
                            selectedAgentID = agent.id
                            if let task = farm.tasks.first(where: { $0.agentID == agent.id }) {
                                selectedTaskKey = task.key
                            }
                            onInspect(.agent)
                        }
                    }
                }
            }
            .padding(15)
        }
        .frame(minHeight: 380, idealHeight: 400, maxHeight: 440)
        .clipShape(RoundedRectangle(cornerRadius: 15, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 15, style: .continuous)
                .stroke(palette.outline.opacity(0.30), lineWidth: 1)
                .allowsHitTesting(false)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Synthetic farm scene for \(farm.title)")
    }

    private func taskState(_ task: FarmTask) -> SampleState {
        FixtureProjection.taskStatus(farm: farm, state: sampleState, taskKey: task.key)
    }

    private func agentStatus(_ agent: FarmAgent) -> SampleState {
        guard let task = farm.tasks.first(where: { $0.agentID == agent.id }) else { return .waiting }
        return taskState(task)
    }
}

private struct FarmBackdrop: View {
    let palette: FarmPalette

    var body: some View {
        Canvas(opaque: true, colorMode: .linear) { context, size in
            let bounds = CGRect(origin: .zero, size: size)
            context.fill(Path(bounds), with: .color(palette.sky))

            let sunRect = CGRect(x: size.width * 0.76, y: 28, width: 30, height: 30)
            context.fill(Path(ellipseIn: sunRect.insetBy(dx: -7, dy: -7)), with: .color(palette.gold.opacity(0.22)))
            context.fill(Path(sunRect), with: .color(palette.gold))

            var backHill = Path()
            backHill.move(to: CGPoint(x: 0, y: size.height * 0.45))
            backHill.addQuadCurve(to: CGPoint(x: size.width * 0.55, y: size.height * 0.43), control: CGPoint(x: size.width * 0.24, y: size.height * 0.28))
            backHill.addQuadCurve(to: CGPoint(x: size.width, y: size.height * 0.42), control: CGPoint(x: size.width * 0.76, y: size.height * 0.26))
            backHill.addLine(to: CGPoint(x: size.width, y: size.height))
            backHill.addLine(to: CGPoint(x: 0, y: size.height))
            backHill.closeSubpath()
            context.fill(backHill, with: .color(palette.sageLight))

            let fieldRect = CGRect(x: 0, y: size.height * 0.54, width: size.width, height: size.height * 0.46)
            context.fill(Path(fieldRect), with: .color(palette.sage))

            for row in 0..<8 {
                for column in 0..<18 {
                    let offset = CGFloat((row * 7 + column * 11) % 5)
                    let x = CGFloat(column) * size.width / 17
                    let y = size.height * 0.57 + CGFloat(row) * 24 + offset
                    let tuft = CGRect(x: x, y: y, width: 3, height: 3)
                    context.fill(Path(tuft), with: .color(row.isMultiple(of: 2) ? palette.sageLight.opacity(0.65) : palette.creek.opacity(0.25)))
                }
            }

            var path = Path()
            path.move(to: CGPoint(x: size.width * 0.42, y: size.height * 0.55))
            path.addCurve(to: CGPoint(x: size.width * 0.70, y: size.height), control1: CGPoint(x: size.width * 0.49, y: size.height * 0.72), control2: CGPoint(x: size.width * 0.76, y: size.height * 0.68))
            path.addLine(to: CGPoint(x: size.width * 0.90, y: size.height))
            path.addCurve(to: CGPoint(x: size.width * 0.50, y: size.height * 0.55), control1: CGPoint(x: size.width * 0.80, y: size.height * 0.76), control2: CGPoint(x: size.width * 0.54, y: size.height * 0.76))
            path.closeSubpath()
            context.fill(path, with: .color(Color(red: 0.76, green: 0.66, blue: 0.44).opacity(0.82)))

            let pond = CGRect(x: -12, y: size.height * 0.78, width: 92, height: 39)
            context.fill(Path(ellipseIn: pond), with: .color(palette.creek.opacity(0.75)))
            context.stroke(Path(ellipseIn: pond.insetBy(dx: 5, dy: 5)), with: .color(palette.sky.opacity(0.75)), lineWidth: 2)

            var fenceTop = Path()
            fenceTop.move(to: CGPoint(x: 0, y: size.height * 0.52))
            fenceTop.addLine(to: CGPoint(x: size.width, y: size.height * 0.52))
            var fenceBottom = Path()
            fenceBottom.move(to: CGPoint(x: 0, y: size.height * 0.57))
            fenceBottom.addLine(to: CGPoint(x: size.width, y: size.height * 0.57))
            context.stroke(fenceTop, with: .color(palette.soil), lineWidth: 4)
            context.stroke(fenceBottom, with: .color(palette.soil.opacity(0.85)), lineWidth: 3)
            for post in stride(from: 12.0, through: Double(size.width), by: 36.0) {
                var postPath = Path()
                postPath.move(to: CGPoint(x: post, y: size.height * 0.49))
                postPath.addLine(to: CGPoint(x: post, y: size.height * 0.60))
                context.stroke(postPath, with: .color(palette.soil), lineWidth: 4)
            }
        }
        .accessibilityHidden(true)
    }
}

private struct PlotButton: View {
    let task: FarmTask
    let state: SampleState
    let selected: Bool
    let palette: FarmPalette
    @FocusState.Binding var focusedTaskID: String?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 7) {
                HStack {
                    Text(task.id)
                        .font(.system(size: 12, weight: .bold, design: .monospaced))
                    Spacer(minLength: 3)
                    Image(systemName: state.symbol)
                        .font(.system(size: 12, weight: .bold))
                        .accessibilityHidden(true)
                }
                Text(task.plotTitle)
                    .font(.system(size: 12, weight: .semibold))
                    .lineLimit(2)
                HStack(spacing: 3) {
                    ForEach(0..<4, id: \.self) { index in
                        Rectangle()
                            .fill(index.isMultiple(of: 2) ? palette.sageLight : palette.gold.opacity(0.8))
                            .frame(width: 4, height: CGFloat(7 + index % 2 * 4))
                    }
                    Spacer(minLength: 2)
                    Text(state.title)
                        .font(.system(size: 11, weight: .semibold))
                        .multilineTextAlignment(.trailing)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .foregroundStyle(palette.ink)
            .padding(9)
            .frame(maxWidth: .infinity, minHeight: 100, alignment: .leading)
            .background {
                RoundedRectangle(cornerRadius: 5)
                    .fill(Color(red: 0.97, green: 0.91, blue: 0.72))
                    .overlay(alignment: .bottom) {
                        Rectangle().fill(palette.soil).frame(height: 5)
                    }
            }
            .overlay {
                RoundedRectangle(cornerRadius: 5)
                    .stroke(selected ? palette.berry : palette.soil, lineWidth: selected ? 3 : 2)
            }
        }
        .buttonStyle(.plain)
        .modifier(TaskKeyboardFocus(taskID: task.id, palette: palette, focusedTaskID: $focusedTaskID, action: action))
        .accessibilityLabel("Task plot \(task.id), \(task.title), \(state.title)")
        .accessibilityHint("Select to inspect task details and evidence.")
    }
}

private struct BuildingButton: View {
    let title: String
    let count: String
    let symbol: String
    let palette: FarmPalette
    let action: () -> Void
    @FocusState private var isFocused: Bool

    var body: some View {
        Button(action: action) {
            VStack(spacing: 4) {
                Image(systemName: symbol)
                    .font(.system(size: 22, weight: .medium))
                    .foregroundStyle(palette.ink)
                    .frame(width: 48, height: 44)
                    .background(palette.gold.opacity(0.80), in: RoundedRectangle(cornerRadius: 4))
                    .overlay(alignment: .bottom) {
                        Rectangle().fill(palette.soil).frame(height: 4)
                    }
                HStack(spacing: 4) {
                    Text(title).font(.system(size: 12, weight: .semibold))
                    Text(count).font(.system(size: 12, weight: .bold, design: .monospaced))
                }
                .foregroundStyle(palette.ink)
                .padding(.horizontal, 6)
                .padding(.vertical, 3)
                .background(palette.paper.opacity(0.95), in: RoundedRectangle(cornerRadius: 4))
            }
        }
        .buttonStyle(.plain)
        .focused($isFocused)
        .overlay {
            RoundedRectangle(cornerRadius: 8)
                .stroke(isFocused ? palette.creek : .clear, style: StrokeStyle(lineWidth: 2, dash: [4, 3]))
                .padding(-4)
                .allowsHitTesting(false)
        }
        .accessibilityLabel("\(title), \(count) sample records")
    }
}

private struct AgentButton: View {
    let agent: FarmAgent
    let status: SampleState
    let selected: Bool
    let palette: FarmPalette
    let action: () -> Void
    @FocusState private var isFocused: Bool

    var body: some View {
        Button(action: action) {
            VStack(spacing: 2) {
                PixelAnimal(species: agent.species)
                    .frame(width: 48, height: 48)
                Text(agent.name)
                    .font(.system(size: 11, weight: .bold))
                    .foregroundStyle(palette.ink)
                Text(status.title)
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(palette.ink)
                    .padding(.horizontal, 5)
                    .padding(.vertical, 2)
                    .background(statusColor(status).opacity(0.24), in: RoundedRectangle(cornerRadius: 4))
            }
            .padding(4)
            .background(selected ? palette.paper.opacity(0.90) : .clear, in: RoundedRectangle(cornerRadius: 8))
            .overlay {
                RoundedRectangle(cornerRadius: 8)
                    .stroke(selected ? palette.gold : .clear, lineWidth: 2)
            }
        }
        .buttonStyle(.plain)
        .focused($isFocused)
        .overlay {
            RoundedRectangle(cornerRadius: 10)
                .stroke(isFocused ? palette.creek : .clear, style: StrokeStyle(lineWidth: 2, dash: [4, 3]))
                .padding(-3)
                .allowsHitTesting(false)
        }
        .accessibilityLabel("\(agent.name), \(agent.species.rawValue) agent, \(status.title). Appearance is cosmetic.")
        .accessibilityHint("Select the agent and its task.")
    }
}

struct PixelAnimal: View {
    let species: AnimalSpecies

    private var rows: [String] {
        switch species {
        case .fox:
            [
                ".RR......RR.", ".RLR....RLR.", ".RLLRRRRLLR.",
                "..RRRRRRRR..", "..RMRRRRMR..", "..RDDDDDDR..",
                "...DDMDDD...", "...RDDRR....", "...RRDRR.RR.",
                "...RRDRRRRD.", "...RR.RR.DD.", "..MMM.MMM..."
            ]
        case .rabbit:
            [
                "..CC..CC....", "..CK..KC....", "..CK..KC....",
                "..CCCCCC....", ".CCCCCCCC...", ".CCMCCMCC...",
                ".CCCDCCCC...", "..CDDDCC....", "..CCCCCCC...",
                ".DCCCCCCCD..", "..CC..CC....", ".MMM..MMM..."
            ]
        case .raccoon:
            [
                ".SS......SS.", ".SDS....SDS.", ".SSSSSSSSSS.",
                "..SSSSSSSS..", ".SMMMMMMMMS.", ".SMDSMDSDMS.",
                ".SSDDMDDSSS.", "...SDDDSS...", "...SSSSSS.S.",
                "..SSSSSSSDS.", "...SS.SS.SS.", "..MMM.MMM..."
            ]
        }
    }

    var body: some View {
        Canvas { context, size in
            // Integral cells keep the original silhouettes crisp at every used size.
            let pixel = max(1, floor(min(size.width, size.height) / 12))
            let origin = CGPoint(x: floor((size.width - pixel * 12) / 2), y: floor((size.height - pixel * 12) / 2))
            let colors: [Character: Color] = [
                "R": Color(red: 0.73, green: 0.35, blue: 0.22),
                "L": Color(red: 0.92, green: 0.68, blue: 0.34),
                "C": Color(red: 0.82, green: 0.75, blue: 0.62),
                "K": Color(red: 0.72, green: 0.38, blue: 0.48),
                "S": Color(red: 0.43, green: 0.51, blue: 0.49),
                "D": Color(red: 0.99, green: 0.88, blue: 0.66),
                "M": Color(red: 0.10, green: 0.20, blue: 0.20)
            ]
            for (rowIndex, row) in rows.enumerated() {
                for (columnIndex, character) in row.enumerated() {
                    guard let color = colors[character] else { continue }
                    let rect = CGRect(x: origin.x + CGFloat(columnIndex) * pixel, y: origin.y + CGFloat(rowIndex) * pixel, width: pixel, height: pixel)
                    context.fill(Path(rect), with: .color(color))
                }
            }
        }
        .accessibilityHidden(true)
    }
}

func statusColor(_ state: SampleState) -> Color {
    switch state {
    case .running: Color(red: 0.20, green: 0.49, blue: 0.34)
    case .waiting: Color(red: 0.57, green: 0.43, blue: 0.14)
    case .blocked: Color(red: 0.55, green: 0.25, blue: 0.20)
    case .disconnected: Color(red: 0.30, green: 0.42, blue: 0.48)
    case .reviewNeeded: Color(red: 0.48, green: 0.30, blue: 0.43)
    case .accepted: Color(red: 0.16, green: 0.40, blue: 0.31)
    }
}
