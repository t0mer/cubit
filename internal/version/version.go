// Package version carries the build-time version string.
package version

// Version is set at build time with
// -ldflags "-X github.com/t0mer/cubit/internal/version.Version=<v>".
var Version = "dev"
