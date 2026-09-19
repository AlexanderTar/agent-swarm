// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "SwarmBar",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "SwarmBar", targets: ["SwarmBar"]),
    ],
    targets: [
        // Logic only (wire types, daemon client, rules, stores). Coverage-gated at 80 %.
        .target(name: "SwarmBarKit"),
        // SwiftUI views. Excluded from the coverage gate; covered by render smoke tests.
        .target(name: "SwarmBarUI", dependencies: ["SwarmBarKit"]),
        // App entry point and macOS service adapters (AppleScript, notifications).
        .executableTarget(name: "SwarmBar", dependencies: ["SwarmBarKit", "SwarmBarUI"]),
        .testTarget(name: "SwarmBarTests", dependencies: ["SwarmBarKit", "SwarmBarUI"]),
    ]
)
