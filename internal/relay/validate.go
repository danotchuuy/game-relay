package relay

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxPeerName      = 32
	maxLobbyName     = 64
	maxMetadataBytes = 4096
	codeLength       = 6
	defaultListLimit = 50
	maxListLimit     = 200
)

// codeAlphabet omits look-alike characters (0/O, 1/I) so codes are easy to
// read aloud and type.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 32 chars: no modulo bias

var gameIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func newCode() string {
	b := make([]byte, codeLength)
	rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// normalizeCode accepts codes typed by humans: any case, with spaces or dashes.
func normalizeCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return unicode.ToUpper(r)
	}, s)
}

func cleanText(s, field string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", errf(ErrBadRequest, "%s must be valid UTF-8", field)
	}
	if n := utf8.RuneCountInString(s); n > max {
		return "", errf(ErrBadRequest, "%s is %d characters; the limit is %d", field, n, max)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "", errf(ErrBadRequest, "%s must not contain control characters", field)
	}
	return s, nil
}

// parseMetadata validates that raw is a JSON object within the size limit and
// returns its compact encoding plus a decoded copy used for list filters.
func parseMetadata(raw json.RawMessage) (json.RawMessage, map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		return nil, nil, errf(ErrBadRequest, "metadata must be a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, nil, errf(ErrBadRequest, "metadata must be a JSON object")
	}
	if buf.Len() > maxMetadataBytes {
		return nil, nil, errf(ErrBadRequest, "metadata is %d bytes; the limit is %d", buf.Len(), maxMetadataBytes)
	}
	return buf.Bytes(), decoded, nil
}

func parseFilter(filter map[string]json.RawMessage) (map[string]any, error) {
	want := make(map[string]any, len(filter))
	for k, raw := range filter {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, errf(ErrBadRequest, "filter value for %q is not valid JSON", k)
		}
		want[k] = v
	}
	return want, nil
}

// matchesFilter reports whether every filter key is present in meta with an
// equal value.
func matchesFilter(meta, want map[string]any) bool {
	for k, v := range want {
		got, ok := meta[k]
		if !ok || !reflect.DeepEqual(got, v) {
			return false
		}
	}
	return true
}
