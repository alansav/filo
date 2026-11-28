//go:build unix

package journal

import "syscall"

// noBlock is added to every open of a file in .filo/, so a name swapped for a FIFO after it was looked at cannot
// make Filo wait forever for the FIFO's other end.
const noBlock = syscall.O_NONBLOCK
