package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skmtkytr/stor/engine"
)

// validTorrentURL checks that a URL uses http/https and doesn't point to
// private/internal addresses (SSRF prevention).
func validTorrentURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true // non-IP hostname, allow
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
}

// JSON-RPC 2.0 types.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id"`
}

type rpcResponse struct {
	JSONRPC string  `json:"jsonrpc"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
	ID      any     `json:"id"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// RPCHandler handles JSON-RPC 2.0 requests.
type RPCHandler struct {
	mu     sync.Mutex // protects cfg from concurrent HTTP handlers
	engine *engine.Engine
	cfg    *Config
}

// NewRPCHandler creates a new RPC handler.
func NewRPCHandler(eng *engine.Engine, cfg *Config) *RPCHandler {
	return &RPCHandler{engine: eng, cfg: cfg}
}

// ServeHTTP handles POST /api/rpc.
func (h *RPCHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	const maxRPCBodySize = 10 << 20 // 10 MB
	var req rpcRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRPCBodySize)).Decode(&req); err != nil {
		slog.Warn("rpc parse error", "error", err)
		writeRPCError(w, nil, -32700, "parse error")
		return
	}

	if req.JSONRPC != "2.0" {
		slog.Warn("rpc invalid request", "jsonrpc", req.JSONRPC)
		writeRPCError(w, req.ID, -32600, "invalid request: jsonrpc must be 2.0")
		return
	}

	start := time.Now()
	result, rpcError := h.dispatch(req.Method, req.Params)
	duration := time.Since(start)

	if rpcError != nil {
		slog.Warn("rpc error",
			"method", req.Method,
			"code", rpcError.Code,
			"error", rpcError.Message,
			"duration_ms", duration.Milliseconds(),
		)
		writeJSON(w, rpcResponse{JSONRPC: "2.0", Error: rpcError, ID: req.ID})
		return
	}

	slog.Info("rpc call",
		"method", req.Method,
		"duration_ms", duration.Milliseconds(),
	)
	writeJSON(w, rpcResponse{JSONRPC: "2.0", Result: result, ID: req.ID})
}

func (h *RPCHandler) dispatch(method string, params json.RawMessage) (any, *rpcErr) {
	switch method {
	case "torrent.add":
		return h.torrentAdd(params)
	case "torrent.addFile":
		return h.torrentAddFile(params)
	case "torrent.addURL":
		return h.torrentAddURL(params)
	case "torrent.remove":
		return h.torrentRemove(params)
	case "torrent.pause":
		return h.torrentPause(params)
	case "torrent.resume":
		return h.torrentResume(params)
	case "torrent.get":
		return h.torrentGet(params)
	case "torrent.list":
		return h.torrentList()
	case "torrent.queueTop":
		return h.torrentQueueMove(params, "top")
	case "torrent.queueUp":
		return h.torrentQueueMove(params, "up")
	case "torrent.queueDown":
		return h.torrentQueueMove(params, "down")
	case "torrent.queueBottom":
		return h.torrentQueueMove(params, "bottom")
	case "daemon.stats":
		return h.daemonStats()
	case "daemon.setMaxActive":
		return h.daemonSetMaxActive(params)
	case "daemon.setConfig":
		return h.daemonSetConfig(params)
	case "daemon.version":
		return h.daemonVersion()
	default:
		return nil, &rpcErr{Code: -32601, Message: fmt.Sprintf("method not found: %s", method)}
	}
}

func (h *RPCHandler) torrentAdd(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Source == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: source required"}
	}

	id, err := h.engine.AddTorrent(p.Source)
	if err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return map[string]string{"id": id}, nil
}

func (h *RPCHandler) torrentAddFile(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		Data string `json:"data"` // base64 encoded .torrent
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Data == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: data required"}
	}

	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid base64 data"}
	}

	id, err := h.engine.AddTorrentFile(data)
	if err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return map[string]string{"id": id}, nil
}

