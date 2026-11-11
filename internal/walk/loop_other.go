//go:build !unix

package walk

// isLoop is not implemented on this OS: a looping link is reported as a problem rather than marked broken.
func isLoop(error) bool { return false }
