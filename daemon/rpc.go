package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/skmtkytr/stor/engine"
)

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

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	return h.engine.GetStats(), nil
}

func (h *RPCHandler) daemonSetMaxActive(params json.RawMessage) (any, *rpcErr) {
	var p struct {
		MaxActive int `json:"max_active"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.MaxActive < 1 {
		return nil, &rpcErr{Code: -32602, Message: "invalid params: max_active must be >= 1"}
	}
	h.engine.SetMaxActive(p.MaxActive)
	h.cfg.MaxActive = p.MaxActive
	_ = h.cfg.Save()
	return map[string]int{"max_active": p.MaxActive}, nil
}

func (h *RPCHandler) daemonVersion() (any, *rpcErr) {
	return map[string]string{"version": "0.1.0"}, nil
}

func writeRPCError(w http.ResponseWriter, id any, code int, msg string) {
	writeJSON(w, rpcResponse{JSONRPC: "2.0", Error: &rpcErr{Code: code, Message: msg}, ID: id})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
