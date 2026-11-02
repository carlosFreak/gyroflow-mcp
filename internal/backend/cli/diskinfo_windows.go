package cli

// Staging only applies to the macOS App Store build.
func freeBytes(string) (uint64, bool) { return 0, false }

func sameDevice(string, string) bool { return false }
