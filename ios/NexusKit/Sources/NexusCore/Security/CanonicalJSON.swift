import CryptoKit
import Foundation
import SwiftProtobuf

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  ACTION HASH — MUST MATCH backend/internal/security/approval/canonical.go                ║
// ║  (docs/WIRE_FORMATS.md "Action hash")                                                    ║
// ║                                                                                          ║
// ║  action_sha256 = SHA256( RFC 8785 JCS canonical JSON of Approval.payload )               ║
// ║   • no whitespace; object keys sorted by UTF-16 code units                               ║
// ║   • strings: escape only `"` `\` and U+0000..U+001F (\b \t \n \f \r, else \u00xx lower)  ║
// ║   • numbers: IEEE-754 doubles printed like ECMAScript Number.prototype.toString          ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public enum CanonicalJSON {
    public enum CanonicalError: Error {
        case nonFiniteNumber
    }

    /// SHA256(JCS(payload)) — what the phone recomputes and compares with `payload_hash`.
    public static func actionHash(of payload: Google_Protobuf_Struct) throws -> Data {
        Data(SHA256.hash(data: Data(try canonicalize(payload).utf8)))
    }

    public static func canonicalize(_ object: Google_Protobuf_Struct) throws -> String {
        var out = ""
        try writeObject(object.fields, to: &out)
        return out
    }

    private static func writeObject(_ fields: [String: Google_Protobuf_Value], to out: inout String) throws {
        let keys = fields.keys.sorted { Array($0.utf16).lexicographicallyPrecedes(Array($1.utf16)) }
        out.append("{")
        for (index, key) in keys.enumerated() {
            if index > 0 { out.append(",") }
            writeString(key, to: &out)
            out.append(":")
            try writeValue(fields[key] ?? Google_Protobuf_Value(), to: &out)
        }
        out.append("}")
    }

    private static func writeValue(_ value: Google_Protobuf_Value, to out: inout String) throws {
        switch value.kind {
        case .nullValue, .none:
            out.append("null")
        case .boolValue(let b):
            out.append(b ? "true" : "false")
        case .numberValue(let n):
            out.append(try formatNumber(n))
        case .stringValue(let s):
            writeString(s, to: &out)
        case .listValue(let list):
            out.append("[")
            for (index, item) in list.values.enumerated() {
                if index > 0 { out.append(",") }
                try writeValue(item, to: &out)
            }
            out.append("]")
        case .structValue(let object):
            try writeObject(object.fields, to: &out)
        }
    }

    static func writeString(_ string: String, to out: inout String) {
        out.append("\"")
        for scalar in string.unicodeScalars {
            switch scalar {
            case "\"": out.append("\\\"")
            case "\\": out.append("\\\\")
            case "\u{08}": out.append("\\b")
            case "\u{0C}": out.append("\\f")
            case "\n": out.append("\\n")
            case "\r": out.append("\\r")
            case "\t": out.append("\\t")
            default:
                if scalar.value < 0x20 {
                    out.append(String(format: "\\u%04x", scalar.value))
                } else {
                    out.unicodeScalars.append(scalar)
                }
            }
        }
        out.append("\"")
    }

    /// ECMAScript Number::toString for finite doubles (mirrors Go `formatNumber`).
    /// Swift's `description` yields the shortest round-trip digits, which we re-layout.
    static func formatNumber(_ value: Double) throws -> String {
        guard value.isFinite else { throw CanonicalError.nonFiniteNumber }
        if value == 0 { return "0" }
        let sign = value < 0 ? "-" : ""
        let text = "\(abs(value))"   // e.g. "84.3", "100.0", "1e-07", "1.2345e+21"

        var mantissa = Substring(text)
        var exponent = 0
        if let e = text.firstIndex(where: { $0 == "e" || $0 == "E" }) {
            mantissa = text[..<e]
            exponent = Int(text[text.index(after: e)...]) ?? 0
        }
        let parts = mantissa.split(separator: ".", omittingEmptySubsequences: false)
        let integerPart = String(parts[0])
        let fractionPart = parts.count > 1 ? String(parts[1]) : ""

        // value = digits × 10^scale
        var digits = integerPart + fractionPart
        var scale = exponent - fractionPart.count
        while digits.count > 1 && digits.hasPrefix("0") { digits.removeFirst() }
        while digits.count > 1 && digits.hasSuffix("0") {
            digits.removeLast()
            scale += 1
        }
        let k = digits.count
        let n = scale + k   // value = 0.digits × 10^n

        let result: String
        if k <= n && n <= 21 {
            result = digits + String(repeating: "0", count: n - k)
        } else if 0 < n && n <= 21 {
            let index = digits.index(digits.startIndex, offsetBy: n)
            result = String(digits[..<index]) + "." + String(digits[index...])
        } else if -6 < n && n <= 0 {
            result = "0." + String(repeating: "0", count: -n) + digits
        } else {
            let e = n - 1
            let expText = (e < 0 ? "-" : "+") + String(abs(e))
            if k == 1 {
                result = digits + "e" + expText
            } else {
                result = String(digits.prefix(1)) + "." + String(digits.dropFirst()) + "e" + expText
            }
        }
        return sign + result
    }
}
