//go:build !darwin

package credentials

import "errors"

// Stash is currently macOS-only at the daemon layer. CI builds on
// Linux still need this package to compile cleanly, but every
// platformXxx call returns "unsupported platform" so any code path
// that genuinely reaches the keychain on Linux fails loudly rather
// than silently no-op'ing. Port the goback linux/windows
// implementations here when there's a real second-platform use case.

var errUnsupported = errors.New("credentials: keychain not implemented on this platform")

func platformStore(key, value string) error  { return errUnsupported }
func platformLoad(key string) (string, error) { return "", errUnsupported }
func platformDelete(key string) error         { return errUnsupported }
