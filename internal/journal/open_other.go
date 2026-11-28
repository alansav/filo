//go:build !unix

package journal

// noBlock is 0 where there are no FIFOs to block on (and the register is refused anyway: docs/design.md, Register).
const noBlock = 0
