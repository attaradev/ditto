// Package version holds build-time version information injected by GoReleaser
// via -ldflags.
package version

// Version, Commit, and Date are set at build time by GoReleaser. They default
// to developer-friendly values so the binary still works when built locally.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
