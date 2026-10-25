// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "DacliFarmDesign",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "DacliFarmDesign", targets: ["DacliFarmDesign"])
    ],
    targets: [
        .executableTarget(name: "DacliFarmDesign"),
        .testTarget(name: "DacliFarmDesignTests", dependencies: ["DacliFarmDesign"])
    ]
)
