//go:build !unix

package journal

import "errors"

func mkfifo(string) error { return errors.New("no FIFOs") }
