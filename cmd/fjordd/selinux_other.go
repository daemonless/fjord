//go:build !linux

package main

func selinuxEnforcing() bool          { return false }
func selinuxType(string) string       { return "" }
func labelForContainers(string) error { return nil }
