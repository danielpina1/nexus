// Package audit is a hash-chained, append-only audit log with signed
// checkpoints (SECURITY.md Layer 7).
//
// # Hash chain (exact)
//
//	payload_hash = SHA256(payload bytes)
//	entry_hash   = SHA256( ASCII "nexus/v1/audit" || 0x00
//	                       || u64be(seq)            (first entry has seq 1)
//	                       || prev_hash             (32 bytes; 32 zero bytes for seq 1)
//	                       || payload_hash          (32 bytes)
//	                       || i64be(at, Unix nanoseconds) )
//	head         = entry_hash of the last entry (32 zero bytes when empty)
//
// # Checkpoint (nexusv1.AuditCheckpoint)
//
//	signature = ECDSA P-256 / SHA-256 over
//	            M = ASCII "nexus/v1/audit-checkpoint" || 0x00 || u64be(sequence) || head_sha256
//	            with the Hub (or Node) identity key.
//
// Chain verification detects modification, insertion, deletion and
// reordering; truncation of the tail is detected by comparing with a
// previously signed checkpoint (mirrored to Nodes).
package audit

import (
	"bufio"
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

// Tags.
const (
	EntryTag      = "nexus/v1/audit"
	CheckpointTag = "nexus/v1/audit-checkpoint"
)

// Errors.
var (
	ErrBroken     = errors.New("audit: chain broken")
	ErrCheckpoint = errors.New("audit: checkpoint mismatch")
)

var zeroHash = make([]byte, 32)

// Entry is one log record.
type Entry struct {
	Seq         uint64    `json:"seq"`
	PrevHash    []byte    `json:"prev_hash"`
	PayloadHash []byte    `json:"payload_hash"`
	At          time.Time `json:"at"`
	Hash        []byte    `json:"hash"`
	// Payload is stored alongside (may be redacted to nil; the chain still
	// verifies through PayloadHash).
	Payload []byte `json:"payload,omitempty"`
}

// EntryHash computes the chained hash (see package doc).
func EntryHash(seq uint64, prev, payloadHash []byte, at time.Time) []byte {
	m := keys.NewBuilder(EntryTag).U64(seq).Fixed(prev).Fixed(payloadHash).I64(at.UnixNano()).Message()
	h := sha256.Sum256(m)
	return h[:]
}

// VerifyChain checks a full chain starting at seq 1 and returns its head.
func VerifyChain(entries []Entry) ([]byte, error) {
	prev := zeroHash
	for i, e := range entries {
		if e.Seq != uint64(i+1) {
			return nil, fmt.Errorf("%w: entry %d has seq %d", ErrBroken, i+1, e.Seq)
		}
		if !bytes.Equal(e.PrevHash, prev) {
			return nil, fmt.Errorf("%w: seq %d prev_hash mismatch", ErrBroken, e.Seq)
		}
		if e.Payload != nil {
			ph := sha256.Sum256(e.Payload)
			if !bytes.Equal(ph[:], e.PayloadHash) {
				return nil, fmt.Errorf("%w: seq %d payload modified", ErrBroken, e.Seq)
			}
		}
		if len(e.PayloadHash) != 32 {
			return nil, fmt.Errorf("%w: seq %d payload_hash length", ErrBroken, e.Seq)
		}
		h := EntryHash(e.Seq, e.PrevHash, e.PayloadHash, e.At)
		if !bytes.Equal(h, e.Hash) {
			return nil, fmt.Errorf("%w: seq %d hash mismatch", ErrBroken, e.Seq)
		}
		prev = e.Hash
	}
	return prev, nil
}

// CheckpointMessage returns the signed bytes of a checkpoint.
func CheckpointMessage(seq uint64, head []byte) []byte {
	return keys.NewBuilder(CheckpointTag).U64(seq).Fixed(head).Message()
}

// SignCheckpoint signs (seq, head).
func SignCheckpoint(seq uint64, head []byte, signer crypto.Signer) (*nexusv1.AuditCheckpoint, error) {
	sig, err := keys.Sign(signer, CheckpointMessage(seq, head))
	if err != nil {
		return nil, err
	}
	return &nexusv1.AuditCheckpoint{Sequence: seq, HeadSha256: append([]byte(nil), head...), Signature: sig}, nil
}

// VerifyCheckpoint checks a checkpoint signature.
func VerifyCheckpoint(cp *nexusv1.AuditCheckpoint, pub *ecdsa.PublicKey) error {
	if cp == nil || len(cp.HeadSha256) != 32 {
		return ErrCheckpoint
	}
	if !keys.Verify(pub, CheckpointMessage(cp.Sequence, cp.HeadSha256), cp.Signature) {
		return fmt.Errorf("%w: bad signature", ErrCheckpoint)
	}
	return nil
}

// VerifyAgainstCheckpoint verifies the chain and that it contains the
// checkpointed head at the checkpointed sequence (detects truncation and
// rewriting of history before the checkpoint).
func VerifyAgainstCheckpoint(entries []Entry, cp *nexusv1.AuditCheckpoint, pub *ecdsa.PublicKey) error {
	if err := VerifyCheckpoint(cp, pub); err != nil {
		return err
	}
	if _, err := VerifyChain(entries); err != nil {
		return err
	}
	if cp.Sequence == 0 {
		if !bytes.Equal(cp.HeadSha256, zeroHash) {
			return ErrCheckpoint
		}
		return nil
	}
	if uint64(len(entries)) < cp.Sequence {
		return fmt.Errorf("%w: log truncated (have %d, checkpoint at %d)", ErrCheckpoint, len(entries), cp.Sequence)
	}
	if subtle.ConstantTimeCompare(entries[cp.Sequence-1].Hash, cp.HeadSha256) != 1 {
		return fmt.Errorf("%w: history rewritten before seq %d", ErrCheckpoint, cp.Sequence)
	}
	return nil
}

// Log is an append-only log persisted as JSON lines (0600). A Log opened
// with path "" is memory-only.
type Log struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	entries []Entry
	now     func() time.Time
}