func (h *RPCHandler) torrentAddURL(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URL == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: url required"}
	}

	if !validTorrentURL(p.URL) {
		return nil, &rpcErr{Code: -32602, Message: "invalid URL: private/internal addresses not allowed"}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(p.URL)
	if err != nil {
		return nil, &rpcErr{Code: -32000, Message: fmt.Sprintf("download failed: %v", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &rpcErr{Code: -32000, Message: fmt.Sprintf("download failed: HTTP %d", resp.StatusCode)}
	}

	// Limit to 10MB
	const maxSize = 10 * 1024 * 1024
	data := make([]byte, 0, 64*1024)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
			if len(data) > maxSize {
				return nil, &rpcErr{Code: -32000, Message: "torrent file too large (>10MB)"}
			}
		}
		if readErr != nil {
			break
		}
	}

	id, err := h.engine.AddTorrentFile(data)
	if err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return map[string]string{"id": id}, nil
}

func (h *RPCHandler) torrentRemove(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		ID          string `json:"id"`
		DeleteFiles bool   `json:"delete_files"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: id required"}
	}

	if err := h.engine.RemoveTorrent(p.ID, p.DeleteFiles); err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return struct{}{}, nil
}

func (h *RPCHandler) torrentPause(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: id required"}
	}

	if err := h.engine.PauseTorrent(p.ID); err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return struct{}{}, nil
}

func (h *RPCHandler) torrentResume(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: id required"}
	}

	if err := h.engine.ResumeTorrent(p.ID); err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return struct{}{}, nil
}

func (h *RPCHandler) torrentGet(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: id required"}
	}

	info, err := h.engine.GetTorrent(p.ID)
	if err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return info, nil
}

func (h *RPCHandler) torrentList() (any, *rpcErr) {
	return h.engine.ListTorrents(), nil
}

func (h *RPCHandler) torrentQueueMove(params json.RawMessage, direction string) (any, *rpcErr) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: id required"}
	}

	var err error
	switch direction {
	case "top":
		err = h.engine.QueueTop(p.ID)
	case "up":
		err = h.engine.QueueUp(p.ID)
	case "down":
		err = h.engine.QueueDown(p.ID)
	case "bottom":
		err = h.engine.QueueBottom(p.ID)
	}
	if err != nil {
		return nil, &rpcErr{Code: -32000, Message: err.Error()}
	}
	return struct{}{}, nil
}

func (h *RPCHandler) daemonStats() (any, *rpcErr) {
	stats := h.engine.GetStats()
	h.mu.Lock()
	stats.Config.LogLevel = h.cfg.LogLevel
	h.mu.Unlock()
	return stats, nil
}

func (h *RPCHandler) daemonSetMaxActive(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		MaxActive int `json:"max_active"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.MaxActive < 1 {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: max_active must be >= 1"}
	}
	h.engine.SetMaxActive(p.MaxActive)
	h.mu.Lock()
	h.cfg.MaxActive = p.MaxActive
	_ = h.cfg.Save()
	h.mu.Unlock()
	return map[string]int{"max_active": p.MaxActive}, nil
}

