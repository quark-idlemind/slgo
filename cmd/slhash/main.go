// Command slhash reads a password on standard input and writes the
// hashed form a viewer sends to a Second Life login server.
//
// The scheme is the grid's own: the plaintext is md5'd whole and sent
// as "$1$" followed by the lowercase hex digits -- llloginhandler.cpp
// md5s --login, llsecapi.cpp prepends "$1$".  It is the same form slgo
// already mints and compares in agent/login and internal/creds, so a
// digest from here logs in wherever a stored viewer_password does.
//
//	printf %s SECRET | slhash
//
// Only the first line is read, and a trailing CR/LF is stripped, so
// typing a password and pressing return gives the same digest as a
// password piped in with no newline.  Nothing here prints, logs or
// keeps the plaintext.
package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

func main() {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr, "slhash: no password on standard input")
		os.Exit(1)
	}
	sum := md5.Sum([]byte(strings.TrimRight(line, "\r\n")))
	fmt.Println("$1$" + hex.EncodeToString(sum[:]))
}
