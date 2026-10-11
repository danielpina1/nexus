# Wire Formats (cryptographic)

Byte-exact formats every client (iOS, web, CLI, nodes) must reproduce. Source of truth:
`backend/internal/security`. Change both sides together.

## Conventions
- P-256 public keys: 65-byte X9.63 uncompressed (`x963Representation`). Parsers also accept raw 64,
  compressed 33, SPKI DER.
- X25519 public keys: 32 raw bytes.
- "Signature over M": ECDSA P-256 / SHA-256 over M (CryptoKit `signature(for: M)`). Go emits DER,
  accepts DER or raw 64-byte.
- Key id: lowercase hex of the first 16 bytes of SHA256(public key bytes).
- `lp(x)` = `u32be(len(x)) || x`. `TAG||0x00` = ASCII tag followed by one zero byte.

## Action hash
`SHA256(RFC 8785 JCS canonical JSON of Approval.payload)`. Integers > 2^53 must be strings.

## SignedApproval (device approval key, Face ID)
```
"nexus/v1/approval" 0x00 || lp(approval_id) || u8(approve ? 1 : 0) || lp(challenge)
  || action_sha256 (32 bytes) || i64be(expires_at.seconds)
```
`expires_at` must be in the future and ≤ 24 h ahead.

## Authenticate (device auth key)
```
"nexus/v1/authenticate" 0x00 || lp(device_id) || lp(challenge)
```
Challenge: 32 bytes, single use, 2 min TTL.

## Signed requests (device auth key)
Headers: `X-Nexus-Device`, `X-Nexus-Timestamp` (unix s), `X-Nexus-Nonce` (≥16 bytes, base64url
no padding), `X-Nexus-Counter` (decimal ≥1, optional), `X-Nexus-Signature` (std base64).
Signed string (`\n`-joined):
```
METHOD\nTARGET\nhex(sha256(body))\nTIMESTAMP\nNONCE\nDEVICE_ID\nCOUNTER
```
`TARGET` = path + `?query`; `COUNTER` = "" if header absent.

## OwnerStatement (owner root key)
```
"nexus/v1/owner-statement" 0x00 || u32be(kind) || u64be(sequence) || i64be(issued_at.seconds)
  || lp(subject_public_key) || lp(subject_id) || lp(policy_sha256) || lp(payload)
```

## Sealed envelope
HPKE base mode, DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / ChaCha20-Poly1305;
`info = "nexus/v1/envelope"`, `aad = header bytes` (verbatim);
signature over `header || enc || ciphertext`;
`suite = "hpke-x25519-sha256-chacha20poly1305+p256-ecdsa"`.
Receiver checks purpose, recipient, ±60 s timestamp, nonce ≥16 bytes unused, counter strictly
increasing per sender.

## Pairing
- QR: `nexus://pair?v=1&k=device|node&hub=<url>(repeatable)&ca=<sha256 hex of CA DER>&s=<token>&hk=<base64url hub key>&exp=<unix>`
  (base64url JSON with the same fields also accepted).
- Token: base64url (no padding) of a 32-byte secret; 5 min; single use (consumed even on failure).
- `key = HKDF-SHA256(ikm = secret, salt = empty, info = "nexus/v1/pairing", L = 32)`
- `proof = HMAC-SHA256(key, auth_pub || approval_pub || encryption_pub)` (bytes as sent).
- App Attest `clientDataHash = SHA256(auth_pub || approval_pub || encryption_pub)`.

## Safety code (6 emojis)
Inputs: hub identity key + device's 3 public keys, sorted bytewise.
`d = SHA256("nexus/v1/safety-code" 0x00 || lp(k1) || lp(k2) || …)`;
emoji i (0..5) = `Emojis[d[i] >> 2]` from the 64-entry table in
`backend/internal/security/pairing/pairing.go`; joined with single spaces.

## Audit log
Entry hash = `SHA256("nexus/v1/audit" 0x00 || u64be(seq) || prev(32) || payload_hash(32) || i64be(at unix nanos))`,
first prev = 32 zero bytes. Checkpoint signature over
`"nexus/v1/audit-checkpoint" 0x00 || u64be(seq) || head`.
