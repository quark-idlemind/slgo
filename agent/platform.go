package agent

// What the login server is told about the computer underneath.
//
// A viewer sends four of these: a three letter platform key, the
// pointer width of the build, a dotted version number, and the name a
// human would recognise -- "mac", 64, "15.7.7", "macOS 15.7.7".  None of
// it is load bearing; Linden Lab keeps it for statistics and for
// deciding whether a client is too old to let in.
//
// It is worth sending anyway, and worth sending honestly.  A login
// carrying a machine identity but no operating system is a shape no
// viewer has, and the point of the identity is to have an ordinary
// shape.  Reporting one platform in "platform" and another in
// "platform_string" would be worse than sending neither, so all of it
// comes from the same place: the host this is actually running on.

import (
	"runtime"
	"strconv"
)

// platformKey is the three letter name Linden Lab uses for an operating
// system.  A platform they have no key for is reported as Linux, which
// is what the login server treats as the general case.
func platformKey() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	case "windows":
		return "win"
	default:
		return "lnx"
	}
}

// addressSize is the pointer width of this build, 32 or 64, matching the
// viewer's ADDRESS_SIZE.
func addressSize() int { return strconv.IntSize }

// hostPlatform reports the platform key, the dotted version, and the
// readable name.  The last two are empty when the host cannot say, and
// an empty one is left out of the login rather than sent blank.
func hostPlatform() (key, version, name string) {
	version, name = osVersion()
	return platformKey(), version, name
}

// dottedVersion trims a release string down to the major.minor.patch
// the viewer sends, so "6.8.0-45-generic" becomes "6.8.0", and pads a
// short one, so "15.7" becomes "15.7.0".  A viewer always sends three
// components because it builds the string from three integers.
func dottedVersion(release string) string {
	var parts []string
	digits := ""
scan:
	for i := 0; i < len(release) && len(parts) < 3; i++ {
		switch c := release[i]; {
		case c >= '0' && c <= '9':
			digits += string(c)
		case c == '.' && digits != "":
			parts = append(parts, digits)
			digits = ""
		default:
			// Anything else ends the number: the
			// "-45-generic" a Linux release string carries
			// is not part of the version.
			break scan
		}
	}
	if digits != "" && len(parts) < 3 {
		parts = append(parts, digits)
	}
	if len(parts) == 0 {
		return ""
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return parts[0] + "." + parts[1] + "." + parts[2]
}
