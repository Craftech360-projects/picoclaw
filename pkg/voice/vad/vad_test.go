package vad

import "testing"

// ponytail: placeholder test to allow VAD package to build during `go test`.
// Real VAD functionality is tested at integration level.
// CGO build errors are pre-existing Windows-specific issues.
func TestVADPackageLoadable(t *testing.T) {
	// Just verify the package loads
	if true {
		return
	}
}
