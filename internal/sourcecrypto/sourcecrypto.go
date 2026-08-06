package sourcecrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/google/uuid"
)

const (
	envelopeVersion = 1
	algorithm       = "AES-256-GCM+AES-256-GCM"
	keySize         = 32
	nonceSize       = 12
	maxCiphertext   = 64 << 10
	maxEnvelope     = 96 << 10
	maxKeyring      = 16 << 10
	maxJSONDepth    = 4
)

var (
	ErrInvalidEnvelope = errors.New("invalid source configuration envelope")
	ErrUnknownKey      = errors.New("source configuration key is unavailable")
	ErrAuthentication  = errors.New("source configuration authentication failed")
	keyIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type Purpose string

const (
	SourceConfig      Purpose = "source-config"
	JobConfigSnapshot Purpose = "job-config-snapshot"
)

type Context struct {
	Purpose   Purpose
	AccountID uuid.UUID
	RecordID  uuid.UUID
}

type Keyring struct {
	activeID string
	keys     map[string][keySize]byte
	random   io.Reader
}

type keyringDocument struct {
	ActiveKeyID string            `json:"activeKeyId"`
	Keys        map[string]string `json:"keys"`
}

type envelope struct {
	Version         int    `json:"version"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"keyId"`
	WrappedKeyNonce string `json:"wrappedKeyNonce"`
	WrappedKey      string `json:"wrappedKey"`
	PayloadNonce    string `json:"payloadNonce"`
	Ciphertext      string `json:"ciphertext"`
}

func LoadKeyring(path string) (*Keyring, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open source encryption key ring: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxKeyring+1))
	if err != nil {
		return nil, fmt.Errorf("read source encryption key ring: %w", err)
	}
	if len(data) == 0 || len(data) > maxKeyring {
		return nil, errors.New("source encryption key ring has an invalid size")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, errors.New("source encryption key ring is invalid")
	}
	if err := requireObjectFields(data, "activeKeyId", "keys"); err != nil {
		return nil, errors.New("source encryption key ring is invalid")
	}

	var document keyringDocument
	if err := decodeStrict(data, &document); err != nil {
		return nil, errors.New("source encryption key ring is invalid")
	}
	if !keyIDPattern.MatchString(document.ActiveKeyID) || len(document.Keys) == 0 {
		return nil, errors.New("source encryption key ring is invalid")
	}

	keys := make(map[string][keySize]byte, len(document.Keys))
	for id, encoded := range document.Keys {
		if !keyIDPattern.MatchString(id) {
			return nil, errors.New("source encryption key ring is invalid")
		}
		decoded, err := decodeCanonical(encoded)
		if err != nil || len(decoded) != keySize {
			return nil, errors.New("source encryption key ring is invalid")
		}
		var key [keySize]byte
		copy(key[:], decoded)
		keys[id] = key
	}
	if _, ok := keys[document.ActiveKeyID]; !ok {
		return nil, errors.New("source encryption key ring is invalid")
	}
	return &Keyring{activeID: document.ActiveKeyID, keys: keys, random: rand.Reader}, nil
}

func (k *Keyring) Encrypt(context Context, plaintext []byte) ([]byte, error) {
	if err := validateContext(context); err != nil {
		return nil, err
	}
	if len(plaintext)+16 > maxCiphertext {
		return nil, errors.New("source configuration exceeds the encryption limit")
	}
	masterKey, ok := k.keys[k.activeID]
	if !ok || k.random == nil {
		return nil, ErrUnknownKey
	}

	dek := make([]byte, keySize)
	if _, err := io.ReadFull(k.random, dek); err != nil {
		return nil, errors.New("generate source configuration key")
	}
	payloadNonce, err := randomNonce(k.random)
	if err != nil {
		return nil, err
	}
	wrappedKeyNonce, err := randomNonce(k.random)
	if err != nil {
		return nil, err
	}

	payloadAEAD, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	masterAEAD, err := newGCM(masterKey[:])
	if err != nil {
		return nil, err
	}
	ciphertext := payloadAEAD.Seal(nil, payloadNonce, plaintext, aad("payload", context))
	wrappedKey := masterAEAD.Seal(nil, wrappedKeyNonce, dek, aad("dek", context))

	document := envelope{
		Version:         envelopeVersion,
		Algorithm:       algorithm,
		KeyID:           k.activeID,
		WrappedKeyNonce: encode(wrappedKeyNonce),
		WrappedKey:      encode(wrappedKey),
		PayloadNonce:    encode(payloadNonce),
		Ciphertext:      encode(ciphertext),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("encode source configuration envelope")
	}
	return encoded, nil
}

