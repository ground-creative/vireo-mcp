package protocol

import "crypto/sha256"

// digestOfCanonical returns the MAN-40 digest over an already-canonicalised
// document: sha256, hex, prefixed "sha256:".
func digestOfCanonical(canonical []byte) (string, error) {
	sum := sha256.Sum256(canonical)
	return "sha256:" + hexString(sum[:]), nil
}
