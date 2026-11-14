package gobusterdir

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/OJ/gobuster/v3/libgobuster"
)

type foundMetadata struct {
	statusCode int
	size       int64
	slash      bool
}

type foundNode struct {
	name      string
	directory bool
	metadata  *foundMetadata
	children  map[string]*foundNode
}

// findingNode is called with treeMu held. The tree uses requested paths, not
// padded display strings or redirect destinations outside the scan scope.
func (d *GobusterDir) findingNode(target url.URL) *foundNode {
	if d.tree == nil {
		d.tree = &foundNode{}
	}
	relative := strings.TrimPrefix(target.Path, d.rootURL.Path)
	relative = strings.Trim(relative, "/")
	node := d.tree
	if relative == "" {
		return node
	}
	for _, name := range strings.Split(relative, "/") {
		node.directory = true
		if node.children == nil {
			node.children = make(map[string]*foundNode)
		}
		child, ok := node.children[name]
		if !ok {
			child = &foundNode{name: name}
			node.children[name] = child
		}
		node = child
	}
	return node
}

func (d *GobusterDir) recordFinding(target url.URL, response libgobuster.HTTPResponse, directory bool) {
	d.treeMu.Lock()
	defer d.treeMu.Unlock()
	node := d.findingNode(target)
	slash := strings.HasSuffix(target.Path, "/")
	// Merge duplicate words and /dir versus /dir/. Prefer the discovered /dir
	// response when both exist, retaining its original redirect code and size.
	if node.metadata == nil || node.metadata.slash && !slash {
		node.metadata = &foundMetadata{statusCode: response.StatusCode, size: response.Length, slash: slash}
	}
	node.directory = node.directory || directory
}

func (d *GobusterDir) markDirectory(target url.URL) {
	d.treeMu.Lock()
	defer d.treeMu.Unlock()
	d.findingNode(target).directory = true
}

func (n *foundNode) group() int {
	if n.directory {
		return 0
	}
	if n.metadata != nil && n.metadata.statusCode == http.StatusOK {
		return 1
	}
	return 2
}

func (d *GobusterDir) treeLabel(node *foundNode) (string, error) {
	name := node.name
	if node.directory {
		name += "/"
	}
	if node.metadata == nil {
		// An intermediate parent is a structural node, not an HTTP finding.
		if !d.options.NoStatus {
			name += " (Status: ?)"
		}
		if !d.options.HideLength {
			name += " [Size: ?]"
		}
		return name + "\n", nil
	}
	result := Result{Path: name, StatusCode: node.metadata.statusCode, Size: node.metadata.size}
	if d.options.NoStatus {
		result.StatusCode = -1
	}
	if d.options.HideLength {
		result.Size = -1
	}
	return result.ResultToString()
}

func (d *GobusterDir) writeTree(w io.Writer, parent *foundNode, prefix string) error {
	children := make([]*foundNode, 0, len(parent.children))
	for _, child := range parent.children {
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool {
		if a, b := children[i].group(), children[j].group(); a != b {
			return a < b
		}
		return children[i].name < children[j].name
	})
	for i, child := range children {
		branch, continuation := "├── ", "│   "
		if i == len(children)-1 {
			branch, continuation = "└── ", "    "
		}
		label, err := d.treeLabel(child)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, prefix, branch, label); err != nil {
			return err
		}
		if err := d.writeTree(w, child, prefix+continuation); err != nil {
			return err
		}
	}
	return nil
}

// WriteSummary prints findings once the scan and its output workers finish.
// It performs no requests and retains raw metadata even when display flags hide it.
func (d *GobusterDir) WriteSummary(w io.Writer) error {
	d.treeMu.Lock()
	defer d.treeMu.Unlock()
	if _, err := fmt.Fprintln(w, "\nFound paths:"); err != nil {
		return err
	}
	rootLabel := d.rootURL.String() + "\n"
	if d.tree != nil && d.tree.metadata != nil {
		label, err := d.treeLabel(&foundNode{name: d.rootURL.String(), metadata: d.tree.metadata})
		if err != nil {
			return err
		}
		rootLabel = label
	}
	if _, err := io.WriteString(w, rootLabel); err != nil {
		return err
	}
	if d.tree == nil || len(d.tree.children) == 0 {
		if d.tree != nil && d.tree.metadata != nil {
			return nil
		}
		_, err := fmt.Fprintln(w, "(no matching paths found)")
		return err
	}
	return d.writeTree(w, d.tree, "")
}
