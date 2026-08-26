// Package version carries the build's identity.
//
// One variable for both binaries, stamped at link time, so a support question
// about a released artefact has an answer that does not depend on remembering
// which download it came from.
package version

// Version is set with -ldflags "-X .../internal/version.Version=1.2.3" by the
// release build. A local build leaves it as it is, which is the honest answer
// for one.
var Version = "dev"
