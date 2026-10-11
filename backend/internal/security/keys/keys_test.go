package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePublicKeyEncodings(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	unc := MarshalPublicKey(&k.PublicKey)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	cases := map[string][]byte{
		"uncompressed": unc,
		"raw":          unc[1:],
		"compressed":   elliptic.MarshalCompressed(elliptic.P256(), k.X, k.Y),
		"der":          der,
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			pk, err := ParsePublicKey(b)
			if err != nil {
				t.Fatal(err)
			}
			if !pk.Equal(&k.PublicKey) {
				t.Fatal("mismatch")
			}
		})
	}
	bad := [][]byte{nil, {4, 1, 2}, append([]byte{4}, make([]byte, 64)...)}
	for _, b := range bad {
		if _, err := ParsePublicKey(b); err == nil {
			t.Fatalf("accepted %x", b)
		}
	}
}

func TestSignVerifyDERAndRaw(t *testing.T) {
	k, _ := Generate()
	msg := []byte("hello")
	sig, err := Sign(k, msg)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(&k.PublicKey, msg, sig) {
		t.Fatal("DER verify failed")
	}
	d := sha256.Sum256(msg)
	r, s, _ := ecdsa.Sign(rand.Reader, k, d[:])
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	s.FillBytes(raw[32:])
	if !Verify(&k.PublicKey, msg, raw) {
		t.Fatal("raw verify failed")
	}
	if Verify(&k.PublicKey, []byte("hellO"), sig) {
		t.Fatal("tampered message verified")
	}
	other, _ := Generate()
	if Verify(&other.PublicKey, msg, sig) {
		t.Fatal("wrong key verified")
	}
	if Verify(&k.PublicKey, msg, nil) || Verify(nil, msg, sig) {
		t.Fatal("nil accepted")
	}
}

func TestLoadOrCreatePrivateKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "id.key")
	k1, err := LoadOrCreatePrivateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	k2, err := LoadOrCreatePrivateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Equal(k2) {
		t.Fatal("reload mismatch")
	}
}

func TestKeyIDAndBuilder(t *testing.T) {
	k, _ := Generate()
	if len(KeyID(&k.PublicKey)) != 32 {
		t.Fatal("key id length")
	}
	m := NewBuilder("t").String("ab").U8(1).U64(2).Message()
	want := []byte{'t', 0, 0, 0, 0, 2, 'a', 'b', 1, 0, 0, 0, 0, 0, 0, 0, 2}
	if string(m) != string(want) {
		t.Fatalf("%x", m)
	}
}
