package torrent

import (
	"crypto/sha1"
	"errors"
	"fmt"

	"github.com/skmtkytr/stor/bencode"
)

// TorrentFile represents a parsed .torrent file.
type TorrentFile struct {
	Announce     string
	AnnounceList [][]string
	Info         Info
	InfoHash     [20]byte // SHA1 of the bencoded info dict
}

// Info represents the "info" dictionary of a torrent.
type Info struct {
	Name        string
	PieceLength int64
	PieceHashes [][20]byte // Each piece's SHA1 hash
	Length      int64      // Single file mode (0 if multi-file)
	Files       []File     // Multi-file mode (empty if single file)
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

	// Parse info fields
	tf.Info, err = parseInfo(infoDict)
	if err != nil {
		return nil, err
	}

	return tf, nil
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

	// Single file or multi-file
	if length, ok := d["length"].(int64); ok {
		info.Length = length
	} else if filesVal, ok := d["files"]; ok {
		files, err := parseFiles(filesVal)
		if err != nil {
			return info, err
		}
		info.Files = files
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
