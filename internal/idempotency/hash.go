package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// CanonicalizeJSON parses raw JSON and re-serializes it with sorted keys and stripped whitespace.
// If the body is empty or whitespace-only, an empty byte slice is returned.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 {
		return []byte{}, nil
	}

	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("invalid json payload: %w", err)
	}

	// Go's json.Marshal sorts map keys deterministically in lexicographical order.
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize json: %w", err)
	}

	return canonical, nil
}

// ComputeRequestHash generates a deterministic SHA-256 digest over the HTTP method,
// URL path, and canonicalized JSON payload.
// Format: SHA256(METHOD + "\n" + PATH + "\n" + CANONICAL_JSON)
func ComputeRequestHash(method, path string, rawBody []byte) (string, error) {
	canonicalBody, err := CanonicalizeJSON(rawBody)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	h.Write([]byte(strings.ToUpper(strings.TrimSpace(method))))
	h.Write([]byte("\n"))
	h.Write([]byte(strings.TrimSpace(path)))
	h.Write([]byte("\n"))
	h.Write(canonicalBody)

	return hex.EncodeToString(h.Sum(nil)), nil
}
