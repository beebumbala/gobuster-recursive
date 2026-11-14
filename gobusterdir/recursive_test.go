package gobusterdir_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OJ/gobuster/v3/gobusterdir"
	"github.com/OJ/gobuster/v3/libgobuster"
)

type scanOutput struct {
	results  []string
	messages []string
	issued   int
	expected int
	summary  string
}

func scan(t *testing.T, target, words string, configure func(*gobusterdir.OptionsDir, *libgobuster.Options)) scanOutput {
	t.Helper()
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte(words), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := gobusterdir.NewOptions()
	opts.URL, _ = url.Parse(target)
	opts.Timeout = time.Second
	opts.Recursive = true
	opts.RecursionDepth = 2
	opts.StatusCodesBlacklistParsed.Add(404)
	global := libgobuster.Options{Threads: 4, Wordlist: wordlist}
	if configure != nil {
		configure(opts, &global)
	}
	logger := libgobuster.NewLogger(false)
	plugin, err := gobusterdir.New(&global, opts, logger)
	if err != nil {
		t.Fatal(err)
	}
	g, err := libgobuster.NewGobuster(&global, plugin, logger)
	if err != nil {
		t.Fatal(err)
	}
	var out scanOutput
	var consumers sync.WaitGroup
	consumers.Add(3)
	go func() {
		defer consumers.Done()
		for result := range g.Progress.ResultChan {
			out.results = append(out.results, strings.TrimSpace(result.(gobusterdir.Result).Path))
		}
	}()
	go func() {
		defer consumers.Done()
		for message := range g.Progress.MessageChan {
			out.messages = append(out.messages, message.Message)
		}
	}()
	go func() {
		defer consumers.Done()
		for err := range g.Progress.ErrorChan {
			t.Errorf("scan error: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err = g.Run(ctx)
	consumers.Wait()
	if err != nil {
		t.Fatal(err)
	}
	out.issued, out.expected = g.Progress.RequestsIssued(), g.Progress.RequestsExpected()
	if out.issued != out.expected {
		t.Fatalf("incomplete accounting: issued %d, expected %d", out.issued, out.expected)
	}
	var summary strings.Builder
	if err := plugin.WriteSummary(&summary); err != nil {
		t.Fatal(err)
	}
	out.summary = summary.String()
	return out
}

func TestRecursiveBreadthFirst(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(fmt.Sprintf("follow=%t", follow), func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var requests []string
			directories := map[string]bool{"/base/": true, "/base/a/": true, "/base/b/": true, "/base/a/a/": true, "/base/b/b/": true}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.URL.Path)
				mu.Unlock()
				switch {
				case directories[r.URL.Path]:
					fmt.Fprint(w, "directory")
				case directories[r.URL.Path+"/"]:
					http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
				case strings.HasSuffix(r.URL.Path, "/leaf"):
					fmt.Fprint(w, "file")
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			out := scan(t, server.URL+"/base/", "a\na\nb\nleaf\n../outside\n%2e%2e/outside\n", func(o *gobusterdir.OptionsDir, _ *libgobuster.Options) {
				o.FollowRedirect, o.NoStatus, o.HideLength = follow, true, true
			})
			for _, path := range []string{"leaf", "a/leaf", "b/leaf", "a/a/leaf", "b/b/leaf"} {
				if !slices.Contains(out.results, path) {
					t.Errorf("missing %s in %v", path, out.results)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if slices.Index(requests, "/base/b/leaf") > slices.Index(requests, "/base/a/a/leaf") {
				t.Fatalf("entered depth 2 before finishing depth 1: %v", requests)
			}
			for _, path := range []string{"/base/a/leaf", "/base/b/leaf"} {
				count := 0
				for _, request := range requests {
					if request == path {
						count++
					}
					if strings.Contains(request, "outside") {
						t.Errorf("out-of-scope request: %s", request)
					}
				}
				if count != 1 {
					t.Errorf("directory scanned %d times: %s", count, path)
				}
			}
		})
	}
}

func TestRecursiveCandidatesAndWildcard(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/plain" || r.URL.Path == "/plain/" || r.URL.Path == "/v1.0/":
			fmt.Fprint(w, "directory")
		case r.URL.Path == "/v1.0" || r.URL.Path == "/wild":
			http.Redirect(w, r, r.URL.Path+"/", http.StatusPermanentRedirect)
		case strings.HasPrefix(r.URL.Path, "/wild/"):
			fmt.Fprint(w, "wildcard")
		case r.URL.Path == "/plain/leaf" || r.URL.Path == "/v1.0/leaf":
			fmt.Fprint(w, "file")
		case r.URL.Path == "/login-alias":
			http.Redirect(w, r, "/login", http.StatusFound)
		case r.URL.Path == "/login":
			fmt.Fprint(w, "login")
		case r.URL.Path == "/excluded":
			fmt.Fprint(w, "1234567")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := scan(t, server.URL, "plain\nv1.0\nwild\nlogin-alias\nexcluded\nleaf\n", func(o *gobusterdir.OptionsDir, _ *libgobuster.Options) {
		o.FollowRedirect = true
		o.ExcludeLengthParsed.Add(7)
	})
	for _, path := range []string{"plain/leaf", "v1.0/leaf"} {
		if !slices.Contains(out.results, path) {
			t.Errorf("missing %s", path)
		}
	}
	if !strings.Contains(strings.Join(out.messages, "\n"), "Skipping directory "+server.URL+"/wild/") {
		t.Errorf("missing wildcard warning: %v", out.messages)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, forbidden := range []string{"/wild/leaf", "/login-alias/", "/login/leaf", "/excluded/"} {
		if slices.Contains(requests, forbidden) {
			t.Errorf("unexpected request: %s", forbidden)
		}
	}
}

func TestRecursiveDepthAndDisabled(t *testing.T) {
	t.Parallel()
	for _, recursive := range []bool{false, true} {
		t.Run(fmt.Sprintf("recursive=%t", recursive), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" || r.URL.Path == "/a/" || r.URL.Path == "/a/a/" {
					fmt.Fprint(w, "dir")
				} else if r.URL.Path == "/a" || r.URL.Path == "/a/a" {
					http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
				} else {
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			out := scan(t, server.URL, "a\n", func(o *gobusterdir.OptionsDir, _ *libgobuster.Options) {
				o.Recursive, o.RecursionDepth = recursive, 1
			})
			want := 1
			if recursive {
				want = 2
			}
			if out.issued != want {
				t.Fatalf("wanted %d guesses, got %d", want, out.issued)
			}
			if recursive && !strings.Contains(out.summary, "    └── a/ (Status: 301)") {
				t.Fatalf("directory at depth limit missing from tree: %s", out.summary)
			}
		})
	}
}

func TestRecursiveAdditionalWordsAndOffset(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/entry/" {
			fmt.Fprint(w, "dir")
		} else if r.URL.Path == "/entry" {
			http.Redirect(w, r, "/entry/", http.StatusMovedPermanently)
		} else if r.URL.Path == "/entry/skipped" || r.URL.Path == "/entry/entry" || strings.HasSuffix(r.URL.Path, ".txt") || strings.HasSuffix(r.URL.Path, ".bak") || strings.HasSuffix(r.URL.Path, ".extra") {
			fmt.Fprint(w, "file")
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := scan(t, server.URL, "skipped\n\nentry\n", func(o *gobusterdir.OptionsDir, g *libgobuster.Options) {
		o.RecursionDepth, o.DiscoverBackup = 1, true
		o.ExtensionsParsed.Add("txt")
		g.WordlistOffset = 1
		g.PatternFile, g.Patterns = "test", []string{"{GOBUSTER}"}
		g.DiscoverPatternFile, g.DiscoverPatterns = "test", []string{"{GOBUSTER}.extra"}
	})
	for _, expected := range []string{"entry.bak", "entry.extra", "entry.txt", "entry/skipped", "entry/entry.bak", "entry/entry.extra", "entry/entry.txt"} {
		if !slices.Contains(out.results, expected) {
			t.Errorf("missing %s in %v", expected, out.results)
		}
	}
	if slices.Contains(out.results, "skipped") {
		t.Error("offset ignored on root pass")
	}
}

func TestRecursiveRedirectScope(t *testing.T) {
	t.Parallel()
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("followed redirect outside scope: %s", r.URL)
		fmt.Fprint(w, "outside")
	}))
	defer outside.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base/":
			fmt.Fprint(w, "root")
		case "/base/external":
			http.Redirect(w, r, outside.URL+"/external/", http.StatusFound)
		case "/base/escape":
			http.Redirect(w, r, "/outside/", http.StatusFound)
		case "/outside/":
			t.Error("followed redirect outside root path")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	scan(t, server.URL+"/base/", "external\nescape\n", func(o *gobusterdir.OptionsDir, _ *libgobuster.Options) {
		o.FollowRedirect = true
	})
}

