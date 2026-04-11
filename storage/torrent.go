package storage

import "github.com/skmtkytr/stor/torrent"

// TotalSize returns the total size of the torrent in bytes.
func TotalSize(tf *torrent.TorrentFile) int64 {
	if tf.Info.Length > 0 {
		return tf.Info.Length
	}
	var total int64
	for _, f := range tf.Info.Files {
		total += f.Length
	}
	return total
}

// IsMultiFile returns whether the torrent has multiple files.
func IsMultiFile(tf *torrent.TorrentFile) bool {
	return len(tf.Info.Files) > 0
}
