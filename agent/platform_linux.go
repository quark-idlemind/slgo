package agent

import "syscall"

// osVersion reports the kernel release, as "6.8.0" and "Linux 6.8".
//
// A viewer differs here: it sends the kernel release as the name but
// glibc's version as the number, which needs cgo to ask for and is a
// stranger answer than the kernel's.  The name is the shape that
// matters -- sysname, then major.minor -- and it is the one that is
// kept.
func osVersion() (version, name string) {
	var un syscall.Utsname
	if err := syscall.Uname(&un); err != nil {
		return "", ""
	}
	release := charsToString(un.Release[:])
	sysname := charsToString(un.Sysname[:])
	version = dottedVersion(release)
	if version == "" || sysname == "" {
		return "", ""
	}
	// "Linux 6.8": major and minor only, the way LLOSInfo truncates
	// it.
	short := version
	if i := lastDot(short); i > 0 {
		short = short[:i]
	}
	return version, sysname + " " + short
}

// charsToString reads a NUL terminated field out of a utsname.  The
// element type differs between architectures -- signed on some, unsigned
// on others -- so it takes whichever the platform hands over.
func charsToString[T int8 | uint8](c []T) string {
	b := make([]byte, 0, len(c))
	for _, x := range c {
		if x == 0 {
			break
		}
		b = append(b, byte(x))
	}
	return string(b)
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
