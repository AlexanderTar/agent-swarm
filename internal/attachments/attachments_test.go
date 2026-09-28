package attachments

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func png(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/one.png")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func in(name string, b []byte) Input { return Input{Name: name, Data: base64.StdEncoding.EncodeToString(b)} }

func badRequest(t *testing.T, err error, want string) {
	t.Helper()
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != want {
		t.Fatalf("err = %v, want bad_request %q", err, want)
	}
}

func TestDecodeAcceptsPNGAndSniffsExtension(t *testing.T) {
	files, err := Decode([]Input{in("Login bug.JPG", png(t))})
	if err != nil || len(files) != 1 || files[0].Ext != ".png" || files[0].Name != "Login bug.JPG" {
		t.Fatalf("Decode = %+v, %v", files, err)
	}
}

func TestDecodeRefusals(t *testing.T) {
	var eleven []Input
	for range 11 {
		eleven = append(eleven, in("a.png", png(t)))
	}
	_, err := Decode(eleven)
	badRequest(t, err, "At most 10 images.")

	_, err = Decode([]Input{in("notes.png", []byte("just text, not an image"))})
	badRequest(t, err, `Image "notes.png" is not a PNG, JPEG, GIF or WebP.`)

	_, err = Decode([]Input{{Name: "x.png", Data: "%%%"}})
	badRequest(t, err, `Image "x.png" is not valid base64.`)

	big := append(png(t), make([]byte, MaxBytes)...)
	_, err = Decode([]Input{in("big.png", big)})
	badRequest(t, err, `Image "big.png" is larger than 10 MB.`)
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Login bug.png":        "login-bug.png",
		"../../etc/passwd":     "etc-passwd",
		"   ":                  "image",
		strings.Repeat("a", 90): strings.Repeat("a", 60),
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveWritesPrivateNumberedFiles(t *testing.T) {
	home := t.TempDir()
	files, _ := Decode([]Input{in("Login bug.png", png(t)), in("second.png", png(t))})
	saved, err := Save(home, "SPIKE-12", files)
	if err != nil || len(saved) != 2 {
		t.Fatalf("Save = %+v, %v", saved, err)
	}
	want := filepath.Join(home, "attachments", "SPIKE-12", "01-login-bug.png")
	if saved[0].Path != want || saved[0].Name != "Login bug.png" {
		t.Fatalf("saved[0] = %+v, want path %s", saved[0], want)
	}
	if fi, _ := os.Stat(Dir(home, "SPIKE-12")); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(saved[1].Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
}

func TestSaveFailureLeavesNoDir(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "attachments"), nil, 0o600); err != nil { // a file blocks the dir
		t.Fatal(err)
	}
	files, _ := Decode([]Input{in("a.png", png(t))})
	if _, err := Save(home, "SPIKE-1", files); err == nil {
		t.Fatal("Save succeeded under a file")
	}
}

func TestBriefSections(t *testing.T) {
	got := BriefSection([]Saved{{Path: "/h/attachments/SPIKE-12/01-login-bug.png", Name: "Login bug.png"}})
	want := "## Attachments\n\nThe user attached these images to the request. Open each one with your file or image tool before you plan, and pass the paths that matter to children in their briefs.\n\n- /h/attachments/SPIKE-12/01-login-bug.png (Login bug.png)"
	if got != want {
		t.Errorf("BriefSection =\n%s\nwant\n%s", got, want)
	}
	got = FailureSection(3, errors.New("disk full"))
	want = "## Attachments\n\nThe user attached 3 images, but Swarm could not save them (disk full). Ask the user to share them another way."
	if got != want {
		t.Errorf("FailureSection = %q", got)
	}
}
