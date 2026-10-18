package vault

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// Encryption at rest for sensitive folders (e.g. private/).
//
// Envelope encryption: every file gets a random 256-bit data key (DEK); the
// content is sealed with XChaCha20-Poly1305 under the DEK and the DEK is
// sealed under the vault key (KEK). Files stay text (armored base64) so git
// and sync tools treat them normally:
//
//	NEXUS-ENCRYPTED v1
//	<base64, 76 columns: kid(8) | nonce(24) | sealed DEK(48) | nonce(24) | sealed content>
//
// kid is the first 8 bytes of sha256(KEK), so a wrong key is reported as
// such. The vault decrypts transparently for authorised reads (the owner's
// API; agents only when encryption.agent_access is set). The index stores
// links/tags/title but not the body of encrypted notes unless
// encryption.searchable is set. Snapshots and backups keep the ciphertext.
//
// The key is NOT stored in the vault: losing it loses the encrypted notes.
// Back it up separately (password manager).

// EncryptedMagic is the first line of encrypted files.
const EncryptedMagic = "NEXUS-ENCRYPTED v1"

// ErrNoKey is returned when an encrypted file is read without the key.
var ErrNoKey = errors.New("vault: note is encrypted and the vault key is not available")

// EncryptionOptions configure encryption at rest.
type EncryptionOptions struct {
	// Folders are vault folders whose notes are encrypted, e.g. ["private/"].
	Folders []string
	// Key is the 32-byte vault key (KEK).
	Key []byte
	// Searchable indexes the decrypted body (full-text search and snippets).
	Searchable bool
	// AgentAccess lets agent tools read encrypted notes.
	AgentAccess bool
}

type crypter struct {
	folders     []string
	kek         []byte
	kid         []byte
	searchable  bool
	agentAccess bool
}

func newCrypter(o *EncryptionOptions) (*crypter, error) {
	if o == nil || len(o.Folders) == 0 {
		return nil, nil
	}
	if len(o.Key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("vault: encryption key must be %d bytes", chacha20poly1305.KeySize)
	}
	c := &crypter{kek: append([]byte(nil), o.Key...), searchable: o.Searchable, agentAccess: o.AgentAccess}
	for _, f := range o.Folders {
		f = strings.Trim(strings.TrimSpace(strings.ReplaceAll(f, "\\", "/")), "/")
		if f != "" {
			c.folders = append(c.folders, strings.ToLower(f)+"/")
		}
	}
	sum := sha256.Sum256(c.kek)
	c.kid = sum[:8]
	return c, nil
}

// covers reports whether rel is inside an encrypted folder.
func (c *crypter) covers(rel string) bool {
	if c == nil {
		return false
	}
	l := strings.ToLower(rel)
	for _, f := range c.folders {
		if strings.HasPrefix(l, f) {
			return true
		}
	}
	return false
}

// IsEncrypted reports whether b is an encrypted vault file.
func IsEncrypted(b []byte) bool {
	return bytes.HasPrefix(b, []byte(EncryptedMagic+"\n")) || bytes.Equal(bytes.TrimSpace(b), []byte(EncryptedMagic))
}

var cryptAD = []byte(EncryptedMagic)

func (c *crypter) seal(plain []byte) ([]byte, error) {
	dek := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	kw, err := chacha20poly1305.NewX(c.kek)
	if err != nil {
		return nil, err
	}
	n1 := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(n1); err != nil {
		return nil, err
	}
	wrapped := kw.Seal(nil, n1, dek, cryptAD)
	cw, err := chacha20poly1305.NewX(dek)
	if err != nil {
		return nil, err
	}
	n2 := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(n2); err != nil {
		return nil, err
	}
	var blob bytes.Buffer
	blob.Write(c.kid)
	blob.Write(n1)
	blob.Write(wrapped)
	blob.Write(n2)
	blob.Write(cw.Seal(nil, n2, plain, cryptAD))
	enc := base64.StdEncoding.EncodeToString(blob.Bytes())
	var out strings.Builder
	out.WriteString(EncryptedMagic + "\n")
	for len(enc) > 76 {
		out.WriteString(enc[:76] + "\n")
		enc = enc[76:]
	}
	out.WriteString(enc + "\n")
	return []byte(out.String()), nil
}

