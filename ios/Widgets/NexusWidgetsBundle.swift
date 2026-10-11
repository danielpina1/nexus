import SwiftUI
import WidgetKit

@main
struct NexusWidgetsBundle: WidgetBundle {
    var body: some Widget {
        InboxWidget()
        MachinesWidget()
        FavouritesWidget()
        LockScreenWidget()
        RunLiveActivity()
        PulseLiveActivity()
        KillSwitchControl()
        QuickCaptureControl()
        TalkToNexusControl()
    }
}
