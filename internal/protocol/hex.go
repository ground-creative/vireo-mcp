package protocol

import "encoding/hex"

// hexString encodes bytes as lowercase hex. Kept separate so digestOfCanonical
// reads as one line.
func hexString(b []byte) string { return hex.EncodeToString(b) }
