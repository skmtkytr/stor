package torrent

import (
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/skmtkytr/stor/bencode"
)

// TorrentFile represents a parsed .torrent file.
type TorrentFile struct {
	Announce     string
	AnnounceList [][]string
	Info         Info
	InfoHash     [20]byte // SHA1 of the bencoded info dict (v1)
	InfoHashV2   [32]byte // BEP 52: SHA256 of the bencoded info dict (v2/hybrid, zero if v1-only)
	MetaVersion  int      // 0 = v1 only, 2 = v2 or hybrid
	WebSeedURLs  []string // BEP 19: HTTP URLs for downloading pieces
}

// Info represents the "info" dictionary of a torrent.
type Info struct {
	Name        string
	PieceLength int64
	PieceHashes [][20]byte     // Each piece's SHA1 hash
	Length      int64          // Single file mode (0 if multi-file)
	Files       []File         // Multi-file mode (empty if single file)
	Private     bool           // BEP 27: if true, disable DHT and PEX
	FileTree    map[string]any // BEP 52: raw v2 file tree (nil if v1-only)
}

// File represents a file entry in a multi-file torrent.
type File struct {
	Length int64
	Path   []string
}

// Parse parses bencoded data into a TorrentFile.
func Parse(data []byte) (*TorrentFile, error) {
	decoded, err := bencode.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("torrent: decode error: %w", err)
	}

	root, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("torrent: root is not a dict")
	}

	tf := &TorrentFile{}

	// announce
	if v, ok := root["announce"]; ok {
		tf.Announce, _ = v.(string)
	}

	// announce-list
	if v, ok := root["announce-list"]; ok {
		tf.AnnounceList, err = parseAnnounceList(v)
		if err != nil {
			return nil, err
		}
	}

	// BEP 19: url-list (webseed)
	if v, ok := root["url-list"]; ok {
		tf.WebSeedURLs = parseURLList(v)
	}

	// info
	infoVal, ok := root["info"]
	if !ok {
		return nil, errors.New("torrent: missing info dict")
	}
	infoDict, ok := infoVal.(map[string]any)
	if !ok {
		return nil, errors.New("torrent: info is not a dict")
	}

	// Compute info hash from the raw bencoded info dict
	infoBencoded, err := bencode.Encode(infoDict)
	if err != nil {
		return nil, fmt.Errorf("torrent: failed to re-encode info: %w", err)
	}
	tf.InfoHash = sha1.Sum(infoBencoded)

	// BEP 52: detect v2/hybrid torrents
	if mv, ok := infoDict["meta version"].(int64); ok && mv == 2 {
		tf.MetaVersion = 2
		tf.InfoHashV2 = sha256.Sum256(infoBencoded)
	}

	// Parse info fields
	tf.Info, err = parseInfo(infoDict)
	if err != nil {
		// BEP 52: v2-only torrents have no "pieces" key — provide a clear error
		if tf.MetaVersion == 2 {
			return nil, fmt.Errorf("torrent: v2-only torrents not yet supported (no v1 piece hashes)")
		}
		return nil, err
	}

	return tf, nil
}

// TrackerURLs returns a deduplicated list of all tracker URLs.
func (tf *TorrentFile) TrackerURLs() []string {
	seen := map[string]bool{}
	var urls []string
	if tf.Announce != "" && !seen[tf.Announce] {
		seen[tf.Announce] = true
		urls = append(urls, tf.Announce)
	}
	for _, tier := range tf.AnnounceList {
		for _, u := range tier {
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	return urls
}

func parseInfo(d map[string]any) (Info, error) {
	var info Info

	name, ok := d["name"].(string)
	if !ok {
		return info, errors.New("torrent: info missing 'name'")
	}
	info.Name = name

	pieceLength, ok := d["piece length"].(int64)
	if !ok {
		return info, errors.New("torrent: info missing 'piece length'")
	}
	if pieceLength <= 0 {
		return info, fmt.Errorf("torrent: invalid piece length: %d", pieceLength)
	}
	info.PieceLength = pieceLength

	piecesStr, ok := d["pieces"].(string)
	if !ok {
		return info, errors.New("torrent: info missing 'pieces'")
	}
	if len(piecesStr)%20 != 0 {
		return info, fmt.Errorf("torrent: pieces length %d is not a multiple of 20", len(piecesStr))
	}

	pieces := []byte(piecesStr)
	numPieces := len(pieces) / 20
	info.PieceHashes = make([][20]byte, numPieces)
	for i := range numPieces {
		copy(info.PieceHashes[i][:], pieces[i*20:(i+1)*20])
	}

	// BEP 27: private flag
	if p, ok := d["private"].(int64); ok && p == 1 {
		info.Private = true
	}

	// Single file or multi-file
	if length, ok := d["length"].(int64); ok {
		if length < 0 {
			return info, fmt.Errorf("torrent: invalid file length: %d", length)
		}
		info.Length = length
	} else if filesVal, ok := d["files"]; ok {
		files, err := parseFiles(filesVal)
		if err != nil {
			return info, err
		}
		info.Files = files
	}

	// BEP 52: store raw file tree for future v2 support
	if ft, ok := d["file tree"].(map[string]any); ok {
		info.FileTree = ft
	}

	return info, nil
}

func parseFiles(v any) ([]File, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("torrent: files is not a list")
	}

	files := make([]File, 0, len(list))
	for _, item := range list {
		d, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("torrent: file entry is not a dict")
		}

		length, ok := d["length"].(int64)
		if !ok {
			return nil, errors.New("torrent: file missing 'length'")
		}
		if length < 0 {
			return nil, fmt.Errorf("torrent: invalid file length: %d", length)
		}

		pathList, ok := d["path"].([]any)
		if !ok {
			return nil, errors.New("torrent: file missing 'path'")
		}

		path := make([]string, 0, len(pathList))
		for _, p := range pathList {
			s, ok := p.(string)
			if !ok {
				return nil, errors.New("torrent: path component is not a string")
			}
			path = append(path, s)
		}

		files = append(files, File{Length: length, Path: path})
	}
	return files, nil
}

func parseAnnounceList(v any) ([][]string, error) {
	tiers, ok := v.([]any)
	if !ok {
		return nil, errors.New("torrent: announce-list is not a list")
	}

	result := make([][]string, 0, len(tiers))
	for _, tier := range tiers {
		trackers, ok := tier.([]any)
		if !ok {
			return nil, errors.New("torrent: announce-list tier is not a list")
		}
		var urls []string
		for _, u := range trackers {
			s, ok := u.(string)
			if !ok {
				return nil, errors.New("torrent: tracker URL is not a string")
			}
			urls = append(urls, s)
		}
		result = append(result, urls)
	}
	return result, nil
}

// parseURLList extracts webseed URLs from url-list (string or list of strings).
func parseURLList(v any) []string {
	switch val := v.(type) {
	case string:
		if val != "" {
			return []string{val}
		}
	case []any:
		urls := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok && s != "" {
				urls = append(urls, s)
			}
		}
		return urls
	}
	return nil
}