func (k *Keyring) Decrypt(context Context, encoded []byte) ([]byte, error) {
	if err := validateContext(context); err != nil {
		return nil, err
	}
	document, wrappedKeyNonce, wrappedKey, payloadNonce, ciphertext, err := parseEnvelope(encoded)
	if err != nil {
		return nil, err
	}
	masterKey, ok := k.keys[document.KeyID]
	if !ok {
		return nil, ErrUnknownKey
	}
	masterAEAD, err := newGCM(masterKey[:])
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	dek, err := masterAEAD.Open(nil, wrappedKeyNonce, wrappedKey, aad("dek", context))
	if err != nil {
		return nil, ErrAuthentication
	}
	payloadAEAD, err := newGCM(dek)
	if err != nil {
		return nil, ErrAuthentication
	}
	plaintext, err := payloadAEAD.Open(nil, payloadNonce, ciphertext, aad("payload", context))
	if err != nil {
		return nil, ErrAuthentication
	}
	return plaintext, nil
}

// Rewrap changes only the encrypted DEK and its nonce; payload ciphertext stays intact.
func (k *Keyring) Rewrap(context Context, encoded []byte) ([]byte, error) {
	if err := validateContext(context); err != nil {
		return nil, err
	}
	document, oldNonce, wrappedKey, _, _, err := parseEnvelope(encoded)
	if err != nil {
		return nil, err
	}
	oldKey, ok := k.keys[document.KeyID]
	if !ok {
		return nil, ErrUnknownKey
	}
	oldAEAD, _ := newGCM(oldKey[:])
	dek, err := oldAEAD.Open(nil, oldNonce, wrappedKey, aad("dek", context))
	if err != nil {
		return nil, ErrAuthentication
	}
	activeKey, ok := k.keys[k.activeID]
	if !ok || k.random == nil {
		return nil, ErrUnknownKey
	}
	newNonce, err := randomNonce(k.random)
	if err != nil {
		return nil, err
	}
	activeAEAD, _ := newGCM(activeKey[:])
	document.KeyID = k.activeID
	document.WrappedKeyNonce = encode(newNonce)
	document.WrappedKey = encode(activeAEAD.Seal(nil, newNonce, dek, aad("dek", context)))
	rewrapped, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("encode source configuration envelope")
	}
	return rewrapped, nil
}

func parseEnvelope(encoded []byte) (envelope, []byte, []byte, []byte, []byte, error) {
	var document envelope
	if len(encoded) == 0 || len(encoded) > maxEnvelope {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	if err := rejectDuplicateKeys(encoded); err != nil {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	if err := requireObjectFields(encoded, "version", "algorithm", "keyId", "wrappedKeyNonce", "wrappedKey", "payloadNonce", "ciphertext"); err != nil {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	if err := decodeStrict(encoded, &document); err != nil {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	if document.Version != envelopeVersion || document.Algorithm != algorithm || !keyIDPattern.MatchString(document.KeyID) {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	wrappedKeyNonce, err := decodeLength(document.WrappedKeyNonce, nonceSize)
	if err != nil {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	wrappedKey, err := decodeLength(document.WrappedKey, keySize+16)
	if err != nil {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	payloadNonce, err := decodeLength(document.PayloadNonce, nonceSize)
	if err != nil {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	ciphertext, err := decodeCanonical(document.Ciphertext)
	if err != nil || len(ciphertext) < 16 || len(ciphertext) > maxCiphertext {
		return document, nil, nil, nil, nil, ErrInvalidEnvelope
	}
	return document, wrappedKeyNonce, wrappedKey, payloadNonce, ciphertext, nil
}

func validateContext(context Context) error {
	if context.Purpose != SourceConfig && context.Purpose != JobConfigSnapshot {
		return errors.New("invalid source configuration purpose")
	}
	if context.AccountID == uuid.Nil || context.RecordID == uuid.Nil {
		return errors.New("source configuration context requires record identities")
	}
	return nil
}

func aad(part string, context Context) []byte {
	return []byte(fmt.Sprintf("workouts-explorer/source-config-envelope/v1/%s\n%s\n%s\n%s", part, context.Purpose, context.AccountID, context.RecordID))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func randomNonce(random io.Reader) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, errors.New("generate source configuration nonce")
	}
	return nonce, nil
}

func encode(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func decodeLength(encoded string, length int) ([]byte, error) {
	value, err := decodeCanonical(encoded)
	if err != nil || len(value) != length {
		return nil, ErrInvalidEnvelope
	}
	return value, nil
}

func decodeCanonical(encoded string) ([]byte, error) {
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(value) != encoded {
		return nil, ErrInvalidEnvelope
	}
	return value, nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func requireObjectFields(data []byte, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) != len(required) {
		return errors.New("JSON object has unexpected fields")
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return errors.New("JSON object is missing a required field")
		}
	}
	return nil
}
