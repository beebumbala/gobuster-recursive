package gobusterdir

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/OJ/gobuster/v3/libgobuster"
)

func (d *GobusterDir) NeedsWordlistReplay() bool {
	return d.options.Recursive
}

func (d *GobusterDir) requestOptions() libgobuster.RequestOptions {
	if d.options.Recursive {
		return libgobuster.RequestOptions{CheckURL: d.inScope}
	}
	return libgobuster.RequestOptions{}
}

// inScope also rejects traversal hidden behind URL encoding. Do not clean a
// suspicious path into an allowed one and then send the original to the server.
func (d *GobusterDir) inScope(target url.URL) bool {
	if !strings.EqualFold(target.Scheme, d.rootURL.Scheme) || !strings.EqualFold(target.Host, d.rootURL.Host) || target.User != nil {
		return false
	}
	path := target.Path
	for range 4 {
		if !strings.HasPrefix(path, d.rootURL.Path) || strings.Contains(path, "\\") || strings.Contains(path, "//") {
			return false
		}
		for _, segment := range strings.Split(path, "/") {
			if segment == "." || segment == ".." {
				return false
			}
		}
		decoded, err := url.PathUnescape(path)
		if err != nil {
			return false
		}
		if decoded == path {
			return true
		}
		path = decoded
	}
	return false
}

func directoryStatus(status int) bool {
	return status >= 200 && status < 300 || status == http.StatusUnauthorized || status == http.StatusForbidden
}

func (d *GobusterDir) accepted(status int, size int64) bool {
	if status == 0 || d.options.ExcludeLengthParsed.Contains(int(size)) {
		return false
	}
	if d.options.StatusCodesBlacklistParsed.Length() > 0 {
		return !d.options.StatusCodesBlacklistParsed.Contains(status)
	}
	return d.options.StatusCodesParsed.Contains(status)
}

func sameDirectoryURL(a, b url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host) && a.Path == b.Path && a.RawQuery == b.RawQuery
}

// directoryRedirect treats Location as evidence for a directory
func directoryRedirect(location, candidate url.URL) bool {
	if location.User != nil || location.Opaque != "" || (location.Scheme != "http" && location.Scheme != "https") {
		return false
	}
	if !strings.EqualFold(location.Hostname(), candidate.Hostname()) {
		return false
	}
	if !strings.EqualFold(location.Host, candidate.Host) && !(defaultHTTPPort(location) && defaultHTTPPort(candidate)) {
		return false
	}
	location.Scheme, location.Host = candidate.Scheme, candidate.Host
	return sameDirectoryURL(location, candidate)
}

func defaultHTTPPort(target url.URL) bool {
	port := target.Port()
	return port == "" || target.Scheme == "http" && port == "80" || target.Scheme == "https" && port == "443"
}

// directoryCandidate separates directory evidence from the recursion depth
// limit, so a directory found at the last level can still appear in the tree.
func (d *GobusterDir) directoryCandidate(requested url.URL, response libgobuster.HTTPResponse) (*url.URL, bool) {
	candidate := requested
	candidate.Path = strings.TrimSuffix(candidate.Path, "/") + "/"
	candidate.RawPath, candidate.Fragment = "", ""
	if !d.inScope(candidate) || candidate.Path == d.currentURL.Path || !strings.HasPrefix(candidate.Path, d.currentURL.Path) {
		return nil, false
	}
	for _, hop := range response.Redirects {
		if !sameDirectoryURL(hop, candidate) {
			return nil, false
		}
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		location := response.Header.Get("Location")
		if location == "" {
			return nil, false
		}
		ref, err := url.Parse(location)
		if err != nil || !directoryRedirect(*response.URL.ResolveReference(ref), candidate) {
			return nil, false
		}
		return &candidate, true
	} else if !directoryStatus(response.StatusCode) {
		return nil, false
	} else if len(response.Redirects) == 0 && !strings.HasSuffix(requested.Path, "/") && path.Ext(requested.Path) != "" {
		return nil, false
	}
	return &candidate, len(response.Redirects) > 0 || strings.HasSuffix(requested.Path, "/")
}

func (d *GobusterDir) discoverDirectory(candidate *url.URL) {
	if candidate == nil || d.options.RecursionDepth > 0 && d.depth >= d.options.RecursionDepth {
		return
	}
	d.discoveryMu.Lock()
	defer d.discoveryMu.Unlock()
	key := candidate.String()
	if !d.seen[key] {
		d.seen[key] = true
		d.nextLevel = append(d.nextLevel, *candidate)
	}
}

// NextPass runs only at the core's completion barrier. The current URL remains
// immutable for all workers until this method prepares the next pass.
func (d *GobusterDir) NextPass(ctx context.Context, progress *libgobuster.Progress) (bool, error) {
	if !d.options.Recursive {
		return false, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if len(d.currentLevel) == 0 {
			if len(d.nextLevel) == 0 {
				return false, nil
			}
			d.currentLevel, d.nextLevel = d.nextLevel, nil
			sort.Slice(d.currentLevel, func(i, j int) bool {
				return d.currentLevel[i].String() < d.currentLevel[j].String()
			})
			d.depth++
		}
		candidate := d.currentLevel[0]
		d.currentLevel = d.currentLevel[1:]
		response, err := d.http.RequestDetailed(ctx, candidate, d.requestOptions())
		if err == nil && (!directoryStatus(response.StatusCode) || !d.accepted(response.StatusCode, response.Length) || !sameDirectoryURL(response.URL, candidate) || len(response.Redirects) != 0) {
			continue
		}
		if err == nil {
			d.currentURL = candidate
			err = d.checkWildcard(ctx, progress)
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err != nil {
			progress.MessageChan <- libgobuster.Message{
				Level:   libgobuster.LevelWarn,
				Message: fmt.Sprintf("Skipping directory %s: %v", candidate.String(), err),
			}
			continue
		}
		progress.SetTarget(candidate.String(), d.depth)
		d.markDirectory(candidate)
		progress.MessageChan <- libgobuster.Message{
			Level:   libgobuster.LevelInfo,
			Message: fmt.Sprintf("Scanning directory %s (depth %d)", candidate.String(), d.depth),
		}
		return true, nil
	}
}
