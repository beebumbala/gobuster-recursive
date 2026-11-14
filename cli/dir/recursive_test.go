package dir

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	urfave "github.com/urfave/cli/v2"
)

func TestRecursiveCLIUsesOneOutputSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/dir/":
			fmt.Fprint(w, "directory")
		case "/dir":
			http.Redirect(w, r, "/dir/", http.StatusMovedPermanently)
		case "/file", "/dir/file":
			fmt.Fprint(w, "file")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tmp := t.TempDir()
	words, output := filepath.Join(tmp, "words"), filepath.Join(tmp, "results")
	if err := os.WriteFile(words, []byte("dir\nfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture, err := os.Create(filepath.Join(tmp, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	original := os.Stdout
	os.Stdout = capture
	defer func() { os.Stdout = original }()
	app := &urfave.App{Commands: []*urfave.Command{Command()}}
	err = app.RunContext(t.Context(), []string{"gobuster", "dir", "-u", server.URL, "-w", words, "-R", "--recursion-depth", "1", "--no-progress", "--no-color", "-o", output})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capture.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	text, err := io.ReadAll(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"Gobuster v", "Starting gobuster", "Finished"} {
		if strings.Count(string(text), marker) != 1 {
			t.Errorf("expected one %q in %s", marker, text)
		}
	}
	results, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	summaryIndex := strings.Index(string(text), "Found paths:")
	if summaryIndex < 0 || strings.Count(string(text), "Found paths:") != 1 {
		t.Fatalf("expected one final tree: %s", text)
	}
	if !strings.Contains(string(text[:summaryIndex]), "dir/file") || strings.Index(string(text), "Finished") < summaryIndex {
		t.Fatalf("tree printed before results or after Finished: %s", text)
	}
	for _, expected := range []string{"├── dir/ (Status: 301)", "│   └── file (Status: 200) [Size: 4]", "└── file (Status: 200) [Size: 4]"} {
		if !strings.Contains(string(text[summaryIndex:]), expected) {
			t.Errorf("missing %q in tree: %s", expected, text[summaryIndex:])
		}
	}
	var rootFound, childFound bool
	for _, line := range strings.Split(string(results), "\n") {
		rootFound = rootFound || strings.HasPrefix(line, "file ")
		childFound = childFound || strings.HasPrefix(line, "dir/file ")
	}
	if !rootFound || !childFound {
		t.Fatalf("missing root or child result: %s", results)
	}
	if strings.Contains(string(results), "Found paths:") || strings.Contains(string(results), "──") {
		t.Fatalf("tree was mixed into the flat results file: %s", results)
	}
}

func TestDirectoryTreeWithoutRecursionAndQuiet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/file", "/folder/":
			fmt.Fprint(w, "data")
		case "/folder":
			http.Redirect(w, r, "/folder/", http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("quiet=%t", quiet), func(t *testing.T) {
			tmp := t.TempDir()
			words := filepath.Join(tmp, "words")
			if err := os.WriteFile(words, []byte("file\nfolder\nfile\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			capture, err := os.Create(filepath.Join(tmp, "stdout"))
			if err != nil {
				t.Fatal(err)
			}
			defer capture.Close()
			original := os.Stdout
			os.Stdout = capture
			defer func() { os.Stdout = original }()
			args := []string{"gobuster", "dir", "-u", server.URL, "-w", words, "--no-progress", "--no-color"}
			if quiet {
				args = append(args, "--quiet")
			}
			app := &urfave.App{Commands: []*urfave.Command{Command()}}
			if err := app.RunContext(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if _, err := capture.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(capture)
			if err != nil {
				t.Fatal(err)
			}
			text := string(content)
			if quiet {
				if strings.Contains(text, "Found paths:") || strings.Contains(text, "──") {
					t.Fatalf("quiet output contains a tree: %s", text)
				}
			} else {
				if !strings.Contains(text, "├── folder/ (Status: 301)") || !strings.Contains(text, "└── file (Status: 200) [Size: 4]") {
					t.Fatalf("missing tree in non-recursive mode: %s", text)
				}
			}
		})
	}
}

func TestRecursionFlagValidation(t *testing.T) {
	for _, flags := range [][]string{{"--recursion-depth", "1"}, {"-R", "--recursion-depth", "-1"}} {
		app := &urfave.App{Commands: []*urfave.Command{Command()}}
		args := append([]string{"gobuster", "dir", "-u", "http://127.0.0.1", "-w", "unused"}, flags...)
		if err := app.RunContext(t.Context(), args); err == nil || !strings.Contains(err.Error(), "recursion-depth") {
			t.Fatalf("expected recursion-depth validation, got %v", err)
		}
	}
}
