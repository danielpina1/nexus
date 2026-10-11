// swift-tools-version:6.0
//
// NexusKit — shared code for the Nexus iOS app, widgets, intents and notification extensions.
//
// NexusAPI  : generated SwiftProtobuf messages + connect-swift clients (do not edit Generated/).
// NexusCore : hub client, keys/crypto, pairing, event stream, observable store, shared models.
//
// Note: the generated code uses `nonisolated` on type declarations (SE-0449), which needs the
// Swift 6.1 compiler (Xcode 16.3+), and `_NameMap(bytecode:)` from SwiftProtobuf 1.30+.

import PackageDescription

let package = Package(
    name: "NexusKit",
    platforms: [.iOS(.v18)],
    products: [
        .library(name: "NexusAPI", targets: ["NexusAPI"]),
        .library(name: "NexusCore", targets: ["NexusCore"]),
    ],
    dependencies: [
        .package(url: "https://github.com/connectrpc/connect-swift", from: "1.0.0"),
        .package(url: "https://github.com/apple/swift-protobuf", from: "1.31.0"),
    ],
    targets: [
        .target(
            name: "NexusAPI",
            dependencies: [
                .product(name: "Connect", package: "connect-swift"),
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
            ],
            path: "Sources/NexusAPI"
        ),
        .target(
            name: "NexusCore",
            dependencies: [
                "NexusAPI",
                .product(name: "Connect", package: "connect-swift"),
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
            ],
            path: "Sources/NexusCore"
        ),
        .testTarget(
            name: "NexusCoreTests",
            dependencies: [
                "NexusCore",
                "NexusAPI",
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
            ],
            path: "Tests/NexusCoreTests"
        ),
    ],
    swiftLanguageModes: [.v5]
)