func (c *crypter) open(b []byte) ([]byte, error) {
	if c == nil {
		return nil, ErrNoKey
	}
	body := strings.TrimPrefix(string(b), EncryptedMagic)
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(body), ""))
	if err != nil {
		return nil, fmt.Errorf("vault: corrupt encrypted file: %w", err)
	}
	const hdr = 8 + chacha20poly1305.NonceSizeX + chacha20poly1305.KeySize + chacha20poly1305.Overhead + chacha20poly1305.NonceSizeX
	if len(raw) < hdr+chacha20poly1305.Overhead {
		return nil, fmt.Errorf("vault: corrupt encrypted file: too short")
	}
	if !bytes.Equal(raw[:8], c.kid) {
		return nil, fmt.Errorf("%w (encrypted with a different key)", ErrNoKey)
	}
	p := 8
	n1 := raw[p : p+chacha20poly1305.NonceSizeX]
	p += chacha20poly1305.NonceSizeX
	wrapped := raw[p : p+chacha20poly1305.KeySize+chacha20poly1305.Overhead]
	p += chacha20poly1305.KeySize + chacha20poly1305.Overhead
	n2 := raw[p : p+chacha20poly1305.NonceSizeX]
	p += chacha20poly1305.NonceSizeX
	kw, err := chacha20poly1305.NewX(c.kek)
	if err != nil {
		return nil, err
	}
	dek, err := kw.Open(nil, n1, wrapped, cryptAD)
	if err != nil {
		return nil, fmt.Errorf("vault: cannot unwrap file key: %w", err)
	}
	cw, err := chacha20poly1305.NewX(dek)
	if err != nil {
		return nil, err
	}
	plain, err := cw.Open(nil, n2, raw[p:], cryptAD)
	if err != nil {
		return nil, fmt.Errorf("vault: encrypted file failed authentication (tampered or corrupt): %w", err)
	}
	return plain, nil
}

// decode returns the plaintext of on-disk bytes.
func (v *Vault) decode(raw []byte) ([]byte, error) {
	if !IsEncrypted(raw) {
		return raw, nil
	}
	return v.crypt.open(raw)
}

// encode returns the on-disk bytes for a note at rel.
func (v *Vault) encode(rel string, plain []byte) ([]byte, error) {
	if !v.crypt.covers(rel) {
		return plain, nil
	}
	return v.crypt.seal(plain)
}

// IsPrivate reports whether rel is in an encrypted folder.
func (v *Vault) IsPrivate(rel string) bool { return v.crypt.covers(rel) }

// AgentMayRead reports whether agent tools may read rel.
func (v *Vault) AgentMayRead(rel string) bool {
	return !v.crypt.covers(rel) || v.crypt.agentAccess
}

// ---------------------------------------------------------------------------
// Key management

// LoadOrCreateKeyFile reads a base64 32-byte key from path, creating it
// (mode 0600) when missing. A key file readable by others is refused.
func LoadOrCreateKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key := make([]byte, chacha20poly1305.KeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := writeAtomicMode(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("vault: key file %s must not be readable by others (chmod 600)", path)
		}
	}
	return decodeKey(string(b))
}

func decodeKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("vault: key must be %d bytes, base64", chacha20poly1305.KeySize)
	}
	return key, nil
}

// LoadOrCreateKeychainKey reads the key from the macOS Keychain (generic
// password, service "nexus-vault"), creating it when missing.
func LoadOrCreateKeychainKey(account string) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("vault: keychain key source is only available on macOS")
	}
	if account == "" {
		account = "vault-key"
	}
	out, err := exec.Command("security", "find-generic-password", "-s", "nexus-vault", "-a", account, "-w").Output()
	if err == nil {
		return decodeKey(string(out))
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(key)
	if out, err := exec.Command("security", "add-generic-password", "-s", "nexus-vault", "-a", account, "-w", enc, "-U").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("vault: keychain: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return key, nil
}
