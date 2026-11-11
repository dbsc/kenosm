package crypto

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := New("test-secret")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	plain := "sk-super-secret-api-key-12345"
	enc, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if enc == plain || enc == "" {
		t.Fatalf("ciphertext should differ from plaintext and be non-empty")
	}

	got, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("round trip mismatch: got %q want %q", got, plain)
	}
}

func TestEncryptEmpty(t *testing.T) {
	c, _ := New("test-secret")
	enc, err := c.Encrypt("")
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	if enc != "" {
		t.Fatalf("expected empty ciphertext for empty plaintext, got %q", enc)
	}
	got, err := c.Decrypt("")
	if err != nil || got != "" {
		t.Fatalf("Decrypt empty: got %q err %v", got, err)
	}
}

func TestCipherIsRandomized(t *testing.T) {
	c, _ := New("test-secret")
	a, _ := c.Encrypt("same-plaintext")
	b, _ := c.Encrypt("same-plaintext")
	if a == b {
		t.Fatalf("encryption should be non-deterministic (random nonce)")
	}
}

func TestWrongKeyFails(t *testing.T) {
	c1, _ := New("key-one")
	c2, _ := New("key-two")
	enc, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatalf("expected decryption with wrong key to fail")
	}
	if strings.Contains(enc, "secret") {
		t.Fatalf("ciphertext should not contain plaintext")
	}
}
