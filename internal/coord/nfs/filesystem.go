// Package nfs provides an NFS v3 server backed by S3 storage.
package nfs

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

var errReadOnly = errors.New("read-only filesystem")

// S3Filesystem implements billy.Filesystem backed by S3 storage.
type S3Filesystem struct {
	store    *s3.Store
	bucket   string
	prefix   string // Object prefix for this share
	readOnly bool
}

// NewS3Filesystem creates a new S3-backed filesystem for a bucket.
func NewS3Filesystem(store *s3.Store, bucket, prefix string, readOnly bool) *S3Filesystem {
	return &S3Filesystem{
		store:    store,
		bucket:   bucket,
		prefix:   prefix,
		readOnly: readOnly,
	}
}

// normalizePath converts a path to S3 object key format.
func (f *S3Filesystem) normalizePath(path string) string {
	path = filepath.Clean(path)
	path = strings.TrimPrefix(path, "/")
	// filepath.Clean returns "." for empty path
	if path == "." {
		path = ""
	}
	if f.prefix != "" {
		if path == "" {
			return f.prefix
		}
		return f.prefix + "/" + path
	}
	return path
}

// Create creates the named file.
func (f *S3Filesystem) Create(filename string) (billy.File, error) {
	if f.readOnly {
		return nil, errReadOnly
	}
	return f.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
}

// Open opens the named file for reading.
func (f *S3Filesystem) Open(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDONLY, 0)
}

// OpenFile opens the named file with specified flags.
func (f *S3Filesystem) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	key := f.normalizePath(filename)

	// Check if writing
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if writing && f.readOnly {
		return nil, errReadOnly
	}

	// Check if file exists
	meta, err := f.store.HeadObject(f.bucket, key)
	exists := err == nil

	// Handle create/truncate flags
	if flag&os.O_CREATE != 0 && !exists {
		// Create empty file
		_, err := f.store.PutObject(f.bucket, key, bytes.NewReader(nil), 0, "application/octet-stream", nil)
		if err != nil {
			return nil, err
		}
		meta = &s3.ObjectMeta{
			Key:          key,
			Size:         0,
			ContentType:  "application/octet-stream",
			LastModified: time.Now(),
		}
	} else if !exists {
		return nil, os.ErrNotExist
	}

	return &s3File{
		fs:       f,
		name:     filename,
		key:      key,
		flag:     flag,
		perm:     perm,
		size:     meta.Size,
		modTime:  meta.LastModified,
		position: 0,
	}, nil
}

// Stat returns file info.
func (f *S3Filesystem) Stat(filename string) (os.FileInfo, error) {
	key := f.normalizePath(filename)

	// Check if it's a file
	meta, err := f.store.HeadObject(f.bucket, key)
	if err == nil {
		return &s3FileInfo{
			name:    filepath.Base(filename),
			size:    meta.Size,
			mode:    0644,
			modTime: meta.LastModified,
			isDir:   false,
		}, nil
	}

	// Check if it's a directory (has children)
	objects, _, _, err := f.store.ListObjects(f.bucket, key+"/", "", 1)
	if err != nil {
		return nil, os.ErrNotExist
	}
	if len(objects) > 0 || key == "" || key == f.prefix {
		return &s3FileInfo{
			name:    filepath.Base(filename),
			size:    0,
			mode:    0755 | os.ModeDir,
			modTime: time.Now(),
			isDir:   true,
		}, nil
	}

	return nil, os.ErrNotExist
}

// Rename renames a file.
func (f *S3Filesystem) Rename(oldpath, newpath string) error {
	if f.readOnly {
		return errReadOnly
	}

	oldKey := f.normalizePath(oldpath)
	newKey := f.normalizePath(newpath)

	// Get the object
	reader, meta, err := f.store.GetObject(f.bucket, oldKey)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	// Copy to new location
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	_, err = f.store.PutObject(f.bucket, newKey, bytes.NewReader(data), int64(len(data)), meta.ContentType, meta.Metadata)
	if err != nil {
		return err
	}

	// Delete old
	return f.store.DeleteObject(f.bucket, oldKey)
}

// Remove removes a file.
func (f *S3Filesystem) Remove(filename string) error {
	if f.readOnly {
		return errReadOnly
	}
	key := f.normalizePath(filename)
	return f.store.DeleteObject(f.bucket, key)
}

// Join joins path elements.
func (f *S3Filesystem) Join(elem ...string) string {
	return filepath.Join(elem...)
}

// TempFile creates a temporary file.
func (f *S3Filesystem) TempFile(dir, prefix string) (billy.File, error) {
	if f.readOnly {
		return nil, errReadOnly
	}
	name := filepath.Join(dir, prefix+time.Now().Format("20060102150405.000000"))
	return f.Create(name)
}

