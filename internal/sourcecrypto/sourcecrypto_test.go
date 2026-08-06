package sourcecrypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var testContext = Context{
	Purpose:   SourceConfig,
	AccountID: uuid.MustParse("01989c84-9da8-7a98-a984-300f27e48a55"),
	RecordID:  uuid.MustParse("01989c84-b106-7aec-8385-2d928dd7c92d"),
}

func TestEncryptDecryptAndContextBinding(t *testing.T) {
	keyring := testKeyring("current", map[string]byte{"current": 1})
	plaintext := []byte(`{"version":1,"path":"/archive/exports"}`)
	encoded, err := keyring.Encrypt(testContext, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("/archive")) {
		t.Fatal("envelope contains plaintext configuration")
	}
	decrypted, err := keyring.Decrypt(testContext, encoded)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypt=%q err=%v", decrypted, err)
	}

	wrong := testContext
	wrong.Purpose = JobConfigSnapshot
	if _, err := keyring.Decrypt(wrong, encoded); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong purpose error = %v", err)
	}
	wrong = testContext
	wrong.RecordID = uuid.New()
	if _, err := keyring.Decrypt(wrong, encoded); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong record error = %v", err)
	}
	wrong = testContext
	wrong.AccountID = uuid.New()
	if _, err := keyring.Decrypt(wrong, encoded); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong account error = %v", err)
	}
}

func TestEnvelopeTamperingFailsClosed(t *testing.T) {
	keyring := testKeyring("current", map[string]byte{"current": 2})
	encoded, err := keyring.Encrypt(testContext, []byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	encodedCiphertext := document["ciphertext"].(string)
	ciphertext, _ := base64.RawURLEncoding.DecodeString(encodedCiphertext)
	ciphertext[len(ciphertext)-1] ^= 1
	document["ciphertext"] = base64.RawURLEncoding.EncodeToString(ciphertext)
	tampered, _ := json.Marshal(document)
	if _, err := keyring.Decrypt(testContext, tampered); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tampered ciphertext error = %v", err)
	}

	invalid := []string{
		`{}`,
		`{"version":1,"version":1}`,
		strings.Replace(string(encoded), `"keyId":"current"`, `"KeyId":"current"`, 1),
		strings.Replace(string(encoded), `"keyId":"current"`, `"keyId":"current","KeyId":"current"`, 1),
		string(encoded) + `{}`,
		strings.Replace(string(encoded), `"version":1`, `"version":2`, 1),
		strings.Replace(string(encoded), `"algorithm":"AES-256-GCM+AES-256-GCM"`, `"algorithm":"other"`, 1),
		strings.Replace(string(encoded), `"keyId":"current"`, `"keyId":"INVALID"`, 1),
		strings.Replace(string(encoded), `"payloadNonce":"`, `"unexpected":true,"payloadNonce":"`, 1),
		strings.Replace(string(encoded), `"payloadNonce":"`, `"payloadNonce":"%0A`, 1),
		strings.Repeat(" ", maxEnvelope+1),
	}
	for _, candidate := range invalid {
		if _, err := keyring.Decrypt(testContext, []byte(candidate)); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("invalid envelope error = %v for %.40q", err, candidate)
		}
	}
}

