// This file is derived from connect-swift (https://github.com/connectrpc/connect-swift):
//   Libraries/Connect/Public/Implementation/Clients/URLSessionHTTPClient.swift
//   Libraries/Connect/Internal/Streaming/URLSessionStream.swift
//   Libraries/Connect/Internal/Utilities/Lock.swift, Locked.swift
// (main as of 2026-08-04, API-compatible with connect-swift 1.0.0 … 1.2.3).
//
// Copyright 2022-2025 The Connect Authors
// Modifications Copyright 2026 The Nexus Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// The Apache-2.0 licence text: ios/NexusKit/ThirdPartyLicenses/connect-swift-LICENSE.txt
//
// Modifications:
// - renamed to PinnedURLSessionHTTPClient / PinnedURLSessionStream / ConnectLock(ed);
// - the URLSession delegate also answers authentication challenges (session and task level)
//   with HubTrust, pinning the Hub's private CA (`caFingerprint` from the pairing QR);
// - a task that failed because its server trust was rejected by the pin is reported as
//   `.unavailable` with a clear message instead of `.canceled`;
// - internal helpers of the Connect module (Lock, Locked, URLSession code mapping, header
//   formatting) are copied here because they are not public.
//
// Why a copy: connect-swift's URLSessionHTTPClient routes delegate calls through a private
// wrapper that does not forward authentication challenges, so subclassing cannot add trust
// handling.

import Connect
import Foundation
import os.log

/// `HTTPClientInterface` backed by `URLSession` that behaves like connect-swift's
/// `URLSessionHTTPClient` (unary + streaming, metrics, cancellation, error mapping) and pins the
/// Hub's private CA with `HubTrust`.
///
/// Thread-safe through an internal lock.
public final class PinnedURLSessionHTTPClient: NSObject, HTTPClientInterface, @unchecked Sendable {
    /// Lock used for safely accessing stream storage.
    private let lock = ConnectLock()
    /// Closures stored for notifying when metrics are available.
    private var metricsClosures = [Int: @Sendable (HTTPMetrics) -> Void]()
    /// Session used for performing requests.
    private let session: URLSession
    /// List of active streams.
    private var streams = [Int: PinnedURLSessionStream]()
    /// Pinned CA fingerprint and the last pin failure per host.
    private let trust: HubTrustDelegate

    /// - Parameter caFingerprint: lowercase hex SHA-256 of the Hub CA DER (pairing QR `ca=`).
    ///   nil keeps the system's default trust handling (not paired yet).
    public init(caFingerprint: String?, configuration: URLSessionConfiguration = .default) {
        let delegateQueue = OperationQueue()
        delegateQueue.name = "nexus.pinned-urlsession-http-client"
        delegateQueue.maxConcurrentOperationCount = 1
        delegateQueue.qualityOfService = .userInitiated
        let trust = HubTrustDelegate(caFingerprint: caFingerprint)
        let delegate = PinnedURLSessionDelegateWrapper(trust: trust)
        self.trust = trust
        self.session = URLSession(
            configuration: configuration,
            delegate: delegate,
            delegateQueue: delegateQueue
        )
        super.init()
        delegate.client = self
    }

    deinit {
        self.session.finishTasksAndInvalidate()
    }

    /// The pinned fingerprint (normalized), nil when not pinning.
    public var caFingerprint: String? { trust.caFingerprint }

