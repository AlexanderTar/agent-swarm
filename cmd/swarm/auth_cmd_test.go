package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const goodPW = "correct horse battery"

func runAuth(t *testing.T, srvURL, home, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"auth"}, args...)
	full = append(full, "--home", home, "--url", srvURL)
	code := runWithStdin(full, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestAuthSetSendsEmailAndPassword(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{"PUT /api/auth/credentials": ``})
	defer srv.Close()
	code, out, errOut := runAuth(t, srv.URL, home, "me@example.com\n"+goodPW+"\n"+goodPW+"\n", "set")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(lastBody(got, "/api/auth/credentials")), &body); err != nil ||
		body["email"] != "me@example.com" || body["password"] != goodPW {
		t.Fatalf("PUT body = %q", lastBody(got, "/api/auth/credentials"))
	}
	if (*got)[0].method != "PUT" || (*got)[0].via != "cli" {
		t.Fatalf("request = %+v", (*got)[0])
	}
	if strings.Contains(out+errOut, goodPW) {
		t.Fatal("the password must never be printed")
	}
	if !strings.Contains(out, "me@example.com") {
		t.Fatalf("want a confirmation naming the email, got %q", out)
	}
}

func TestAuthSetRefusesBadInput(t *testing.T) {
	for _, c := range []struct{ name, stdin, want string }{
		{"short", "me@example.com\nshort\nshort\n", "at least 12"},
		{"mismatch", "me@example.com\n" + goodPW + "\n" + goodPW + "x\n", "don't match"},
		{"no email", "\n" + goodPW + "\n" + goodPW + "\n", "email"},
		{"eof", "me@example.com\n", "password"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, got, home := stubDaemon(t, map[string]string{"PUT /api/auth/credentials": ``})
			defer srv.Close()
			code, out, errOut := runAuth(t, srv.URL, home, c.stdin, "set")
			if code != 1 || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit %d, stderr %q; want 1 mentioning %q", code, errOut, c.want)
			}
			if len(*got) != 0 {
				t.Fatalf("nothing may reach the daemon, got %+v", *got)
			}
			if strings.Contains(out+errOut, goodPW) {
				t.Fatal("the password must never be printed")
			}
		})
	}
}

func TestAuthSetShowsTheDaemonsRefusal(t *testing.T) {
	srv, _, home := stubDaemon(t, nil) // every route 404s
	defer srv.Close()
	code, _, errOut := runAuth(t, srv.URL, home, "me@example.com\n"+goodPW+"\n"+goodPW+"\n", "set")
	if code != 1 || !strings.Contains(errOut, "Unknown API route.") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestAuthStatus(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"GET /api/auth/credentials": `{"configured":true,"email":"me@example.com","sessions":2}`})
	defer srv.Close()
	code, out, errOut := runAuth(t, srv.URL, home, "", "status")
	if code != 0 || !strings.Contains(out, "me@example.com") || !strings.Contains(out, "2 active session") {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
	if (*got)[0].method != "GET" {
		t.Fatalf("request = %+v", (*got)[0])
	}

	srv2, _, home2 := stubDaemon(t, map[string]string{"GET /api/auth/credentials": `{"configured":false,"sessions":0}`})
	defer srv2.Close()
	code, out, _ = runAuth(t, srv2.URL, home2, "", "status")
	if code != 0 || !strings.Contains(out, "No login set") || !strings.Contains(out, "swarm auth set") {
		t.Fatalf("unconfigured: exit %d out %q", code, out)
	}
}

func TestAuthLogoutAll(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{"DELETE /api/auth/sessions": ``})
	defer srv.Close()
	code, out, errOut := runAuth(t, srv.URL, home, "", "logout-all")
	if code != 0 || !strings.Contains(out, "Signed out") {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
	if (*got)[0].method != "DELETE" || (*got)[0].path != "/api/auth/sessions" {
		t.Fatalf("request = %+v", (*got)[0])
	}
}

func TestAuthUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}} {
		var out, errOut bytes.Buffer
		code := runWithStdin(append([]string{"auth"}, args...), strings.NewReader(""), &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), "swarm auth set|status|logout-all") {
			t.Errorf("auth %v: exit %d stderr %q", args, code, errOut.String())
		}
	}
}
