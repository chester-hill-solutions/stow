//go:build !linux && !darwin

package parentwatch

// Watch reports that no mechanism is available. Windows is not a first-release
// target, so the honest answer is to say so and let the caller log it.
func Watch(Requested) error { return ErrUnsupported }