    @discardableResult
    public func unary(
        request: HTTPRequest<Data?>,
        onMetrics: @escaping @Sendable (HTTPMetrics) -> Void,
        onResponse: @escaping @Sendable (HTTPResponse) -> Void
    ) -> Cancelable {
        assert(!request.isGRPC, "PinnedURLSessionHTTPClient does not support gRPC")
        let urlRequest = URLRequest(httpRequest: request)
        let trust = self.trust
        let requestURL = request.url
        let task = self.session.dataTask(with: urlRequest) { data, urlResponse, error in
            if let httpURLResponse = urlResponse as? HTTPURLResponse {
                onResponse(HTTPResponse(
                    code: Code.fromPinnedURLSessionCode(httpURLResponse.statusCode),
                    headers: httpURLResponse.formattedLowercasedConnectHeaders(),
                    message: data,
                    trailers: [:], // URLSession does not support trailers
                    error: error,
                    tracingInfo: .init(httpStatus: httpURLResponse.statusCode)
                ))
            } else if let error = error {
                if let trustError = trust.trustError(for: error, url: requestURL) {
                    onResponse(HTTPResponse(
                        code: .unavailable,
                        headers: [:],
                        message: data,
                        trailers: [:],
                        error: ConnectError(
                            code: .unavailable,
                            message: trustError.errorDescription,
                            exception: trustError
                        ),
                        tracingInfo: nil
                    ))
                    return
                }
                let code = Code.fromPinnedURLSessionCode((error as NSError).code)
                onResponse(HTTPResponse(
                    code: code,
                    headers: [:],
                    message: data,
                    trailers: [:],
                    error: ConnectError(
                        code: code,
                        message: error.localizedDescription,
                        exception: error
                    ),
                    tracingInfo: nil
                ))
            } else {
                onResponse(HTTPResponse(
                    code: .unknown,
                    headers: [:],
                    message: data,
                    trailers: [:],
                    error: ConnectError(
                        code: .unknown,
                        message: "unexpected response type \(type(of: urlResponse))",
                        exception: error
                    ),
                    tracingInfo: nil
                ))
            }
        }
        self.lock.perform { self.metricsClosures[task.taskIdentifier] = onMetrics }
        task.resume()
        return Cancelable { task.cancel() }
    }

    public func stream(
        request: HTTPRequest<Data?>, responseCallbacks: ResponseCallbacks
    ) -> RequestCallbacks<Data> {
        assert(!request.isGRPC, "PinnedURLSessionHTTPClient does not support gRPC")
        let trust = self.trust
        let requestURL = request.url
        let urlSessionStream = PinnedURLSessionStream(
            request: URLRequest(httpRequest: request),
            session: self.session,
            responseCallbacks: responseCallbacks,
            trustError: { error in trust.trustError(for: error, url: requestURL) }
        )
        self.lock.perform {
            self.streams[urlSessionStream.taskID] = urlSessionStream
            self.metricsClosures[urlSessionStream.taskID] = responseCallbacks.receiveResponseMetrics
        }
        return RequestCallbacks(
            cancel: { [weak urlSessionStream] in urlSessionStream?.cancel() },
            sendData: { [weak urlSessionStream] data in
                do {
                    try urlSessionStream?.sendData(data)
                } catch let error {
                    os_log(
                        .error,
                        "Failed to write data to stream - closing connection: %@",
                        error.localizedDescription
                    )
                    urlSessionStream?.close()
                }
            },
            sendClose: { [weak urlSessionStream] in urlSessionStream?.close() }
        )
    }

    // MARK: - URLSession delegate functions

    func urlSession(
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
        completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
    ) {
        defer { completionHandler(.allow) }

        guard let httpURLResponse = response as? HTTPURLResponse else {
            return
        }

        let stream = self.lock.perform { self.streams[dataTask.taskIdentifier] }
        stream?.handleResponse(httpURLResponse)
    }

    func urlSession(
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data
    ) {
        let stream = self.lock.perform { self.streams[dataTask.taskIdentifier] }
        stream?.handleResponseData(data)
    }

    func urlSession(
        _ session: URLSession, task: URLSessionTask,
        needNewBodyStream completionHandler: @escaping (InputStream?) -> Void
    ) {
        // Look up the stream under the lock, but vend/cancel the body stream outside it.
        // `requestBodyStream` may call `task.cancel()`, and cancelation can deliver
        // `didCompleteWithError` on this same serial delegate queue; holding `self.lock`
        // across that work would risk lock-order deadlocks with stream removal.
        let stream = self.lock.perform { self.streams[task.taskIdentifier] }
        completionHandler(stream?.requestBodyStream)
    }

