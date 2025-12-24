// Package version contains build-time version information.
package version

// These variables are set at build time via ldflags.
var (
	// Version is the semantic version (e.g., "1.2.3").
	Version = "dev"
	// Commit is the git commit SHA.
	Commit = "unknown"
	// BuildDate is the build timestamp in RFC3339 format.
	BuildDate = "unknown"
)
