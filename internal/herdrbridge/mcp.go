package herdrbridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const defaultMCPProtocolVersion = "2025-03-26"

// Server exposes the restricted Herdr bridge over authenticated HTTP MCP endpoints.
type Server struct {
	manager *Manager
	token   string
	mux     *http.ServeMux
}

// NewServer constructs an authenticated HTTP handler for /mcp and /health.
func NewServer(manager *Manager, token string) *Server {
	s := &Server{
		manager: manager,
		token:   token,
		mux:     http.NewServeMux(),
	}
	s.mux.HandleFunc("POST /mcp", s.handleMCP)
	s.mux.HandleFunc("DELETE /mcp", s.handleMCPDelete)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	provided := strings.TrimPrefix(auth, prefix)
	return len(provided) > 0 && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.manager.ListAgents(r.Context()))
}

func (s *Server) handleMCPDelete(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32700, Message: "request body too large or unreadable"},
		})
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		writeJSON(w, http.StatusBadRequest, rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32600, Message: "empty JSON-RPC payload"},
		})
		return
	}

	if trimmed[0] == '[' {
		var reqs []rpcRequest
		if err := json.Unmarshal(trimmed, &reqs); err != nil || len(reqs) == 0 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "invalid JSON-RPC batch"},
			})
			return
		}
		responses := make([]rpcResponse, 0, len(reqs))
		for _, req := range reqs {
			if resp, ok := s.dispatchRPC(r.Context(), req); ok {
				responses = append(responses, resp)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, responses)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32700, Message: "invalid JSON-RPC request"},
		})
		return
	}
	resp, ok := s.dispatchRPC(r.Context(), req)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatchRPC(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      normalizedID(req.ID),
			Error:   &rpcError{Code: -32600, Message: "invalid JSON-RPC 2.0 request"},
		}, true
	}
	if len(req.ID) == 0 || bytes.Equal(bytes.TrimSpace(req.ID), []byte("null")) {
		// Notification: acknowledge without JSON-RPC response.
		return rpcResponse{}, false
	}

	id := normalizedID(req.ID)
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		version := strings.TrimSpace(params.ProtocolVersion)
		if version == "" {
			version = defaultMCPProtocolVersion
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result: map[string]any{
				"protocolVersion": version,
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
				"serverInfo": map[string]any{
					"name":    "cage-herdr-bridge",
					"version": "1.0.0",
				},
				"instructions": "Use this bridge to delegate tasks to whitelisted host CLI agents (such as codex, claude, or opencode) running inside the host herdr-core. Prefer read-only modes for analysis/review and workspace-write modes when edits are intended. Long-running or interactive sessions can be steered over multiple turns with send_agent_message and inspected with read_agent_session.",
			},
		}, true

	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{}}, true

	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": toolDefinitions()}}, true

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      id,
				Error:   &rpcError{Code: -32602, Message: "invalid tools/call params"},
			}, true
		}
		res, err := s.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      id,
				Result:  toolTextResult(map[string]any{"error": err.Error()}, true),
			}, true
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result:  toolTextResult(res, false),
		}, true

	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}, true
	}
}

func (s *Server) callTool(ctx context.Context, name string, rawArgs json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(rawArgs)) == 0 {
		rawArgs = json.RawMessage("{}")
	}
	switch name {
	case "list_host_agents":
		return s.manager.ListAgents(ctx), nil

	case "start_agent_session":
		var req StartRequest
		if err := decodeStrictJSON(rawArgs, &req); err != nil {
			return nil, err
		}
		return s.manager.StartSession(ctx, req)

	case "send_agent_message":
		var req SendRequest
		if err := decodeStrictJSON(rawArgs, &req); err != nil {
			return nil, err
		}
		return s.manager.SendMessage(ctx, req)

	case "read_agent_session":
		var req ReadRequest
		if err := decodeStrictJSON(rawArgs, &req); err != nil {
			return nil, err
		}
		return s.manager.ReadSession(ctx, req)

	case "close_agent_session":
		var req struct {
			SessionID string `json:"session_id"`
		}
		if err := decodeStrictJSON(rawArgs, &req); err != nil {
			return nil, err
		}
		return s.manager.CloseSession(ctx, req.SessionID)

	default:
		return nil, errors.New("unknown tool: " + name)
	}
}

func decodeStrictJSON(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func toolTextResult(payload any, isError bool) map[string]any {
	b, _ := json.MarshalIndent(payload, "", "  ")
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(b)},
		},
		"isError": isError,
	}
}

func normalizedID(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage("null")
	}
	return trimmed
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_host_agents",
			"description": "List whitelisted host CLI agents (such as codex, claude, opencode), their allowed modes, Herdr availability, and active bridge sessions.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			"name":        "start_agent_session",
			"description": "Start a whitelisted host CLI agent inside a persistent Herdr terminal pane for an authorized shared project directory. Interactive modes stay open for multi-turn steering via send_agent_message; non-interactive modes run a single task and exit.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Whitelisted agent profile name from list_host_agents (for example: codex, claude, opencode).",
					},
					"mode": map[string]any{
						"type":        "string",
						"description": "Preset mode defined by the profile (for example: read-only, workspace-write, exec-read-only, exec-write). Defaults to the profile default_mode.",
					},
					"workdir": map[string]any{
						"type":        "string",
						"description": "Project directory path (guest mount path or host project path within an authorized shared project).",
					},
					"initial_prompt": map[string]any{
						"type":        "string",
						"description": "Optional initial task/prompt for interactive sessions, or required task prompt for non-interactive exec modes.",
					},
					"wait_timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Maximum seconds to wait for initial output (1-600).",
					},
				},
				"required":             []string{"agent", "workdir"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "send_agent_message",
			"description": "Send a follow-up prompt or steering instruction to an active interactive Herdr agent session and wait up to the timeout for its status and new output.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session ID returned by start_agent_session.",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "Instruction or follow-up message to send to the running agent.",
					},
					"wait_timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Maximum seconds to wait for this turn before returning current progress (1-600).",
					},
				},
				"required":             []string{"session_id", "message"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "read_agent_session",
			"description": "Inspect the current state (idle, working, waiting_approval, exited) and recent output of a bridge-managed Herdr agent session.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session ID returned by start_agent_session.",
					},
					"lines": map[string]any{
						"type":        "integer",
						"description": "Number of recent terminal lines to read (default 120, max 500).",
					},
					"wait_timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional seconds to wait before reading snapshot (0-600).",
					},
				},
				"required":             []string{"session_id"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "close_agent_session",
			"description": "Terminate and remove a bridge-managed Herdr agent session when work is finished or needs to be aborted.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session ID returned by start_agent_session.",
					},
				},
				"required":             []string{"session_id"},
				"additionalProperties": false,
			},
		},
	}
}
