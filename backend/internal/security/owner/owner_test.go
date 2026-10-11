package owner

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/approval"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

type K = nexusv1.OwnerStatement_Kind

func stmt(t *testing.T, root *ecdsa.PrivateKey, kind K, seq uint64, mut func(*nexusv1.OwnerStatement)) *nexusv1.OwnerStatement {
	t.Helper()
	st := &nexusv1.OwnerStatement{Kind: kind, Sequence: seq, IssuedAt: timestamppb.Now()}
	if mut != nil {
		mut(st)
	}
	if err := Sign(st, root); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestMessageLayout(t *testing.T) {
	st := &nexusv1.OwnerStatement{
		Kind: nexusv1.OwnerStatement_KIND_POLICY, Sequence: 7,
		IssuedAt: &timestamppb.Timestamp{Seconds: 1}, SubjectId: "x", PolicySha256: []byte{1, 2},
	}
	got := hex.EncodeToString(Message(st))
	want := hex.EncodeToString([]byte(Tag)) + "00" + "00000004" + "0000000000000007" + "0000000000000001" +
		"00000000" + "00000001" + "78" + "00000002" + "0102" + "00000000"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestTrustStoreLifecycle(t *testing.T) {
	root, _ := keys.Generate()
	dev, _ := keys.Generate()
	node, _ := keys.Generate()
	path := filepath.Join(t.TempDir(), "trust.json")
	ts := NewTrustStore(&root.PublicKey, path)
	ctx := context.Background()

	devPub := keys.MarshalPublicKey(&dev.PublicKey)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_ENROL_DEVICE, 1, func(s *nexusv1.OwnerStatement) {
		s.SubjectId, s.SubjectPublicKey = "dev_1", devPub[1:] // raw 64-byte encoding accepted
	})))
	must(ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_ENROL_NODE, 2, func(s *nexusv1.OwnerStatement) {
		s.SubjectId, s.SubjectPublicKey = "node_1", keys.MarshalPublicKey(&node.PublicKey)
	})))
	pol := sha256.Sum256([]byte("policy"))
	must(ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_POLICY, 3, func(s *nexusv1.OwnerStatement) { s.PolicySha256 = pol[:] })))

	got, err := ts.ApprovalKey(ctx, "dev_1")
	if err != nil || !got.Equal(&dev.PublicKey) {
		t.Fatalf("ApprovalKey: %v", err)
	}
	if _, ok := ts.Principal("node", "node_1"); !ok {
		t.Fatal("node missing")
	}
	if _, err := ts.ApprovalKey(ctx, "node_1"); !errors.Is(err, approval.ErrUnknownDevice) {
		t.Fatal("node key must not be usable as a device approval key")
	}

	// Rollback / replay of an older or equal sequence.
	if err := ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_UNLOCK, 3, nil)); !errors.Is(err, ErrRollback) {
		t.Fatalf("rollback: %v", err)
	}
	if err := ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_UNLOCK, 1, nil)); !errors.Is(err, ErrRollback) {
		t.Fatalf("rollback: %v", err)
	}

	// Forged by a non-owner key.
	fake, _ := keys.Generate()
	if err := ts.Apply(stmt(t, fake, nexusv1.OwnerStatement_KIND_LOCK, 10, nil)); !errors.Is(err, ErrBadSignature) {
		t.Fatal(err)
	}
	// Tampered after signing.
	st := stmt(t, root, nexusv1.OwnerStatement_KIND_ENROL_DEVICE, 4, func(s *nexusv1.OwnerStatement) {
		s.SubjectId, s.SubjectPublicKey = "dev_2", devPub
	})
	st.SubjectId = "dev_evil"
	if err := ts.Apply(st); !errors.Is(err, ErrBadSignature) {
		t.Fatal(err)
	}

	// Lock, revoke.
	must(ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_LOCK, 4, nil)))
	if !ts.Locked() {
		t.Fatal("not locked")
	}
	must(ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_REVOKE_KEY, 5, func(s *nexusv1.OwnerStatement) { s.SubjectId = "dev_1" })))
	if _, err := ts.ApprovalKey(ctx, "dev_1"); err == nil {
		t.Fatal("revoked device still trusted")
	}
	if !ts.IsRevoked(devPub) {
		t.Fatal("key not marked revoked")
	}
	// A revoked key cannot be re-enrolled; failed apply leaves state unchanged.
	if err := ts.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_ENROL_DEVICE, 6, func(s *nexusv1.OwnerStatement) {
		s.SubjectId, s.SubjectPublicKey = "dev_1b", devPub
	})); !errors.Is(err, ErrRevokedKey) {
		t.Fatal(err)
	}
	if ts.Sequence() != 5 {
		t.Fatalf("sequence advanced on failure: %d", ts.Sequence())
	}

	// Persistence round trip.
	st2, err := os.Stat(path)
	if err != nil || st2.Mode().Perm() != 0o600 {
		t.Fatalf("persist: %v", err)
	}
	re, err := LoadTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if re.Sequence() != 5 || !re.Locked() || !re.IsRevoked(devPub) || hex.EncodeToString(re.PolicySHA256()) != hex.EncodeToString(pol[:]) {
		t.Fatal("reloaded state differs")
	}
	if !re.OwnerRoot().Equal(&root.PublicKey) {
		t.Fatal("root differs")
	}
	if err := re.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_UNLOCK, 5, nil)); !errors.Is(err, ErrRollback) {
		t.Fatal("rollback after reload")
	}

	// End-to-end: approval verified against the trust store.
	must(re.Apply(stmt(t, root, nexusv1.OwnerStatement_KIND_ENROL_DEVICE, 6, func(s *nexusv1.OwnerStatement) {
		s.SubjectId, s.SubjectPublicKey = "dev_3", keys.MarshalPublicKey(&fake.PublicKey)
	})))
	h := sha256.Sum256([]byte("act"))
	d := &nexusv1.SignedApproval{ApprovalId: "apr", Approve: true, Challenge: []byte("c"), ActionSha256: h[:],
		ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), DeviceId: "dev_3"}
	must(approval.Sign(d, fake))
	must(approval.Verify(ctx, d, h[:], re, time.Now()))
}

func TestMalformed(t *testing.T) {
	root, _ := keys.Generate()
	ts := NewTrustStore(&root.PublicKey, "")
	bad := []*nexusv1.OwnerStatement{
		stmt(t, root, nexusv1.OwnerStatement_KIND_ENROL_DEVICE, 1, func(s *nexusv1.OwnerStatement) { s.SubjectPublicKey = []byte{1}; s.SubjectId = "d" }),
		stmt(t, root, nexusv1.OwnerStatement_KIND_POLICY, 1, func(s *nexusv1.OwnerStatement) { s.PolicySha256 = []byte{1} }),
		stmt(t, root, nexusv1.OwnerStatement_KIND_UNSPECIFIED, 1, nil),
		stmt(t, root, nexusv1.OwnerStatement_KIND_REVOKE_KEY, 1, nil),
	}
	for i, st := range bad {
		if err := ts.Apply(st); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if ts.Sequence() != 0 {
		t.Fatal("sequence advanced")
	}
}