func (h *RPCHandler) daemonSetConfig(params json.RawMessage) (any, *rpcErr) {
	// Use pointers to distinguish "not set" from zero values
	var p struct {
		DownloadDir *string `json:"download_dir"`
		TmpDir      *string `json:"tmp_dir"`
		MaxActive   *int    `json:"max_active"`
		MaxPeers    *int    `json:"max_peers"`
		MaxPipeline *int    `json:"max_pipeline"`
		DialTimeout *int    `json:"dial_timeout"`
		NumWant     *int    `json:"numwant"`
		LogLevel    *string `json:"log_level"`
		EnableUTP   *bool   `json:"enable_utp"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid params"}
	}

	// Validate directory paths: must be absolute and not contain traversal
	if p.DownloadDir != nil {
		if err := validateDirPath(*p.DownloadDir); err != nil {
			return nil, &rpcErr{Code: -32602, Message: "invalid download_dir: " + err.Error()}
		}
	}
	if p.TmpDir != nil {
		if err := validateDirPath(*p.TmpDir); err != nil {
			return nil, &rpcErr{Code: -32602, Message: "invalid tmp_dir: " + err.Error()}
		}
	}

	// Engine setters are thread-safe; lock only for h.cfg mutations.
	if p.DownloadDir != nil {
		h.engine.SetDownloadDir(*p.DownloadDir)
	}
	if p.TmpDir != nil {
		h.engine.SetTmpDir(*p.TmpDir)
	}
	if p.MaxActive != nil && *p.MaxActive >= 1 {
		h.engine.SetMaxActive(*p.MaxActive)
	}
	if p.MaxPeers != nil && *p.MaxPeers >= 1 {
		h.engine.SetMaxPeers(*p.MaxPeers)
	}
	if p.MaxPipeline != nil && *p.MaxPipeline >= 1 {
		h.engine.SetMaxPipeline(*p.MaxPipeline)
	}
	if p.DialTimeout != nil && *p.DialTimeout >= 1 {
		h.engine.SetDialTimeout(*p.DialTimeout)
	}
	if p.NumWant != nil && *p.NumWant >= 1 {
		h.engine.SetNumWant(*p.NumWant)
	}
	if p.EnableUTP != nil {
		h.engine.SetEnableUTP(*p.EnableUTP)
	}
	if p.LogLevel != nil {
		slog.SetLogLoggerLevel(ParseLogLevel(*p.LogLevel))
	}

	h.mu.Lock()
	if p.DownloadDir != nil {
		h.cfg.DownloadDir = *p.DownloadDir
	}
	if p.TmpDir != nil {
		h.cfg.TmpDir = *p.TmpDir
	}
	if p.MaxActive != nil && *p.MaxActive >= 1 {
		h.cfg.MaxActive = *p.MaxActive
	}
	if p.MaxPeers != nil && *p.MaxPeers >= 1 {
		h.cfg.MaxPeers = *p.MaxPeers
	}
	if p.MaxPipeline != nil && *p.MaxPipeline >= 1 {
		h.cfg.MaxPipeline = *p.MaxPipeline
	}
	if p.DialTimeout != nil && *p.DialTimeout >= 1 {
		h.cfg.DialTimeout = *p.DialTimeout
	}
	if p.NumWant != nil && *p.NumWant >= 1 {
		h.cfg.NumWant = *p.NumWant
	}
	if p.LogLevel != nil {
		h.cfg.LogLevel = *p.LogLevel
	}
	if p.EnableUTP != nil {
		h.cfg.EnableUTP = *p.EnableUTP
	}
	if err := h.cfg.Save(); err != nil {
		slog.Warn("config save failed", "error", err)
	}
	h.mu.Unlock()
	return struct{}{}, nil
}

func (h *RPCHandler) daemonVersion() (any, *rpcErr) {
	return map[string]string{"version": "0.1.0"}, nil
}

// validateDirPath checks that a directory path is absolute, canonical,
// and doesn't contain path traversal components.
func validateDirPath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("must be absolute path")
	}
	// Reject if path contains ".." components
	normalized := strings.ReplaceAll(path, "\\", "/")
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return fmt.Errorf("path contains traversal")
		}
	}
	// If the path exists, resolve symlinks and re-validate the real path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		if resolved != filepath.Clean(path) {
			return fmt.Errorf("path contains symlink (resolves to %s)", resolved)
		}
	}
	return nil
}

func writeRPCError(w http.ResponseWriter, id any, code int, msg string) {
	writeJSON(w, rpcResponse{JSONRPC: "2.0", Error: &rpcErr{Code: code, Message: msg}, ID: id})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
