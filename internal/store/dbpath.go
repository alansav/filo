package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDBPath is where the register lives when no --db is given: the user's data directory, outside every root.
func DefaultDBPath() (string, error) {
	return defaultDBPath(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

// defaultDBPath takes the OS name, environment and home lookup as inputs so every platform's rule can be tested on
// any platform.
func defaultDBPath(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	switch goos {
	case "windows":
		dir := getenv("LocalAppData")
		if dir == "" {
			return "", errors.New("cannot find the data directory: %LocalAppData% is not set; use --db")
		}
		return filepath.Join(dir, "filo", DBFileName), nil
	case "darwin", "ios":
		h, err := home()
		if err != nil || h == "" {
			return "", errors.New("cannot find the home directory; use --db")
		}
		return filepath.Join(h, "Library", "Application Support", "filo", DBFileName), nil
	default:
		// XDG Base Directory spec: a relative XDG_DATA_HOME is invalid and must be ignored.
		if dir := getenv("XDG_DATA_HOME"); dir != "" && filepath.IsAbs(dir) {
			return filepath.Join(dir, "filo", DBFileName), nil
		}
		h, err := home()
		if err != nil || h == "" {
			return "", errors.New("cannot find the home directory; use --db")
		}
		return filepath.Join(h, ".local", "share", "filo", DBFileName), nil
	}
}
