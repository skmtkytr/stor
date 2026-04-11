package download

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/skmtkytr/stor/torrent"
)

// MultiFileWriter handles writing piece data to the correct files
// in a multi-file torrent. Pieces can span multiple files.
type MultiFileWriter struct {
	baseDir string
	files   []fileEntry
	total   int64
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

	for _, f := range tf.Info.Files {
		relPath := filepath.Join(f.Path...)
		fullPath := filepath.Join(baseDir, relPath)

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
	}, nil
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

		f, err := os.OpenFile(fe.path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return written, fmt.Errorf("multifile: open %s: %w", fe.path, err)
		}

		n, err := f.WriteAt(remaining[:canWrite], fileOff)
		_ = f.Close()
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

		f, err := os.Open(fe.path)
		if err != nil {
			return read, err
		}

		n, err := f.ReadAt(buf[:canRead], fileOff)
		_ = f.Close()
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
		f, err := os.OpenFile(fe.path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		if err := f.Truncate(fe.length); err != nil {
			_ = f.Close()
			return err
		}
		_ = f.Close()
	}
	return nil
}

// Close is a no-op (files are opened/closed per operation).
func (mw *MultiFileWriter) Close() error {
	return nil
}

// IsMultiFile returns whether the torrent has multiple files.
func IsMultiFile(tf *torrent.TorrentFile) bool {
	return len(tf.Info.Files) > 0
}
