// Package version carries the build's identity.
//
// One variable for both binaries, stamped at link time, so a support question
// about a released artefact has an answer that does not depend on remembering
// which download it came from.
package version

// Version is set with -ldflags "-X .../internal/version.Version=..." by the
// Makefile: a release version at a tag, and 0.2.0-SNAPSHOT - a snapshot of the
// version being developed - anywhere else. A `go build` or `go run` that skips
// the ldflags leaves it as it is, which is the honest answer for one.
var Version = "dev"
