package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// downloadPieceHTTP downloads a single piece from a webseed URL using HTTP Range.
func downloadPieceHTTP(ctx context.Context, baseURL string, tf *torrent.TorrentFile, pw PieceWork) ([]byte, error) {
	pieceLen := int64(tf.Info.PieceLength)
	offset := int64(pw.Index) * pieceLen

	var buf []byte
	if !storage.IsMultiFile(tf) {
		data, err := httpRange(ctx, baseURL, offset, int64(pw.Length))
		if err != nil {
			return nil, err
		}
		buf = data
	} else {
		buf = make([]byte, pw.Length)
		written := 0
		remaining := int64(pw.Length)
		pos := offset

		var fileOffset int64
		for _, f := range tf.Info.Files {
			fileEnd := fileOffset + f.Length
			if pos >= fileEnd {
				fileOffset = fileEnd
				continue
			}

			localOff := pos - fileOffset
			canRead := f.Length - localOff
			if canRead > remaining {
				canRead = remaining
			}

			url := strings.TrimRight(baseURL, "/") + "/" + strings.Join(f.Path, "/")
			data, err := httpRange(ctx, url, localOff, canRead)
			if err != nil {
				return nil, fmt.Errorf("webseed: %s: %w", f.Path[len(f.Path)-1], err)
			}
			copy(buf[written:], data)
			written += len(data)
			remaining -= canRead
			pos += canRead
			fileOffset = fileEnd

			if remaining <= 0 {
				break
			}
		}
	}

	hash := sha1.Sum(buf)
	if hash != pw.Hash {
		return nil, fmt.Errorf("webseed: piece %d hash mismatch", pw.Index)
	}
	return buf, nil
}

func httpRange(ctx context.Context, url string, offset, length int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("webseed: expected HTTP 206, got %d for %s", resp.StatusCode, url)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, length))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) < length {
		return nil, fmt.Errorf("webseed: short read: got %d, want %d", len(data), length)
	}
	return data, nil
}