// ReadDir reads a directory.
func (f *S3Filesystem) ReadDir(path string) ([]os.FileInfo, error) {
	prefix := f.normalizePath(path)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	objects, _, _, err := f.store.ListObjects(f.bucket, prefix, "", 10000)
	if err != nil {
		return nil, err
	}

	// Group by immediate child (file or directory)
	seen := make(map[string]bool)
	var infos []os.FileInfo

	for _, obj := range objects {
		// Get the path relative to prefix
		relPath := strings.TrimPrefix(obj.Key, prefix)
		if relPath == "" {
			continue
		}

		// Check if this is a direct child or nested
		parts := strings.SplitN(relPath, "/", 2)
		name := parts[0]

		if seen[name] {
			continue
		}
		seen[name] = true

		if len(parts) > 1 {
			// This is a directory
			infos = append(infos, &s3FileInfo{
				name:    name,
				size:    0,
				mode:    0755 | os.ModeDir,
				modTime: obj.LastModified,
				isDir:   true,
			})
		} else {
			// This is a file
			infos = append(infos, &s3FileInfo{
				name:    name,
				size:    obj.Size,
				mode:    0644,
				modTime: obj.LastModified,
				isDir:   false,
			})
		}
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name() < infos[j].Name()
	})

	return infos, nil
}

// MkdirAll creates a directory and all parents.
func (f *S3Filesystem) MkdirAll(path string, perm os.FileMode) error {
	// S3 doesn't have real directories - they're implied by object prefixes
	// We can optionally create a placeholder object
	return nil
}

// Symlink creates a symbolic link (not supported for S3).
func (f *S3Filesystem) Symlink(target, link string) error {
	return errors.New("symlinks not supported")
}

// Readlink reads a symbolic link (not supported for S3).
func (f *S3Filesystem) Readlink(link string) (string, error) {
	return "", errors.New("symlinks not supported")
}

// Lstat returns file info (same as Stat for S3).
func (f *S3Filesystem) Lstat(filename string) (os.FileInfo, error) {
	return f.Stat(filename)
}

// Chroot returns a new filesystem rooted at path.
func (f *S3Filesystem) Chroot(path string) (billy.Filesystem, error) {
	newPrefix := f.normalizePath(path)
	return NewS3Filesystem(f.store, f.bucket, newPrefix, f.readOnly), nil
}

// Root returns the root path.
func (f *S3Filesystem) Root() string {
	return "/"
}

// --- s3File implementation ---

type s3File struct {
	fs       *S3Filesystem
	name     string
	key      string
	flag     int
	perm     os.FileMode
	size     int64
	modTime  time.Time
	position int64
	buffer   *bytes.Buffer // For writes
	closed   bool
}

func (f *s3File) Name() string {
	return f.name
}

func (f *s3File) Read(p []byte) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.position >= f.size {
		return 0, io.EOF
	}

	reader, _, err := f.fs.store.GetObject(f.fs.bucket, f.key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = reader.Close() }()

	// Skip to position
	if f.position > 0 {
		if _, err := io.CopyN(io.Discard, reader, f.position); err != nil {
			return 0, err
		}
	}

	n, err := reader.Read(p)
	f.position += int64(n)
	return n, err
}

func (f *s3File) ReadAt(p []byte, off int64) (int, error) {
	f.position = off
	return f.Read(p)
}

func (f *s3File) Write(p []byte) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND) == 0 {
		return 0, errors.New("file not open for writing")
	}

	if f.buffer == nil {
		f.buffer = new(bytes.Buffer)
		// If appending, read existing content first
		if f.flag&os.O_APPEND != 0 && f.size > 0 {
			reader, _, err := f.fs.store.GetObject(f.fs.bucket, f.key)
			if err == nil {
				_, _ = io.Copy(f.buffer, reader)
				_ = reader.Close()
			}
		}
	}

	return f.buffer.Write(p)
}

func (f *s3File) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.position = offset
	case io.SeekCurrent:
		f.position += offset
	case io.SeekEnd:
		f.position = f.size + offset
	}
	return f.position, nil
}

func (f *s3File) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true

	// If we have a write buffer, flush it to S3
	if f.buffer != nil && f.buffer.Len() > 0 {
		data := f.buffer.Bytes()
		_, err := f.fs.store.PutObject(f.fs.bucket, f.key, bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
		return err
	}
	return nil
}

func (f *s3File) Lock() error {
	return nil // No-op
}

func (f *s3File) Unlock() error {
	return nil // No-op
}

func (f *s3File) Truncate(size int64) error {
	if f.fs.readOnly {
		return errReadOnly
	}
	if size == 0 {
		_, err := f.fs.store.PutObject(f.fs.bucket, f.key, bytes.NewReader(nil), 0, "application/octet-stream", nil)
		return err
	}
	// For non-zero truncate, read existing, truncate, and write back
	reader, _, err := f.fs.store.GetObject(f.fs.bucket, f.key)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(reader, size))
	_ = reader.Close()
	if err != nil {
		return err
	}

	// Pad with zeros if needed
	if int64(len(data)) < size {
		data = append(data, make([]byte, size-int64(len(data)))...)
	}

	_, err = f.fs.store.PutObject(f.fs.bucket, f.key, bytes.NewReader(data), size, "application/octet-stream", nil)
	return err
}

// --- s3FileInfo implementation ---

type s3FileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (fi *s3FileInfo) Name() string       { return fi.name }
func (fi *s3FileInfo) Size() int64        { return fi.size }
func (fi *s3FileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *s3FileInfo) ModTime() time.Time { return fi.modTime }
func (fi *s3FileInfo) IsDir() bool        { return fi.isDir }
func (fi *s3FileInfo) Sys() interface{}   { return nil }

// Ensure S3Filesystem implements billy.Filesystem
var _ billy.Filesystem = (*S3Filesystem)(nil)
var _ billy.File = (*s3File)(nil)
var _ fs.FileInfo = (*s3FileInfo)(nil)
