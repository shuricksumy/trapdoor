// Package version holds build information, set via -ldflags at build time.
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a human-readable version line.
func String() string {
	return Version + " (" + Commit + ", " + Date + ")"
}
