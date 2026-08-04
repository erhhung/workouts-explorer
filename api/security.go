package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/net/idna"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/unicode/rangetable"
)

const (
	argonMemory      = 65536
	argonIterations  = 3
	argonParallelism = 1

	canonicalizationVersion = 1
	canonicalUnicodeVersion = "15.0.0"
	canonicalIDNAProfile    = "uts46-lookup-nontransitional-bidi-dns-v1"
)

var (
	errInvalidIdentity = errors.New("invalid identity")
	emailLocalPattern  = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+(?:\.[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+)*$`)
	argonParamsPattern = regexp.MustCompile(`^m=([0-9]+),t=([0-9]+),p=([0-9]+)$`)
	idnaProfile        = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.BidiRule(), idna.VerifyDNSLength(true))
	unicode15Assigned  = rangetable.Assigned("15.0.0")
)

func canonicalUsername(raw string) (display, canonical string, err error) {
	if !utf8.ValidString(raw) {
		return "", "", errInvalidIdentity
	}
	display = trimUnicode15Whitespace(raw)
	if !unicode15String(display) {
		return "", "", errInvalidIdentity
	}
	canonical = norm.NFKC.String(cases.Fold().String(norm.NFKC.String(display)))
	if utf8.RuneCountInString(canonical) < 3 || utf8.RuneCountInString(canonical) > 32 || len(canonical) > 128 {
		return "", "", errInvalidIdentity
	}
	for index, r := range canonical {
		if index == 0 && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", "", errInvalidIdentity
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) && !strings.ContainsRune("._-", r) {
			return "", "", errInvalidIdentity
		}
	}
	return display, canonical, nil
}

func canonicalEmail(raw string) (display, canonical string, err error) {
	if !utf8.ValidString(raw) {
		return "", "", errInvalidIdentity
	}
	display = trimUnicode15Whitespace(raw)
	if !unicode15String(display) {
		return "", "", errInvalidIdentity
	}
	if strings.Count(display, "@") != 1 || strings.ContainsAny(display, `\"()[],:;<>`) {
		return "", "", errInvalidIdentity
	}
	local, domain, _ := strings.Cut(display, "@")
	if len(local) == 0 || len(local) > 64 || !emailLocalPattern.MatchString(local) || domain == "" || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return "", "", errInvalidIdentity
	}
	asciiDomain, convertErr := idnaProfile.ToASCII(domain)
	if convertErr != nil || strings.HasSuffix(asciiDomain, ".") || strings.Contains(asciiDomain, "..") {
		return "", "", errInvalidIdentity
	}
	for _, label := range strings.Split(asciiDomain, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", "", errInvalidIdentity
		}
	}
	canonical = strings.ToLower(local) + "@" + strings.ToLower(asciiDomain)
	if len(canonical) > 254 {
		return "", "", errInvalidIdentity
	}
	return display, canonical, nil
}

// Filtering first fixes the persisted canonicalization domain to Unicode 15.0,
// while normalization and folding retain Unicode's stability guarantees.
func unicode15String(value string) bool {
	for _, r := range value {
		if !unicode.Is(unicode15Assigned, r) {
			return false
		}
	}
	return true
}

// Unicode 15.0 White_Space is stable and deliberately kept independent of the Go runtime tables.
func trimUnicode15Whitespace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		return r == 0x20 || r == 0x85 || r == 0xa0 || r == 0x1680 || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r >= 0x9 && r <= 0xd || r >= 0x2000 && r <= 0x200a
	})
}

type passwordHasher struct {
	minimum int
	gate    chan struct{}
}

func newPasswordHasher(minimum int) *passwordHasher {
	if minimum == 0 {
		minimum = 12
	}
	return &passwordHasher{minimum: minimum, gate: make(chan struct{}, 2)}
}

func (h *passwordHasher) validate(password string) error {
	if !utf8.ValidString(password) || strings.ContainsRune(password, 0) || utf8.RuneCountInString(password) < h.minimum || utf8.RuneCountInString(password) > 128 || len(password) > 512 {
		return errors.New("password does not meet policy")
	}
	return nil
}

func (h *passwordHasher) enter(ctx context.Context) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case h.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("password service busy")
	}
}

func (h *passwordHasher) hash(ctx context.Context, password string) (string, error) {
	if err := h.validate(password); err != nil {
		return "", err
	}
	if err := h.enter(ctx); err != nil {
		return "", err
	}
	defer func() { <-h.gate }()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func (h *passwordHasher) verify(ctx context.Context, password, encoded string) (bool, bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, false, errors.New("invalid password hash")
	}
	parameters := argonParamsPattern.FindStringSubmatch(parts[3])
	var memory, iterations uint32
	var parallelism uint8
	if len(parameters) != 4 {
		return false, false, errors.New("invalid password hash parameters")
	}
	if _, err := fmt.Sscanf(parameters[1]+" "+parameters[2]+" "+parameters[3], "%d %d %d", &memory, &iterations, &parallelism); err != nil || memory < 8192 || memory > 131072 || iterations < 1 || iterations > 10 || parallelism < 1 || parallelism > 4 {
		return false, false, errors.New("invalid password hash parameters")
	}
	salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	want, keyErr := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if saltErr != nil || keyErr != nil || len(salt) != 16 || len(want) != 32 {
		return false, false, errors.New("invalid password hash encoding")
	}
	if err := h.enter(ctx); err != nil {
		return false, false, err
	}
	defer func() { <-h.gate }()
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	current := memory == argonMemory && iterations == argonIterations && parallelism == argonParallelism
	return subtle.ConstantTimeCompare(got, want) == 1, !current, nil
}

func randomToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256(raw)
	return encoded, sum[:], nil
}

func tokenVerifier(encoded string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

func rateDigest(key []byte, operation, kind string, subject []byte) []byte {
	mac := hmac.New(sha256.New, key)
	for _, part := range [][]byte{[]byte("workouts-rate-limit-v1"), []byte(operation), []byte(kind), subject} {
		_, _ = fmt.Fprintf(mac, "%d:", len(part))
		_, _ = mac.Write(part)
	}
	return mac.Sum(nil)
}

func networkPrefix(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return net.IP(v4).Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
