import SwiftUI

extension Color {
    init(hex: UInt32) {
        self.init(red: Double((hex >> 16) & 255) / 255, green: Double((hex >> 8) & 255) / 255, blue: Double(hex & 255) / 255)
    }
}

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

    static let garden = FarmPalette(ink: Color(hex: 0x3C291E), creek: Color(hex: 0x375D3C), sage: Color(hex: 0x759B47), sageLight: Color(hex: 0xB6C56F), sky: Color(hex: 0xBBD3AF), gold: Color(hex: 0xF2CA67), berry: Color(hex: 0x9E443A), paper: Color(hex: 0xF6E5B9), soil: Color(hex: 0x885A36), outline: Color(hex: 0x604127))
    var highContrast: FarmPalette {
        FarmPalette(ink: Color(hex: 0x24170E), creek: Color(hex: 0x21432A), sage: sage, sageLight: sageLight, sky: sky, gold: gold, berry: Color(hex: 0x842B23), paper: Color(hex: 0xFFF0C9), soil: soil, outline: Color(hex: 0x382113))
    }
}

enum FarmStyle {
    static let wood = Color(hex: 0x80512F)
    static let darkWood = Color(hex: 0x452C20)
    static let honey = Color(hex: 0xBB8246)
    static let lightWood = Color(hex: 0xD5A45B)
    static let cream = Color(hex: 0xFFF0C9)
    static let muted = Color(hex: 0x775C40)
    static let ink = Color(hex: 0x3C291E)
    static let paper = Color(hex: 0xF6E5B9)
    static func title(_ size: CGFloat) -> Font { .custom("Georgia-Bold", size: size) }
}

struct TimberSurface: View {
    var dark = false
    var body: some View {
        GeometryReader { geometry in
            ZStack {
                (dark ? FarmStyle.darkWood : FarmStyle.wood)
                Path { path in
                    stride(from: CGFloat(5), to: geometry.size.height, by: 17).forEach { y in
                        path.move(to: CGPoint(x: 0, y: y))
                        path.addLine(to: CGPoint(x: geometry.size.width, y: y))
                    }
                }.stroke(Color.black.opacity(0.08), lineWidth: 1)
                Path { path in
                    stride(from: CGFloat(12), to: geometry.size.height, by: 34).forEach { y in
                        path.move(to: CGPoint(x: 0, y: y))
                        path.addLine(to: CGPoint(x: geometry.size.width, y: y))
                    }
                }.stroke(FarmStyle.lightWood.opacity(0.12), lineWidth: 1)
            }
        }
        .accessibilityHidden(true)
    }
}

struct ParchmentSurface: View {
    var body: some View {
        GeometryReader { geometry in
            ZStack {
                FarmStyle.paper
                Canvas { context, size in
                    for index in 0..<180 {
                        let x = CGFloat((index * 79 + 23) % max(1, Int(size.width)))
                        let y = CGFloat((index * 101 + 31) % max(1, Int(size.height)))
                        context.fill(Path(CGRect(x: x, y: y, width: 2, height: 1)), with: .color(FarmStyle.wood.opacity(0.065)))
                    }
                }
                HStack(spacing: 0) {
                    FarmStyle.honey.opacity(0.18).frame(width: 4)
                    Spacer()
                    FarmStyle.cream.opacity(0.6).frame(width: 2)
                }
            }
        }.accessibilityHidden(true)
    }
}

struct BevelFrame: ViewModifier {
    var selected = false
    func body(content: Content) -> some View {
        content
            .overlay { Rectangle().strokeBorder(FarmStyle.darkWood, lineWidth: 2) }
            .overlay { Rectangle().inset(by: 3).strokeBorder(selected ? FarmStyle.gold : FarmStyle.lightWood.opacity(0.7), lineWidth: 1) }
    }
}

extension FarmStyle { static let gold = Color(hex: 0xF2CA67) }

struct FarmButtonStyle: ButtonStyle {
    var selected = false
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .foregroundStyle(selected ? FarmStyle.ink : FarmStyle.cream)
            .padding(.horizontal, 12).padding(.vertical, 8)
            .background(selected ? FarmStyle.paper : configuration.isPressed ? FarmStyle.darkWood : FarmStyle.wood)
            .modifier(BevelFrame(selected: selected))
            .contentShape(Rectangle())
    }
}

struct StateBadge: View {
    let state: SampleState
    var body: some View {
        Label(state.title, systemImage: state.symbol)
            .font(.system(size: 11, weight: .semibold))
            .foregroundStyle(statusColor(state))
            .padding(.horizontal, 7).padding(.vertical, 4)
            .background(FarmStyle.cream)
            .overlay { Rectangle().strokeBorder(statusColor(state).opacity(0.5), lineWidth: 1) }
            .fixedSize()
    }
}

func statusColor(_ state: SampleState) -> Color {
    switch state {
    case .running: Color(hex: 0x315E36)
    case .waiting: Color(hex: 0x795313)
    case .blocked: Color(hex: 0x943A2C)
    case .disconnected: Color(hex: 0x3D596C)
    case .reviewNeeded: Color(hex: 0x694875)
    case .accepted: Color(hex: 0x285840)
    }
}

// Explicit input focus preserves task activation without changing macOS settings.
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
                guard press.modifiers.intersection([.command, .control, .option]).isEmpty else { return .ignored }
                action()
                return .handled
            }
            .overlay {
                Rectangle().stroke(focusedTaskID == taskID ? palette.ink : .clear, style: StrokeStyle(lineWidth: 3, dash: [5, 3])).padding(-4).allowsHitTesting(false)
            }
    }
}

private struct FarmArtifactRenderKey: EnvironmentKey { static let defaultValue = false }
extension EnvironmentValues {
    var farmArtifactRender: Bool {
        get { self[FarmArtifactRenderKey.self] }
        set { self[FarmArtifactRenderKey.self] = newValue }
    }
}

private struct FarmRenderSkinsKey: EnvironmentKey { static let defaultValue = false }
private struct FarmRenderTimeKey: EnvironmentKey { static let defaultValue = 2.0 }
extension EnvironmentValues {
    var farmRenderSkins: Bool {
        get { self[FarmRenderSkinsKey.self] }
        set { self[FarmRenderSkinsKey.self] = newValue }
    }
    var farmRenderTime: Double {
        get { self[FarmRenderTimeKey.self] }
        set { self[FarmRenderTimeKey.self] = newValue }
    }
}
