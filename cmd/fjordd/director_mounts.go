package main

import "github.com/daemonless/fjord/pkg/engine/appjail"

// directorRemoveMount drops what service mounts at dest in appjail-director.yml.
func directorRemoveMount(directorYML, service, dest string) (string, error) {
	return appjail.RemoveMount(directorYML, service, dest)
}

// directorAttachBindMount mounts hostPath at dest in service in appjail-director.yml.
func directorAttachBindMount(directorYML, service, hostPath, dest string, opts []string) (string, error) {
	return appjail.AttachBindMount(directorYML, service, hostPath, dest, opts)
}

// directorSetMountOptions sets or clears read-only on what service mounts at dest.
func directorSetMountOptions(directorYML, service, dest string, opts []string) (string, error) {
	return appjail.SetMountOptions(directorYML, service, dest, opts)
}