    func urlSession(
        _ session: URLSession, task: URLSessionTask, didCompleteWithError error: Swift.Error?
    ) {
        let stream = self.lock.perform { self.streams.removeValue(forKey: task.taskIdentifier) }
        stream?.handleCompletion(error: error)
    }

    func urlSession(
        _ session: URLSession, task: URLSessionTask,
        didFinishCollecting metrics: URLSessionTaskMetrics
    ) {
        if let metricsClosure = self.lock.perform(
            action: { self.metricsClosures.removeValue(forKey: task.taskIdentifier) }
        ) {
            metricsClosure(HTTPMetrics(taskMetrics: metrics))
        }
    }
}

// MARK: - URLSession delegate wrapper

/// Avoids a retain cycle between `PinnedURLSessionHTTPClient` and its `URLSession` (which retains
/// its delegate strongly): the wrapper holds the client weakly and forwards delegate calls.
///
/// Authentication challenges are answered by the `HubTrustDelegate` held strongly here (it does
/// not reference the session or the client), so the completion handler is always called, even
/// while the client is being deallocated.
private final class PinnedURLSessionDelegateWrapper: NSObject, @unchecked Sendable {
    weak var client: PinnedURLSessionHTTPClient?
    let trust: HubTrustDelegate

    init(trust: HubTrustDelegate) {
        self.trust = trust
        super.init()
    }
}

extension PinnedURLSessionDelegateWrapper: URLSessionDataDelegate {
    func urlSession(
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
        completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
    ) {
        self.client?.urlSession(
            session, dataTask: dataTask, didReceive: response, completionHandler: completionHandler
        )
    }

    func urlSession(
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data
    ) {
        self.client?.urlSession(session, dataTask: dataTask, didReceive: data)
    }

    func urlSession(
        _ session: URLSession, task: URLSessionTask,
        needNewBodyStream completionHandler: @escaping (InputStream?) -> Void
    ) {
        self.client?.urlSession(session, task: task, needNewBodyStream: completionHandler)
    }
}

extension PinnedURLSessionDelegateWrapper: URLSessionTaskDelegate {
    func urlSession(
        _ session: URLSession, task: URLSessionTask, didCompleteWithError error: Swift.Error?
    ) {
        self.client?.urlSession(session, task: task, didCompleteWithError: error)
    }

    func urlSession(
        _ session: URLSession, task: URLSessionTask,
        didFinishCollecting metrics: URLSessionTaskMetrics
    ) {
        self.client?.urlSession(session, task: task, didFinishCollecting: metrics)
    }

    // Task-level authentication challenges.
    func urlSession(
        _ session: URLSession, task: URLSessionTask,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        self.trust.urlSession(
            session, task: task, didReceive: challenge, completionHandler: completionHandler
        )
    }

    // URLSessionDelegate (inherited): session-level authentication challenges; server trust
    // is delivered here.
    func urlSession(
        _ session: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        self.trust.urlSession(session, didReceive: challenge, completionHandler: completionHandler)
    }
}

// MARK: - Stream

/// Stream implementation that wraps a `URLSession` upload task with a bound stream body
/// (copy of connect-swift's internal `URLSessionStream`).
///
/// Note: This class is `@unchecked Sendable` because the `Foundation.{Input|Output}Stream`
/// types do not conform to `Sendable`.
final class PinnedURLSessionStream: NSObject, @unchecked Sendable {
    private let closedByServer = ConnectLocked(false)
    private let readStream: Foundation.InputStream
    private let requestBodyResendRequested = ConnectLocked(false)
    private let requestBodyStreamVended = ConnectLocked(false)
    private let responseCallbacks: ResponseCallbacks
    private let task: URLSessionUploadTask
    private let writeStream: Foundation.OutputStream
    private let trustError: @Sendable (Swift.Error) -> HubTrustError?

