package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/gempir/agenthold/internal/password"
)

// runHashPassword reads a password and prints its argon2id hash for
// auth.local.password_hash. On a terminal it asks twice without echo;
// otherwise it reads the first line of stdin.
func runHashPassword(args []string) error {
	if len(args) > 0 {
		return errors.New("usage: agenthold hash-password  (reads the password from the terminal or stdin)")
	}
	pw, err := readPassword(os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	h, err := password.New(pw)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func readPassword(in *os.File, prompt io.Writer) (string, error) {
	fd := int(in.Fd()) //nolint:gosec // file descriptors fit in int
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return checkPassword(strings.TrimRight(line, "\r\n"))
	}
	ask := func(label string) (string, error) {
		fmt.Fprint(prompt, label)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(prompt)
		return string(b), err
	}
	pw, err := ask("Password: ")
	if err != nil {
		return "", err
	}
	again, err := ask("Repeat password: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("the passwords do not match")
	}
	return checkPassword(pw)
}

func checkPassword(pw string) (string, error) {
	switch {
	case pw == "":
		return "", errors.New("empty password")
	case len(pw) > 1024:
		return "", errors.New("password longer than 1024 bytes")
	}
	return pw, nil
}
