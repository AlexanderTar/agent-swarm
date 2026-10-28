package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/AlexanderTar/swarmrun/internal/webauth"
)

const authUsage = "usage: swarm auth set|status|logout-all\n"

// cmdAuth manages the web login for lan and tunnel callers. It talks to the
// daemon, which owns the database.
func cmdAuth(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, authUsage)
		return 2
	}
	switch args[0] {
	case "set":
		return authSet(args[1:], stdin, stdout, stderr)
	case "status":
		return authStatus(args[1:], stdout, stderr)
	case "logout-all":
		return authLogoutAll(args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, authUsage)
	return 2
}

func authSet(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, _, code, done := connect("auth set", args, stderr, nil)
	if done {
		return code
	}
	in := bufio.NewReader(stdin)
	fmt.Fprint(stdout, "Email: ")
	email, err := readLine(in)
	if err != nil || !strings.Contains(email, "@") {
		return fail(stderr, errors.New("enter an email address"))
	}
	password, err := readSecret(stdin, in, stdout, "Password: ")
	if err != nil {
		return fail(stderr, errors.New("no password entered"))
	}
	if len(password) < webauth.MinPassword {
		return fail(stderr, fmt.Errorf("the password must be at least %d characters", webauth.MinPassword))
	}
	again, err := readSecret(stdin, in, stdout, "Confirm password: ")
	if err != nil || again != password {
		return fail(stderr, errors.New("the passwords don't match"))
	}
	if err := c.do("PUT", "/api/auth/credentials", map[string]string{"email": email, "password": password}, nil); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Login set for %s. Everyone was signed out.\n", email)
	return 0
}

func authStatus(args []string, stdout, stderr io.Writer) int {
	c, _, code, done := connect("auth status", args, stderr, nil)
	if done {
		return code
	}
	var st struct {
		Configured bool
		Email      string
		Sessions   int
	}
	if err := c.do("GET", "/api/auth/credentials", nil, &st); err != nil {
		return fail(stderr, err)
	}
	if !st.Configured {
		fmt.Fprintln(stdout, "No login set. Run swarm auth set to turn on remote access.")
		return 0
	}
	fmt.Fprintf(stdout, "Login: %s\n%d active session(s)\n", st.Email, st.Sessions)
	return 0
}

func authLogoutAll(args []string, stdout, stderr io.Writer) int {
	c, _, code, done := connect("auth logout-all", args, stderr, nil)
	if done {
		return code
	}
	if err := c.do("DELETE", "/api/auth/sessions", nil, nil); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Signed out everywhere.")
	return 0
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readSecret reads a password without echo on a terminal, and a plain line
// otherwise (pipes and tests).
func readSecret(stdin io.Reader, in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		return string(b), err
	}
	return readLine(in)
}
