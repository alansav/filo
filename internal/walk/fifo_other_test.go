//go:build !unix

package walk

import "errors"

func mkfifo(string) error { return errors.ErrUnsupported }
