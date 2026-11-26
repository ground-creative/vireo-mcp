package protocol

// canonical.go implements RFC 8785 (JSON Canonicalization Scheme) for the
// manifest digest defined by MAN-40.
//
// This is a deliberate copy of the daemon's canonicaliser, kept byte-identical
// because the digest is an interop contract: a client that canonicalises
// differently reports a failure on a correct publisher. The two copies exist
// because the adapter is a separate module; the fixtures in the specification
// (machine/fixtures/digest.json) pin both to the same expected digests, which
// is what stops them drifting.
//
// The two obvious shortcuts are both wrong:
//
//   - encoding/json sorts map members by Unicode code point; RFC 8785 sorts by
//     UTF-16 code unit. The orderings differ for characters outside the BMP.
//   - encoding/json escapes <, > and & unless told not to, and formats floats
//     with Go's rules rather than ECMAScript's. A digest taken over either is
//     reproducible only by Go.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// CanonicalJSON returns the RFC 8785 serialisation of a decoded JSON value.
func CanonicalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeString(buf, v)
	case json.Number:
		number, err := esNumber(string(v))
		if err != nil {
			return err
		}
		buf.WriteString(number)
	case float64:
		number, err := esNumber(strconv.FormatFloat(v, 'g', -1, 64))
		if err != nil {
			return err
		}
		buf.WriteString(number)
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// RFC 8785 §3.2.3: sort by UTF-16 code units, not code points.
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, v[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonical: unsupported value %T", value)
	}
	return nil
}

// writeString emits a JSON string with only the escapes RFC 8785 permits.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
				continue
			}
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// utf16Less orders two strings by their UTF-16 code units, which is what
// ECMAScript's default sort does and what RFC 8785 requires.
func utf16Less(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// esNumber converts a JSON number literal to ECMAScript Number::toString form
// (RFC 8785 §3.2.2.3).
func esNumber(literal string) (string, error) {
	literal = strings.TrimSpace(literal)
	if literal == "" {
		return "", fmt.Errorf("canonical: empty number")
	}

	// An integer literal is already canonical once its sign and leading zeros
	// are normalised; no float machinery can represent 20-digit ids safely.
	if !strings.ContainsAny(literal, ".eE") {
		neg := strings.HasPrefix(literal, "-")
		digits := strings.TrimLeft(strings.TrimPrefix(literal, "-"), "0")
		if digits == "" {
			return "0", nil // covers "-0" and all-zero literals
		}
		if neg {
			return "-" + digits, nil
		}
		return digits, nil
	}

	f, err := strconv.ParseFloat(literal, 64)
	if err != nil {
		return "", fmt.Errorf("canonical: %q is not a number: %w", literal, err)
	}
	if f != f {
		return "", fmt.Errorf("canonical: NaN is not representable in JSON")
	}
	if f == 0 {
		return "0", nil
	}

	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}

	sci := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, expPart, ok := strings.Cut(sci, "e")
	if !ok {
		return "", fmt.Errorf("canonical: unexpected float form %q", sci)
	}
	exp, err := strconv.Atoi(expPart)
	if err != nil {
		return "", fmt.Errorf("canonical: bad exponent in %q: %w", sci, err)
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return "0", nil
	}
	k := len(digits)
	n := k + exp

	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k), nil
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:], nil
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits, nil
	}
	mant := digits
	if k > 1 {
		mant = digits[:1] + "." + digits[1:]
	}
	e := n - 1
	expSign := "+"
	if e < 0 {
		expSign = "-"
		e = -e
	}
	return sign + mant + "e" + expSign + strconv.Itoa(e), nil
}

// decodeCanonical parses raw into a generic value with number literals intact.
func decodeCanonical(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// VerifyDigest reports whether a manifest document carries the digest MAN-40
// requires. A host adapter uses it to tell its model that the document it is
// about to act on is the one the business published, unmodified.
func VerifyDigest(raw []byte) (string, bool, error) {
	value, err := decodeCanonical(raw)
	if err != nil {
		return "", false, err
	}
	doc, ok := value.(map[string]any)
	if !ok {
		return "", false, fmt.Errorf("digest: document is not a JSON object")
	}
	published, _ := doc["digest"].(string)
	delete(doc, "digest")
	delete(doc, "signature")
	canon, err := CanonicalJSON(doc)
	if err != nil {
		return "", false, err
	}
	computed, err := digestOfCanonical(canon)
	if err != nil {
		return "", false, err
	}
	if published == "" {
		return computed, false, nil
	}
	return computed, published == computed, nil
}