func TestPredecessorDecryptAndRewrap(t *testing.T) {
	oldRing := testKeyring("old", map[string]byte{"old": 3})
	encoded, err := oldRing.Encrypt(testContext, []byte("protected"))
	if err != nil {
		t.Fatal(err)
	}
	newRing := testKeyring("new", map[string]byte{"old": 3, "new": 4})
	var original envelope
	if err := json.Unmarshal(encoded, &original); err != nil {
		t.Fatal(err)
	}
	rewrapped, err := newRing.Rewrap(testContext, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rewrapped, []byte(`"keyId":"old"`)) || !bytes.Contains(rewrapped, []byte(`"keyId":"new"`)) {
		t.Fatalf("unexpected rewrapped envelope: %s", rewrapped)
	}
	plaintext, err := newRing.Decrypt(testContext, rewrapped)
	if err != nil || string(plaintext) != "protected" {
		t.Fatalf("decrypt=%q err=%v", plaintext, err)
	}
	if _, err := oldRing.Decrypt(testContext, rewrapped); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("retired ring error = %v", err)
	}
	var rotated envelope
	if err := json.Unmarshal(rewrapped, &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Ciphertext != original.Ciphertext || rotated.PayloadNonce != original.PayloadNonce {
		t.Fatal("rewrap changed payload encryption")
	}
	wrong := testContext
	wrong.RecordID = uuid.New()
	if _, err := newRing.Rewrap(wrong, encoded); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong-context rewrap error = %v", err)
	}
	tampered := strings.Replace(string(encoded), original.WrappedKey, "A"+original.WrappedKey[1:], 1)
	if _, err := newRing.Rewrap(testContext, []byte(tampered)); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tampered-key rewrap error = %v", err)
	}
	newRing.random = bytes.NewReader(nil)
	if _, err := newRing.Rewrap(testContext, encoded); err == nil {
		t.Fatal("rewrap accepted random source failure")
	}
}

func TestLoadKeyringRejectsInvalidDocuments(t *testing.T) {
	validKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, keySize))
	valid := `{"activeKeyId":"current","keys":{"current":"` + validKey + `"}}`
	path := t.TempDir() + "/keyring.json"
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadKeyring(path)
	if err != nil || loaded.activeID != "current" {
		t.Fatalf("active=%q err=%v", loaded.activeID, err)
	}

	invalid := []string{
		`{}`,
		`{"activeKeyId":"missing","keys":{"current":"` + validKey + `"}}`,
		`{"activeKeyId":"current","keys":{"current":"short"}}`,
		`{"activeKeyId":"INVALID","keys":{"INVALID":"` + validKey + `"}}`,
		`{"activeKeyId":"current","activeKeyId":"current","keys":{"current":"` + validKey + `"}}`,
		`{"ActiveKeyId":"current","keys":{"current":"` + validKey + `"}}`,
		`{"activeKeyId":"current","ActiveKeyId":"current","keys":{"current":"` + validKey + `"}}`,
		`{"activeKeyId":"current","keys":{"current":"` + validKey + `","current":"` + validKey + `"}}`,
		`{"activeKeyId":"current","keys":{"current":"` + validKey[:20] + `\n` + validKey[20:] + `"}}`,
		`{"activeKeyId":"current","keys":{"current":"` + validKey + `"},"extra":true}`,
	}
	for _, candidate := range invalid {
		if err := os.WriteFile(path, []byte(candidate), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadKeyring(path); err == nil {
			t.Fatalf("invalid key ring accepted: %.60q", candidate)
		}
	}
}

func TestEncryptionLimitsAndRandomFailure(t *testing.T) {
	keyring := testKeyring("current", map[string]byte{"current": 6})
	if _, err := keyring.Encrypt(testContext, make([]byte, maxCiphertext)); err == nil {
		t.Fatal("oversized plaintext accepted")
	}
	keyring.random = bytes.NewReader(nil)
	if _, err := keyring.Encrypt(testContext, []byte("value")); err == nil {
		t.Fatal("random source failure accepted")
	}
	if _, err := keyring.Encrypt(Context{}, []byte("value")); err == nil {
		t.Fatal("empty context accepted")
	}
}

func testKeyring(active string, keyBytes map[string]byte) *Keyring {
	keys := make(map[string][keySize]byte, len(keyBytes))
	for id, value := range keyBytes {
		var key [keySize]byte
		for index := range key {
			key[index] = value
		}
		keys[id] = key
	}
	return &Keyring{activeID: active, keys: keys, random: bytes.NewReader(bytes.Repeat([]byte{9}, 4096))}
}
