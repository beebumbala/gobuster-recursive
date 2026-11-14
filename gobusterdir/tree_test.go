package gobusterdir

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/OJ/gobuster/v3/libgobuster"
)

func treePlugin(t *testing.T) *GobusterDir {
	t.Helper()
	root, err := url.Parse("https://example.com/base/")
	if err != nil {
		t.Fatal(err)
	}
	return &GobusterDir{options: NewOptions(), rootURL: *root, currentURL: *root}
}

func addTreeFinding(d *GobusterDir, path string, status int, size int64, directory bool) {
	target := d.rootURL
	target.Path += path
	d.recordFinding(target, libgobuster.HTTPResponse{StatusCode: status, Length: size}, directory)
}

func treeText(t *testing.T, d *GobusterDir) string {
	t.Helper()
	var output strings.Builder
	if err := d.WriteSummary(&output); err != nil {
		t.Fatal(err)
	}
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
}

func TestTreeDirectoriesThenOKThenOtherResults(t *testing.T) {
	t.Parallel()
	d := treePlugin(t)
	addTreeFinding(d, "a403", 403, 320, false)
	addTreeFinding(d, "z200", 200, 273, false)
	addTreeFinding(d, "zdir/child", 200, 12, false)
	addTreeFinding(d, "zdir", 301, 361, true)
	addTreeFinding(d, "adir/forbidden", 403, 55, false)
	addTreeFinding(d, "adir/ok", 200, 0, false)
	addTreeFinding(d, "adir/nested", 301, 45, true)
	addTreeFinding(d, "adir", 403, 100, true)
	addTreeFinding(d, "a200", 200, 52, false)
	want := "\nFound paths:\nhttps://example.com/base/\n" +
		"├── adir/ (Status: 403) [Size: 100]\n" +
		"│   ├── nested/ (Status: 301) [Size: 45]\n" +
		"│   ├── ok (Status: 200) [Size: 0]\n" +
		"│   └── forbidden (Status: 403) [Size: 55]\n" +
		"├── zdir/ (Status: 301) [Size: 361]\n" +
		"│   └── child (Status: 200) [Size: 12]\n" +
		"├── a200 (Status: 200) [Size: 52]\n" +
		"├── z200 (Status: 200) [Size: 273]\n" +
		"└── a403 (Status: 403) [Size: 320]\n"
	if got := treeText(t, d); got != want {
		t.Fatalf("unexpected tree:\n%s\nwant:\n%s", got, want)
	}
}

func TestTreeUnknownParentsAndDuplicatePaths(t *testing.T) {
	t.Parallel()
	d := treePlugin(t)
	addTreeFinding(d, ".git/HEAD", 200, 23, false)
	addTreeFinding(d, "admin/", 200, 900, true)
	addTreeFinding(d, "admin", 301, 0, true)
	addTreeFinding(d, "admin/", 200, 900, true)
	var workers sync.WaitGroup
	for range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			addTreeFinding(d, "api.php", 200, 52, false)
		}()
	}
	workers.Wait()
	output := treeText(t, d)
	for _, expected := range []string{".git/ (Status: ?) [Size: ?]", "HEAD (Status: 200) [Size: 23]", "admin/ (Status: 301) [Size: 0]", "api.php (Status: 200) [Size: 52]"} {
		if strings.Count(output, expected) != 1 {
			t.Errorf("wanted one %q in %s", expected, output)
		}
	}
	if strings.Contains(output, "900") {
		t.Fatal("slash probe replaced the original discovery metadata")
	}
}

func TestTreeDisplayFlagsKeepSortingByRawStatus(t *testing.T) {
	t.Parallel()
	d := treePlugin(t)
	d.options.NoStatus, d.options.HideLength, d.options.Expanded = true, true, true
	addTreeFinding(d, "a403", 403, 320, false)
	addTreeFinding(d, "z200", 200, 52, false)
	output := treeText(t, d)
	if strings.Contains(output, "Status:") || strings.Contains(output, "Size:") {
		t.Fatalf("display flags ignored: %s", output)
	}
	if strings.Index(output, "z200") > strings.Index(output, "a403") {
		t.Fatalf("lost raw status ordering: %s", output)
	}
	if !strings.Contains(output, "├── z200\n└── a403\n") {
		t.Fatalf("expanded option corrupted tree labels: %s", output)
	}
}

func TestTreeEmptyAndRootFinding(t *testing.T) {
	t.Parallel()
	d := treePlugin(t)
	if !strings.Contains(treeText(t, d), "(no matching paths found)") {
		t.Fatal("missing empty tree message")
	}
	addTreeFinding(d, "", 200, 123, true)
	output := treeText(t, d)
	if !strings.Contains(output, "https://example.com/base/ (Status: 200) [Size: 123]") || strings.Contains(output, "no matching") {
		t.Fatalf("root finding lost: %s", output)
	}
}