    enum Error: Swift.Error {
        case unableToFindBaseAddress
        case unableToWriteData
    }

    /// The bound input stream to provide to `URLSession` as the request body.
    ///
    /// `URLSession` asks for a body stream via `urlSession(_:task:needNewBodyStream:)` both to
    /// obtain the initial stream and to resend the request after a recoverable error. A streamed
    /// request body cannot be replayed, and the bound input stream may only be opened once
    /// (returning it again makes CFNetwork abort the process). So the stream is vended only
    /// once; on a resend request the task is canceled and `nil` is returned (cancelation is what
    /// makes CFNetwork deliver `didCompleteWithError` instead of parking the task).
    var requestBodyStream: Foundation.InputStream? {
        return self.requestBodyStreamVended.perform { vended in
            guard !vended else {
                self.requestBodyResendRequested.value = true
                self.task.cancel()
                return nil
            }
            vended = true
            return self.readStream
        }
    }

    var taskID: Int {
        return self.task.taskIdentifier
    }

    init(
        request: URLRequest,
        session: URLSession,
        responseCallbacks: ResponseCallbacks,
        trustError: @escaping @Sendable (Swift.Error) -> HubTrustError?
    ) {
        var readStream: Foundation.InputStream!
        var writeStream: Foundation.OutputStream!
        Foundation.Stream.getBoundStreams(
            withBufferSize: 2 * (1_024 * 1_024),
            inputStream: &readStream,
            outputStream: &writeStream
        )

        self.responseCallbacks = responseCallbacks
        self.readStream = readStream
        self.writeStream = writeStream
        self.trustError = trustError
        self.task = session.uploadTask(withStreamedRequest: request)
        super.init()

        writeStream.schedule(in: .current, forMode: .default)
        writeStream.open()
        self.task.resume()
    }

    // MARK: - Outbound

    func sendData(_ data: Data) throws {
        var remaining = Data(data)
        while !remaining.isEmpty {
            let bytesWritten = try remaining.withUnsafeBytes { pointer -> Int in
                guard let baseAddress = pointer.baseAddress else {
                    throw Error.unableToFindBaseAddress
                }

                return self.writeStream.write(
                    baseAddress.assumingMemoryBound(to: UInt8.self),
                    maxLength: remaining.count
                )
            }

            if bytesWritten >= 0 {
                remaining = remaining.dropFirst(bytesWritten)
            } else {
                throw Error.unableToWriteData
            }
        }
    }

    func cancel() {
        self.task.cancel()
    }

    func close() {
        self.writeStream.close()
    }

    // MARK: - Inbound

    func handleResponse(_ response: HTTPURLResponse) {
        let code = Code.fromPinnedURLSessionCode(response.statusCode)
        self.responseCallbacks.receiveResponseHeaders(response.formattedLowercasedConnectHeaders())
        if code != .ok {
            self.closedByServer.value = true
            self.responseCallbacks.receiveClose(code, [:], nil)
        }
    }

    func handleResponseData(_ data: Data) {
        if !self.closedByServer.value {
            self.responseCallbacks.receiveResponseData(data)
        }
    }

    func handleCompletion(error: Swift.Error?) {
        if self.closedByServer.value {
            return
        }

        self.closedByServer.value = true
        if let error = error {
            var code = Code.fromPinnedURLSessionCode((error as NSError).code)
            var message = error.localizedDescription
            var exception: Swift.Error?
            if let trustError = self.trustError(error) {
                // The server trust was rejected by the CA pin (HubTrust).
                code = .unavailable
                message = trustError.errorDescription ?? HubTrust.mismatchMessage
                exception = trustError
            } else if code == .canceled && self.requestBodyResendRequested.value {
                // The cancelation came from `requestBodyStream` refusing a resend, not from the
                // caller. Surface it as a retriable connection failure instead.
                code = .unavailable
                message = "connection was reset and the streamed request body cannot be replayed"
            }
            self.responseCallbacks.receiveClose(
                code,
                [:],
                ConnectError(code: code, message: message, exception: exception)
            )
        } else {
            self.responseCallbacks.receiveClose(.ok, [:], nil)
        }
    }
}

