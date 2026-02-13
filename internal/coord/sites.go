package coord

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

// peerSiteEntry represents a file or directory in a directory listing.
type peerSiteEntry struct {
	Name         string
	IsDir        bool
	Size         int64
	LastModified time.Time
	Expires      *time.Time
}

// peerSiteData holds data for the directory listing template.
type peerSiteData struct {
	Title      string
	Path       string
	ParentPath string
	Entries    []peerSiteEntry
	Truncated  bool
}

// peerIndexEntry represents a peer in the peer index page.
type peerIndexEntry struct {
	Name   string
	Shares []string
}

var dirListingTmpl = template.Must(template.New("dirlist").Funcs(template.FuncMap{
	"urlEncode": url.PathEscape,
	"formatSize": func(size int64) string {
		switch {
		case size >= 1<<30:
			return fmt.Sprintf("%.1f GiB", float64(size)/float64(1<<30))
		case size >= 1<<20:
			return fmt.Sprintf("%.1f MiB", float64(size)/float64(1<<20))
		case size >= 1<<10:
			return fmt.Sprintf("%.1f KiB", float64(size)/float64(1<<10))
		default:
			return fmt.Sprintf("%d B", size)
		}
	},
	"formatTime": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format("02-Jan-2006 15:04")
	},
	"formatExpiry": func(t *time.Time) string {
		if t == nil || t.IsZero() {
			return "-"
		}
		if time.Now().After(*t) {
			return t.Format("02-Jan-2006 15:04") + " (expired)"
		}
		return t.Format("02-Jan-2006 15:04")
	},
}).Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body { background: #0d1117; color: #e6edf3; font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace; font-size: 14px; margin: 0; padding: 20px; }
h1 { color: #58a6ff; font-size: 18px; margin-bottom: 16px; }
a { color: #58a6ff; text-decoration: none; }
a:hover { text-decoration: underline; }
table { border-collapse: collapse; width: 100%; max-width: 1100px; }
th { text-align: left; color: #8b949e; border-bottom: 1px solid #30363d; padding: 8px 16px 8px 0; font-weight: normal; }
td { padding: 4px 16px 4px 0; border-bottom: 1px solid #21262d; }
td.size, th.size { text-align: right; }
.dir { color: #58a6ff; }
.file { color: #e6edf3; }
.expired { color: #f85149; }
.truncated { color: #d29922; margin-top: 12px; font-size: 13px; }
</style>
</head>
<body>
<h1>Index of {{.Path}}</h1>
<table>
<tr><th>Name</th><th class="size">Size</th><th>Last Modified</th><th>Expires</th></tr>
{{if .ParentPath}}<tr><td><a href="{{.ParentPath}}" class="dir">../</a></td><td class="size">-</td><td>-</td><td>-</td></tr>{{end}}
{{range .Entries}}<tr>
<td>{{if .IsDir}}<a href="{{urlEncode .Name}}/" class="dir">{{.Name}}/</a>{{else}}<a href="{{urlEncode .Name}}" class="file">{{.Name}}</a>{{end}}</td>
<td class="size">{{if .IsDir}}-{{else}}{{formatSize .Size}}{{end}}</td>
<td>{{formatTime .LastModified}}</td>
<td>{{formatExpiry .Expires}}</td>
</tr>{{end}}
</table>
{{if .Truncated}}<p class="truncated">Listing truncated to 1000 entries.</p>{{end}}
</body>
</html>
`))

var peerIndexTmpl = template.Must(template.New("peerindex").Funcs(template.FuncMap{
	"urlEncode": url.PathEscape,
}).Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Peer Sites</title>
<style>
body { background: #0d1117; color: #e6edf3; font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace; font-size: 14px; margin: 0; padding: 20px; }
h1 { color: #58a6ff; font-size: 18px; margin-bottom: 16px; }
h2 { color: #e6edf3; font-size: 16px; margin-top: 20px; margin-bottom: 8px; }
a { color: #58a6ff; text-decoration: none; }
a:hover { text-decoration: underline; }
ul { list-style: none; padding-left: 16px; }
li { padding: 4px 0; }
</style>
</head>
<body>
<h1>Peer Sites</h1>
{{range $peer := .}}<h2>{{$peer.Name}}</h2>
<ul>{{range $peer.Shares}}<li><a href="{{urlEncode $peer.Name}}/{{urlEncode .}}/">{{.}}</a></li>{{end}}</ul>
{{end}}
</body>
</html>
`))

// handlePeerSite serves files from peer shares as web pages.
// URL structure: /peers/{peerName}/{shareName}/path/to/file
func (s *Server) handlePeerSite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.fileShareMgr == nil || s.s3Store == nil {
		http.Error(w, "Not available", http.StatusServiceUnavailable)
		return
	}

	// Parse the URL path
	trimmed := strings.TrimPrefix(r.URL.Path, "/peers/")
	trimmed = strings.TrimPrefix(trimmed, "/") // handle double slash

	// /peers/ — peer index
	if trimmed == "" {
		s.handlePeerIndex(w, r)
		return
	}

	parts := strings.SplitN(trimmed, "/", 3)
	peerName := parts[0]

	// /peers/{peerName}/ — share index for this peer
	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "") {
		s.handleShareIndex(w, r, peerName)
		return
	}

	shareName := parts[1]
	filePath := ""
	if len(parts) == 3 {
		filePath = parts[2]
	}

	// Map to internal share name and bucket
	internalName := peerName + "_" + shareName
	share := s.fileShareMgr.Get(internalName)
	if share == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Authorization check
	if !s.canAccessShare(r, share) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	bucketName := s.fileShareMgr.BucketName(internalName)

	// Clean the path to prevent traversal
	filePath = path.Clean("/" + filePath)[1:] // Remove leading slash after clean

	// Serve the file or directory listing
	s.servePeerSiteContent(w, r, bucketName, filePath)
}

// canAccessShare checks if the requester can access a share.
func (s *Server) canAccessShare(r *http.Request, share *s3.FileShare) bool {
	// GuestRead shares are accessible by any mesh peer
	if share.GuestRead {
		return true
	}

	// Non-guest shares require authentication
	ownerID := s.getRequestOwner(r)
	if ownerID == "" {
		return false
	}

	// Owner can always access
	if ownerID == share.Owner {
		return true
	}

	// Check RBAC for bucket-read permission
	bucketName := s.fileShareMgr.BucketName(share.Name)
	if s.s3Authorizer != nil && s.s3Authorizer.Authorize(ownerID, "get", "objects", bucketName, "") {
		return true
	}

	return false
}

// servePeerSiteContent serves a file or directory listing from a share bucket.
func (s *Server) servePeerSiteContent(w http.ResponseWriter, r *http.Request, bucketName, filePath string) {
	// Try serving the exact file first
	if filePath != "" && !strings.HasSuffix(filePath, "/") {
		if s.tryServeFile(w, r, bucketName, filePath) {
			return
		}
	}

	// Strip trailing slash for key lookup
	prefix := strings.TrimSuffix(filePath, "/")

	// Try index.html
	indexKey := "index.html"
	if prefix != "" {
		indexKey = prefix + "/index.html"
	}
	if s.tryServeFile(w, r, bucketName, indexKey) {
		return
	}

	// Show directory listing
	listPrefix := ""
	if prefix != "" {
		listPrefix = prefix + "/"
	}
	s.handleDirectoryListing(w, r, bucketName, listPrefix)
}

// tryServeFile attempts to serve a single file from S3. Returns true if successful.
func (s *Server) tryServeFile(w http.ResponseWriter, r *http.Request, bucketName, key string) bool {
	reader, meta, err := s.s3Store.GetObject(r.Context(), bucketName, key)
	if err != nil {
		return false
	}
	defer func() { _ = reader.Close() }()

	// Skip tombstoned objects
	if meta.IsTombstoned() {
		return false
	}

	// Determine content type
	contentType := meta.ContentType
	if contentType == "" {
		ext := path.Ext(key)
		contentType = mime.TypeByExtension(ext)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Set response headers
	w.Header().Set("Content-Type", contentType)
	if meta.Size > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	}
	if meta.ETag != "" {
		w.Header().Set("ETag", `"`+meta.ETag+`"`)
	}
	if !meta.LastModified.IsZero() {
		w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	}
	w.Header().Set("Cache-Control", "public, max-age=300")

	// HEAD requests return headers only
	if r.Method == http.MethodHead {
		return true
	}

	_, _ = io.Copy(w, reader)
	return true
}

// handleDirectoryListing renders a directory listing for a given prefix.
func (s *Server) handleDirectoryListing(w http.ResponseWriter, r *http.Request, bucketName, prefix string) {
	const maxListObjects = 1000
	objects, _, _, err := s.s3Store.ListObjects(r.Context(), bucketName, prefix, "", maxListObjects)
	if err != nil {
		if errors.Is(err, s3.ErrBucketNotFound) {
			http.Error(w, "Not found", http.StatusNotFound)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	truncated := len(objects) >= maxListObjects

	// Build entries: deduplicate directories, list files
	dirSet := make(map[string]bool)
	var entries []peerSiteEntry

	for _, obj := range objects {
		if obj.IsTombstoned() {
			continue
		}

		// Get relative path from prefix
		relKey := strings.TrimPrefix(obj.Key, prefix)
		if relKey == "" {
			continue
		}

		// Check if this is in a subdirectory
		if idx := strings.Index(relKey, "/"); idx >= 0 {
			dirName := relKey[:idx]
			if !dirSet[dirName] {
				dirSet[dirName] = true
				entries = append(entries, peerSiteEntry{
					Name:  dirName,
					IsDir: true,
				})
			}
		} else {
			entries = append(entries, peerSiteEntry{
				Name:         relKey,
				IsDir:        false,
				Size:         obj.Size,
				LastModified: obj.LastModified,
				Expires:      obj.Expires,
			})
		}
	}

	// Sort: directories first, then alphabetical
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})

	// Always show parent link — "../" works correctly for all directory levels.
	// At share root (/peers/alice/share/), ../ goes to share index.
	// At subdirectory (/peers/alice/share/docs/), ../ goes up one level.
	parentPath := "../"

	displayPath := "/" + prefix
	if prefix == "" {
		displayPath = "/"
	}

	data := peerSiteData{
		Title:      "Index of " + r.URL.Path,
		Path:       displayPath,
		ParentPath: parentPath,
		Entries:    entries,
		Truncated:  truncated,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dirListingTmpl.Execute(w, data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// handlePeerIndex renders a list of all peers with accessible shares.
func (s *Server) handlePeerIndex(w http.ResponseWriter, r *http.Request) {
	shares := s.fileShareMgr.List()

	// Group shares by peer name using owner -> peer name resolution
	peerShares := make(map[string][]string)
	for _, share := range shares {
		if !s.canAccessShare(r, share) {
			continue
		}

		// Resolve peer name from share owner (handles underscores in peer names)
		peerName := s.getPeerName(share.Owner)
		prefix := peerName + "_"
		if !strings.HasPrefix(share.Name, prefix) {
			continue
		}
		shareName := strings.TrimPrefix(share.Name, prefix)
		peerShares[peerName] = append(peerShares[peerName], shareName)
	}

	// Build sorted list
	peers := make([]peerIndexEntry, 0, len(peerShares))
	for name, shares := range peerShares {
		sort.Strings(shares)
		peers = append(peers, peerIndexEntry{Name: name, Shares: shares})
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].Name < peers[j].Name
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := peerIndexTmpl.Execute(w, peers); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// handleShareIndex renders a list of shares for a specific peer.
func (s *Server) handleShareIndex(w http.ResponseWriter, r *http.Request, peerName string) {
	shares := s.fileShareMgr.List()

	prefix := peerName + "_"
	var shareNames []string
	for _, share := range shares {
		if !strings.HasPrefix(share.Name, prefix) {
			continue
		}
		if !s.canAccessShare(r, share) {
			continue
		}
		shareNames = append(shareNames, strings.TrimPrefix(share.Name, prefix))
	}

	if len(shareNames) == 0 {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	sort.Strings(shareNames)
	peers := []peerIndexEntry{{Name: peerName, Shares: shareNames}}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := peerIndexTmpl.Execute(w, peers); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}