// Open loads and verifies the log at path (creating it if absent).
func Open(path string) (*Log, error) {
	l := &Log{path: path}
	if path == "" {
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(path); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) == 0 {
				continue
			}
			var e Entry
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				return nil, fmt.Errorf("%w: %s: %v", ErrBroken, path, err)
			}
			l.entries = append(l.entries, e)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		if _, err := VerifyChain(l.entries); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l.f = f
	return l, nil
}

// SetClock overrides the clock (tests).
func (l *Log) SetClock(now func() time.Time) { l.now = now }

// Close closes the backing file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Append adds an entry with payload and returns it.
func (l *Log) Append(payload []byte) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := time.Now()
	if l.now != nil {
		at = l.now()
	}
	at = at.UTC().Round(0)
	prev := zeroHash
	if n := len(l.entries); n > 0 {
		prev = l.entries[n-1].Hash
	}
	ph := sha256.Sum256(payload)
	seq := uint64(len(l.entries) + 1)
	e := Entry{
		Seq: seq, PrevHash: prev, PayloadHash: ph[:], At: at,
		Hash: EntryHash(seq, prev, ph[:], at), Payload: append([]byte{}, payload...),
	}
	if l.f != nil {
		b, err := json.Marshal(e)
		if err != nil {
			return Entry{}, err
		}
		if _, err := l.f.Write(append(b, '\n')); err != nil {
			return Entry{}, err
		}
		if err := l.f.Sync(); err != nil {
			return Entry{}, err
		}
	}
	l.entries = append(l.entries, e)
	return e, nil
}

// Record appends a JSON payload {"kind": kind, "data": data}.
func (l *Log) Record(kind string, data any) (Entry, error) {
	b, err := json.Marshal(struct {
		Kind string `json:"kind"`
		Data any    `json:"data,omitempty"`
	}{kind, data})
	if err != nil {
		return Entry{}, err
	}
	return l.Append(b)
}

// Head returns the last sequence and head hash.
func (l *Log) Head() (uint64, []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.entries); n > 0 {
		return uint64(n), append([]byte(nil), l.entries[n-1].Hash...)
	}
	return 0, append([]byte(nil), zeroHash...)
}

// Entries returns entries with seq > since.
func (l *Log) Entries(since uint64) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if since >= uint64(len(l.entries)) {
		return nil
	}
	return append([]Entry(nil), l.entries[since:]...)
}

// Verify re-verifies the in-memory chain.
func (l *Log) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := VerifyChain(l.entries)
	return err
}

// Checkpoint signs the current head.
func (l *Log) Checkpoint(signer crypto.Signer) (*nexusv1.AuditCheckpoint, error) {
	seq, head := l.Head()
	return SignCheckpoint(seq, head, signer)
}
