import Connect
import Foundation
import NexusAPI

/// Connection state of the event stream, shown in the UI.
public enum ConnectionState: Equatable, Sendable {
    case disconnected
    case connecting
    case connected
    case reconnecting(attempt: Int, retryIn: TimeInterval)
    case demo

    public var label: String {
        switch self {
        case .disconnected: return "Offline"
        case .connecting: return "Connecting…"
        case .connected: return "Live"
        case .reconnecting(let attempt, let retryIn): return "Reconnecting (#\(attempt), \(Int(retryIn))s)"
        case .demo: return "Demo"
        }
    }
}

/// Keeps `EventService.Subscribe(since_seq)` open with reconnect + exponential backoff and
/// persists the last processed `seq` in the App Group so the stream resumes losslessly.
public final class EventStream: @unchecked Sendable {
    public typealias EventHandler = @MainActor @Sendable (Nexus_V1_Event) -> Void
    public typealias StateHandler = @MainActor @Sendable (ConnectionState) -> Void

    private let hub: HubClient
    private let onEvent: EventHandler
    private let onState: StateHandler
    private var loop: Task<Void, Never>?

    public static var lastSeq: Int64 {
        get { Int64(AppGroup.defaults.integer(forKey: AppGroup.Key.lastEventSeq)) }
        set { AppGroup.defaults.set(Int(newValue), forKey: AppGroup.Key.lastEventSeq) }
    }

    public init(hub: HubClient, onEvent: @escaping EventHandler, onState: @escaping StateHandler) {
        self.hub = hub
        self.onEvent = onEvent
        self.onState = onState
    }

    deinit {
        loop?.cancel()
    }

    public func start() {
        guard loop == nil else { return }
        loop = Task { [hub, onEvent, onState] in
            await Self.run(hub: hub, onEvent: onEvent, onState: onState)
        }
    }

    public func stop() {
        loop?.cancel()
        loop = nil
        let onState = self.onState
        Task { @MainActor in onState(.disconnected) }
    }

    /// Restart immediately (e.g. app returned to foreground or network changed).
    public func restart() {
        loop?.cancel()
        loop = nil
        start()
    }

    private static func backoff(attempt: Int) -> TimeInterval {
        let base = min(30.0, pow(2.0, Double(min(attempt, 5))))   // 2, 4, 8, 16, 30 s
        return base * Double.random(in: 0.7...1.0)
    }

    private static func run(hub: HubClient, onEvent: EventHandler, onState: StateHandler) async {
        var attempt = 0
        while !Task.isCancelled {
            await onState(.connecting)
            do {
                let headers = try await hub.authHeaders()
                let stream = hub.events.subscribe(headers: headers)
                var request = Nexus_V1_SubscribeRequest()
                request.sinceSeq = lastSeq
                try stream.send(request)

                for await result in stream.results() {
                    if Task.isCancelled {
                        stream.cancel()
                        break
                    }
                    switch result {
                    case .headers:
                        attempt = 0
                        await onState(.connected)
                    case .message(let response):
                        // Keepalives (heartbeat, no event) only prove the stream is alive.
                        guard response.hasEvent else { continue }
                        let event = response.event
                        if event.seq > lastSeq { lastSeq = event.seq }
                        await onEvent(event)
                    case .complete(let code, _, _):
                        if code == .unauthenticated {
                            await hub.invalidateToken()
                        }
                    }
                }
                // Ends the HTTP stream if the loop exited because the task was cancelled
                // (harmless when the server already completed it).
                stream.cancel()
            } catch {
                // Fall through to backoff (token fetch failure, not configured, ...).
            }
            if Task.isCancelled { break }
            attempt += 1
            let delay = backoff(attempt: attempt)
            await onState(.reconnecting(attempt: attempt, retryIn: delay))
            try? await Task.sleep(for: .seconds(delay))
        }
    }
}
