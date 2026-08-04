//go:build !darwin && !linux

package agent

// osVersion has nothing to say on a platform nobody has written this
// for, and saying nothing is the right answer: the fields are left out
// of the login rather than filled with a guess.
func osVersion() (version, name string) { return "", "" }
