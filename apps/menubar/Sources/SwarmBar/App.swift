import SwarmBarKit
import SwarmBarUI
import SwiftUI

// Replaced by the full app in Task 17.
@main
struct SwarmBarApp: App {
    var body: some Scene {
        MenuBarExtra {
            Text(Copy.appTitle).padding()
        } label: {
            AgentIcon(.swarm)
        }
        .menuBarExtraStyle(.window)
    }
}
