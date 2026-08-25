package importer

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// GenerateRunID returns a fresh run id in the form migrate uses:
//
//	<YYYYMMDD>T<HHMMSS>Z_<6 hex>
//
// Letters, digits and underscores only, because it becomes part of a Cumulocity
// fragment name and the platform rejects whitespace and punctuation there. That
// is why an ISO timestamp cannot be used verbatim: no dashes, no colons.
func GenerateRunID(now time.Time) string {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// A run id only has to be distinct enough to find a run again. If the
		// system cannot give us randomness, the timestamp alone still does that
		// for anything but two imports in the same second.
		return now.UTC().Format("20060102T150405Z")
	}
	return now.UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(suffix[:])
}
