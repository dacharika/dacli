import SwiftUI

struct FarmWardrobe: View {
    @ObservedObject var session: FarmSession
    @State private var showSkins = false
    @Environment(\.farmRenderSkins) private var renderSkins
    @Environment(\.farmArtifactRender) private var artifactRender
    @State private var adoptionRole: AgentRole = .builder
    @State private var adoptionState: SampleState = .waiting
    private var agent: FarmAgent { session.farm.agent(for: session.selectedAgentID) }
    private var appearance: AgentAppearance { session.appearance(for: agent) }

    var body: some View {
        VStack(alignment: .leading, spacing: 17) {
            HStack(spacing: 0) {
                Button("Animals") { showSkins = false }.buttonStyle(FarmButtonStyle(selected: !(showSkins || renderSkins)))
                Button("Skins") { showSkins = true }.buttonStyle(FarmButtonStyle(selected: showSkins || renderSkins))
            }.font(.system(size: 12, weight: .semibold))
            if showSkins || renderSkins { skins } else { adoption }
        }
    }

    private var adoption: some View {
        VStack(alignment: .leading, spacing: 15) {
            Text("A new farmhand").font(FarmStyle.title(24))
            Text("Adopt another agent with their own role and work plot.")
                .font(.system(size: 12)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
            if let current = session.adoptions[session.farm.id] {
                HStack(spacing: 12) {
                    PixelAnimal(species: current.agent.species).frame(width: 68, height: 68)
                    VStack(alignment: .leading, spacing: 5) {
                        Text(current.agent.name).font(FarmStyle.title(19))
                        Label(current.agent.role.rawValue, systemImage: current.agent.role.symbol).font(.system(size: 12, weight: .semibold))
                        Text("Adoption preview").font(.system(size: 10, weight: .bold)).foregroundStyle(FarmStyle.muted)
                    }
                }.padding(10).background(Color(hex: 0xE5CE9B)).modifier(BevelFrame())
                Text(current.task.title).font(.system(size: 13, weight: .semibold))
                Text(current.task.id).font(.system(size: 11, design: .monospaced))
                StateBadge(state: current.task.baseState)
                Button("Remove adoption preview") { session.removeAdoptionPreview() }.buttonStyle(.bordered).controlSize(.small)
                Text("One adoption preview at a time. Remove it to try another farmhand.").font(.system(size: 11)).foregroundStyle(FarmStyle.muted)
            } else {
                ForEach(AgentRole.allCases, id: \.self) { role in
                    Button { adoptionRole = role } label: {
                        HStack(spacing: 12) {
                            PixelAnimal(species: role.defaultSpecies).frame(width: 48, height: 48)
                            VStack(alignment: .leading, spacing: 4) {
                                Label(role.rawValue, systemImage: role.symbol).font(.system(size: 13, weight: .bold))
                                Text("\(role.defaultSpecies.rawValue) · own work plot").font(.system(size: 11))
                            }
                            Spacer(minLength: 0)
                            Image(systemName: adoptionRole == role ? "checkmark.circle.fill" : "circle").foregroundStyle(Color(hex: 0x375D3C))
                        }.padding(9).foregroundStyle(FarmStyle.ink).background(adoptionRole == role ? FarmStyle.cream : Color(hex: 0xE5CE9B))
                            .overlay { Rectangle().strokeBorder(FarmStyle.honey, lineWidth: adoptionRole == role ? 2 : 1) }
                    }.buttonStyle(.plain)
                }
                if artifactRender {
                    HStack { Text("Work sample").font(.system(size: 12)); Spacer(); StateBadge(state: adoptionState) }
                } else {
                    Picker("Work sample", selection: $adoptionState) {
                        Text("Waiting").tag(SampleState.waiting)
                        Text("Running").tag(SampleState.running)
                        Text("Blocked").tag(SampleState.blocked)
                        Text("Review needed").tag(SampleState.reviewNeeded)
                    }.font(.system(size: 12))
                }
                Button { session.previewAdoption(role: adoptionRole, state: adoptionState) } label: {
                    Label("Preview adoption", systemImage: "plus").font(.system(size: 12, weight: .bold))
                }.buttonStyle(FarmButtonStyle(selected: true))
            }
            Text("Adoption preview only · no purchase or live enrollment.")
                .font(.system(size: 11)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
        }
    }

    private var skins: some View {
        VStack(alignment: .leading, spacing: 15) {
            Text("Dress for the day").font(FarmStyle.title(24))
            Text("Skins dress an existing agent. Their role and work stay the same.")
                .font(.system(size: 12)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
            if artifactRender {
                Label("Dress \(agent.name)", systemImage: "person.crop.square").font(.system(size: 12, weight: .semibold))
            } else {
                Menu {
                    ForEach(session.farm.agents) { item in
                        Button(item.name) {
                            session.selectedAgentID = item.id
                            session.selectedTaskKey = session.farm.currentTask(agentID: item.id, state: session.sampleState, harvest: session.harvestPhase).key
                        }
                    }
                } label: {
                    Label("Dress \(agent.name)", systemImage: "person.crop.square").font(.system(size: 12, weight: .semibold))
                }
            }
            HStack(spacing: 13) {
                PixelAnimal(species: appearance.species, skin: appearance.skin).frame(width: 62, height: 64)
                    .background(Color(hex: 0xDCC18B)).modifier(BevelFrame())
                VStack(alignment: .leading, spacing: 5) {
                    Text(agent.name).font(FarmStyle.title(19))
                    Label(agent.role.rawValue, systemImage: agent.role.symbol).font(.system(size: 12, weight: .semibold))
                    Text(appearance.isPreviewOnly(for: agent) ? "Preview only" : "Sample owned").font(.system(size: 10, weight: .bold)).foregroundStyle(FarmStyle.muted)
                }
            }
            LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 9) {
                ForEach(CosmeticSkin.allCases) { skin in
                    skinButton(skin)
                }
            }
            Button { session.restoreAppearance(for: agent) } label: {
                Label("Restore role default", systemImage: "arrow.counterclockwise").font(.system(size: 12, weight: .semibold))
            }.buttonStyle(.bordered).controlSize(.small)
            Text("Try on locally. Shop ownership is a sample; purchases are unavailable.")
                .font(.system(size: 11)).lineSpacing(3).foregroundStyle(FarmStyle.muted)
        }
    }

    private func skinButton(_ skin: CosmeticSkin) -> some View {
        Button {
            session.preview(AgentAppearance(species: skin.speciesOverride ?? agent.species, skin: skin), for: agent)
        } label: {
            VStack(spacing: 5) {
                PixelAnimal(species: skin.speciesOverride ?? agent.species, skin: skin).frame(width: 44, height: 44)
                Text(skin.rawValue).font(.system(size: 11, weight: .bold)).lineLimit(2).multilineTextAlignment(.center)
                Text(skin.sampleOwned ? "Sample owned" : "Preview only").font(.system(size: 9))
                Text(appearance.skin == skin ? "Trying on" : "Try on").font(.system(size: 10, weight: .bold)).foregroundStyle(Color(hex: 0x375D3C))
            }.padding(.vertical, 8).padding(.horizontal, 4).frame(maxWidth: .infinity)
                .foregroundStyle(FarmStyle.ink).background(appearance.skin == skin ? FarmStyle.cream : Color(hex: 0xE5CE9B))
                .overlay { Rectangle().strokeBorder(FarmStyle.honey, lineWidth: appearance.skin == skin ? 2 : 1) }
        }.buttonStyle(.plain)
        .accessibilityLabel("\(skin.rawValue), \(skin.sampleOwned ? "sample owned" : "preview only"). Try on locally; \(agent.role.rawValue) role unchanged.")
    }
}
