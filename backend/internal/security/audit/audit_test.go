package audit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

func fill(t *testing.T, l *Log, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := l.Record("test", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChainAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "audit.log")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, l, 5)
	seq, head := l.Head()
	if seq != 5 || len(head) != 32 {
		t.Fatal("head")
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	l.Close()
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	l2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seq2, head2 := l2.Head()
	if seq2 != 5 || !bytes.Equal(head, head2) {
		t.Fatal("reload changed head")
	}
	fill(t, l2, 1)
	if s, _ := l2.Head(); s != 6 {
		t.Fatal("append after reload")
	}
	if got := l2.Entries(4); len(got) != 2 || got[0].Seq != 5 {
		t.Fatal("Entries(since)")
	}
	l2.Close()

	// Tamper with the file: reopen must fail.
	b, _ := os.ReadFile(path)
	b = bytes.Replace(b, []byte(`"seq":3`), []byte(`"seq":9`), 1)
	os.WriteFile(path, b, 0o600)
	if _, err := Open(path); !errors.Is(err, ErrBroken) {
		t.Fatalf("tampered file opened: %v", err)
	}
}

func TestTamperDetection(t *testing.T) {
	mk := func() []Entry {
		l, _ := Open("")
		fill(t, l, 4)
		return l.Entries(0)
	}
	cases := map[string]func([]Entry) []Entry{
		"payload":     func(e []Entry) []Entry { e[1].Payload = []byte(`{"kind":"evil"}`); return e },
		"payloadHash": func(e []Entry) []Entry { e[1].Payload = nil; e[1].PayloadHash[0] ^= 1; return e },
		"time":        func(e []Entry) []Entry { e[2].At = e[2].At.Add(time.Second); return e },
		"delete":      func(e []Entry) []Entry { return append(e[:1], e[2:]...) },
		"reorder":     func(e []Entry) []Entry { e[1], e[2] = e[2], e[1]; return e },
		"prev":        func(e []Entry) []Entry { e[3].PrevHash = e[1].Hash; return e },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyChain(mut(mk())); !errors.Is(err, ErrBroken) {
				t.Fatalf("got %v", err)
			}
		})
	}
	// Redacted payload still verifies.
	e := mk()
	e[1].Payload = nil
	if _, err := VerifyChain(e); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpoint(t *testing.T) {
	k, _ := keys.Generate()
	l, _ := Open("")
	fill(t, l, 3)
	cp, err := l.Checkpoint(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpoint(cp, &k.PublicKey); err != nil {
		t.Fatal(err)
	}
	fill(t, l, 2)
	all := l.Entries(0)
	if err := VerifyAgainstCheckpoint(all, cp, &k.PublicKey); err != nil {
		t.Fatal(err)
	}
	// Truncation below the checkpoint.
	if err := VerifyAgainstCheckpoint(all[:2], cp, &k.PublicKey); !errors.Is(err, ErrCheckpoint) {
		t.Fatal(err)
	}
	// History rewritten consistently (recomputed chain) is still detected.
	l2, _ := Open("")
	fill(t, l2, 1)
	l2.Record("rewritten", nil)
	fill(t, l2, 3)
	if err := VerifyAgainstCheckpoint(l2.Entries(0), cp, &k.PublicKey); !errors.Is(err, ErrCheckpoint) {
		t.Fatal(err)
	}
	// Forged checkpoint.
	other, _ := keys.Generate()
	if err := VerifyCheckpoint(cp, &other.PublicKey); !errors.Is(err, ErrCheckpoint) {
		t.Fatal(err)
	}
	cp.Sequence++
	if err := VerifyCheckpoint(cp, &k.PublicKey); !errors.Is(err, ErrCheckpoint) {
		t.Fatal(err)
	}
}