func TestRecursiveDirectoryRedirectAdvertisesHTTP(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(fmt.Sprintf("follow=%t", follow), func(t *testing.T) {
			t.Parallel()
			directories := []string{".cache", "bonus", "captcha", "media", "static"}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil {
					t.Error("directory scan left HTTPS")
				}
				if r.URL.Path == "/" {
					fmt.Fprint(w, "root")
					return
				}
				for _, directory := range directories {
					switch r.URL.Path {
					case "/" + directory:
						http.Redirect(w, r, "http://"+r.Host+"/"+directory+"/", http.StatusMovedPermanently)
						return
					case "/" + directory + "/":
						fmt.Fprint(w, "directory")
						return
					case "/" + directory + "/leaf":
						fmt.Fprint(w, "file")
						return
					}
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			out := scan(t, server.URL, strings.Join(directories, "\n")+"\nleaf\n", func(o *gobusterdir.OptionsDir, _ *libgobuster.Options) {
				o.NoTLSValidation, o.FollowRedirect = true, follow
				o.RecursionDepth = 1
			})
			for _, directory := range directories {
				if !slices.Contains(out.results, directory+"/leaf") {
					t.Errorf("directory from HTTP Location was not scanned over HTTPS: %s", directory)
				}
			}
		})
	}
}

