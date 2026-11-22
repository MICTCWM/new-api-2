package commandgo

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// ProtocolVersion is the wire protocol version this adaptor implements. It
// mirrors the CLI package version the envelope was captured from; changing it
// without changing the envelope shape makes the version/self-report
// inconsistent.
const ProtocolVersion = "1.53.1"

func generateTraceparent() string {
	var traceID [16]byte
	var parentID [8]byte
	readRandomOrTime(traceID[:], "trace id")
	readRandomOrTime(parentID[:], "parent id")
	return "00-" + hex.EncodeToString(traceID[:]) + "-" + hex.EncodeToString(parentID[:]) + "-01"
}

// readRandomOrTime fills dst from crypto/rand, falling back to a time-derived
// value when the system RNG fails. The fallback keeps the trace header
// well-formed instead of panicking or emitting an empty id.
func readRandomOrTime(dst []byte, label string) {
	if _, err := rand.Read(dst); err == nil {
		return
	} else {
		common.SysError("commandgo: crypto/rand failed generating " + label + ": " + err.Error())
	}
	seed := time.Now().UnixNano()
	for i := range dst {
		dst[i] = byte(seed >> (8 * (i % 8)))
	}
}
