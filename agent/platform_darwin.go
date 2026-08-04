package agent

import "syscall"

// osVersion asks the kernel for the product version, the same number
// Software Update shows and the number a viewer reports: it builds
// "macOS 15.7.7" from three integers, so the name is the version with a
// word in front of it.
func osVersion() (version, name string) {
	v, err := syscall.Sysctl("kern.osproductversion")
	if err != nil || v == "" {
		return "", ""
	}
	version = dottedVersion(v)
	if version == "" {
		return "", ""
	}
	return version, "macOS " + version
}