func TestRecursiveFileResponsesDoNotBecomeDirectories(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/admin" || r.URL.Path == "/admin/" || r.URL.Path == "/admin/leaf":
			fmt.Fprint(w, "directory or child")
		case strings.HasPrefix(r.URL.Path, "/script.php"):
			// PHP handlers can accept arbitrary PATH_INFO after the filename.
			fmt.Fprint(w, "same PHP response")
		case strings.HasPrefix(r.URL.Path, "/.htaccess") || strings.HasPrefix(r.URL.Path, "/php.ini"):
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "blanket deny rule")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := scan(t, server.URL, "admin\nscript\n.htaccess\nphp.ini\nleaf\n", func(o *gobusterdir.OptionsDir, _ *libgobuster.Options) {
		o.ExtensionsParsed.Add("php")
		o.RecursionDepth = 1
	})
	for _, expected := range []string{"admin/leaf", "script.php", ".htaccess", "php.ini"} {
		if !slices.Contains(out.results, expected) {
			t.Errorf("missing result: %s", expected)
		}
	}
	if !strings.Contains(out.summary, "admin/ (Status: 200)") || strings.Contains(out.summary, "script.php/") {
		t.Fatalf("incorrect directory classification in tree: %s", out.summary)
	}
	for _, message := range out.messages {
		if strings.Contains(message, "Skipping directory") {
			t.Errorf("file response was probed as a directory: %s", message)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, request := range requests {
		if strings.HasPrefix(request, "/script.php/") || strings.HasPrefix(request, "/.htaccess/") || strings.HasPrefix(request, "/php.ini/") {
			t.Errorf("unexpected directory probe of file: %s", request)
		}
	}
}

func TestRecursiveDottedDirectoryWithSlash(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/v1.0/", "/v1.0/leaf":
			fmt.Fprint(w, "directory or child")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := scan(t, server.URL, "v1.0/\nleaf\n", nil)
	if !slices.Contains(out.results, "v1.0/leaf") {
		t.Fatal("dotted directory found with a slash was skipped")
	}
}
