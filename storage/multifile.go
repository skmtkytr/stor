package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/skmtkytr/stor/torrent"
)

// MultiFileWriter handles writing piece data to the correct files
// in a multi-file torrent. Pieces can span multiple files.
// File handles are cached to avoid repeated open/close syscalls.
type MultiFileWriter struct {
	baseDir string
	files   []fileEntry
	total   int64

	mu      sync.Mutex
	handles map[string]*os.File
}

type fileEntry struct {
	path   string // full path on disk
	offset int64  // byte offset in the torrent's virtual data stream
	length int64
}

// NewMultiFileWriter creates a writer for a multi-file torrent.
// For single-file torrents, use os.File directly.
func NewMultiFileWriter(baseDir string, tf *torrent.TorrentFile) (*MultiFileWriter, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("multifile: create base dir: %w", err)
	}

	var entries []fileEntry
	var offset int64

	cleanBase := filepath.Clean(baseDir) + string(os.PathSeparator)
	for _, f := range tf.Info.Files {
		relPath := filepath.Join(f.Path...)
		fullPath := filepath.Clean(filepath.Join(baseDir, relPath))

		// Prevent path traversal
		if !strings.HasPrefix(fullPath, cleanBase) {
			return nil, fmt.Errorf("multifile: path traversal in %q", relPath)
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return nil, fmt.Errorf("multifile: create dir for %s: %w", relPath, err)
		}

		entries = append(entries, fileEntry{
			path:   fullPath,
			offset: offset,
			length: f.Length,
		})
		offset += f.Length
	}

	return &MultiFileWriter{
		baseDir: baseDir,
		files:   entries,
		total:   offset,
		handles: make(map[string]*os.File),
	}, nil
}

// getHandle returns a cached file handle, opening the file if needed.
func (mw *MultiFileWriter) getHandle(path string, create bool) (*os.File, error) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	if f, ok := mw.handles[path]; ok {
		return f, nil
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, err
	}
	mw.handles[path] = f
	return f, nil
}

// WriteAt writes data at the given byte offset in the virtual data stream.
// The data may span multiple files.
func (mw *MultiFileWriter) WriteAt(data []byte, off int64) (int, error) {
	written := 0
	remaining := data

	for _, fe := range mw.files {
		if len(remaining) == 0 {
			break
		}

		fileEnd := fe.offset + fe.length
		if off >= fileEnd {
			continue // this file is before our write position
		}

		// How far into this file we start writing
		fileOff := off - fe.offset
		if fileOff < 0 {
			fileOff = 0
		}

		// How much we can write to this file
		canWrite := int(fe.length - fileOff)
		if canWrite > len(remaining) {
			canWrite = len(remaining)
		}

		f, err := mw.getHandle(fe.path, true)
		if err != nil {
			return written, fmt.Errorf("multifile: open %s: %w", fe.path, err)
		}

		n, err := f.WriteAt(remaining[:canWrite], fileOff)
		if err != nil {
			return written, fmt.Errorf("multifile: write %s: %w", fe.path, err)
		}

		written += n
		remaining = remaining[n:]
		off += int64(n)
	}

	return written, nil
}

// ReadAt reads data at the given byte offset from the virtual data stream.
func (mw *MultiFileWriter) ReadAt(data []byte, off int64) (int, error) {
	read := 0
	buf := data

	for _, fe := range mw.files {
		if len(buf) == 0 {
			break
		}

		fileEnd := fe.offset + fe.length
		if off >= fileEnd {
			continue
		}

		fileOff := off - fe.offset
		if fileOff < 0 {
			fileOff = 0
		}

		canRead := int(fe.length - fileOff)
		if canRead > len(buf) {
			canRead = len(buf)
		}

		f, err := mw.getHandle(fe.path, false)
		if err != nil {
			return read, err
		}

		n, err := f.ReadAt(buf[:canRead], fileOff)
		if err != nil && n == 0 {
			return read, err
		}

		read += n
		buf = buf[n:]
		off += int64(n)
	}

	return read, nil
}

// PreallocateFiles creates and truncates all files to their expected sizes.
func (mw *MultiFileWriter) PreallocateFiles() error {
	for _, fe := range mw.files {
		f, err := mw.getHandle(fe.path, true)
		if err != nil {
			return err
		}
		if err := f.Truncate(fe.length); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all cached file handles.
func (mw *MultiFileWriter) Close() error {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	var firstErr error
	for path, f := range mw.handles {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(mw.handles, path)
	}
	return firstErr
}
