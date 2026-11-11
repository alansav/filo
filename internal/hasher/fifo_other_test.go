//go:build !unix

package hasher

import "errors"

func mkfifo(string) error { return errors.ErrUnsupported }
