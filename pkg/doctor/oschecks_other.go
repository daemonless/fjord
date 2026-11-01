//go:build !linux && !freebsd

package doctor

func osChecks(Config) []Check { return nil }