// MARK: - Locks (copies of connect-swift's internal Lock / Locked)

/// Wraps `os_unfair_lock`.
final class ConnectLock: @unchecked Sendable {
    private let underlyingLock: UnsafeMutablePointer<os_unfair_lock>

    init() {
        self.underlyingLock = .allocate(capacity: 1)
        self.underlyingLock.initialize(to: os_unfair_lock())
    }

    deinit {
        self.underlyingLock.deinitialize(count: 1)
        self.underlyingLock.deallocate()
    }

    /// Performs `action` while holding the lock and returns its result.
    func perform<T>(action: @escaping () -> T) -> T {
        os_unfair_lock_lock(self.underlyingLock)
        defer { os_unfair_lock_unlock(self.underlyingLock) }
        return action()
    }
}

/// A value guarded by a `ConnectLock`.
final class ConnectLocked<Wrapped>: @unchecked Sendable {
    private let lock = ConnectLock()
    private var wrappedValue: Wrapped

    var value: Wrapped {
        get { self.lock.perform { self.wrappedValue } }
        set { self.lock.perform { self.wrappedValue = newValue } }
    }

    @discardableResult
    func perform<Result>(action: @escaping (inout Wrapped) -> Result) -> Result {
        return self.lock.perform {
            action(&self.wrappedValue)
        }
    }

    init(_ value: Wrapped) {
        self.wrappedValue = value
    }
}

// MARK: - Extensions (copies of connect-swift internals, renamed to avoid clashes)

extension HTTPURLResponse {
    func formattedLowercasedConnectHeaders() -> Headers {
        return self.allHeaderFields.reduce(into: Headers()) { headers, current in
            guard let headerName = (current.key as? String)?.lowercased() else {
                return
            }

            let headerValues = current.value as? String ?? String(describing: current.value)
            for value in headerValues.components(separatedBy: ",") {
                headers[headerName, default: []].append(value.trimmingCharacters(in: .whitespaces))
            }
        }
    }
}

extension Code {
    static func fromPinnedURLSessionCode(_ code: Int) -> Self {
        // https://developer.apple.com/documentation/cfnetwork/cfnetworkerrors?language=swift
        switch code {
        case URLError.unknown.rawValue:
            return .unknown
        case URLError.cancelled.rawValue:
            return .canceled
        case URLError.badURL.rawValue:
            return .invalidArgument
        case URLError.timedOut.rawValue, NSURLErrorTimedOut:
            return .deadlineExceeded
        case URLError.unsupportedURL.rawValue:
            return .unimplemented
        case NSURLErrorCannotConnectToHost,
            NSURLErrorCannotFindHost,
            NSURLErrorDataNotAllowed,
            NSURLErrorInternationalRoamingOff,
            NSURLErrorNetworkConnectionLost,
            NSURLErrorNotConnectedToInternet,
            NSURLErrorSecureConnectionFailed:
            return .unavailable
        case ...100: // URLSession can return errors in this range
            return .unknown
        default:
            return Self.fromHTTPStatus(code)
        }
    }
}

private extension HTTPRequest {
    var isGRPC: Bool {
        return self.headers[HeaderConstants.contentType]?.first.map { contentType in
            return contentType.hasPrefix("application/grpc")
                && !contentType.hasPrefix("application/grpc-web")
        } ?? false
    }
}

private extension URLRequest {
    init(httpRequest: HTTPRequest<Data?>) {
        self.init(url: httpRequest.url)
        self.httpMethod = httpRequest.method.rawValue
        self.httpBody = httpRequest.message
        for (headerName, headerValues) in httpRequest.headers {
            self.setValue(headerValues.joined(separator: ","), forHTTPHeaderField: headerName)
        }
    }
}
