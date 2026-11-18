//go:build darwin

package macnative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

const (
	DefaultFileCommandLimit   = 65536
	DefaultFileCommandTimeout = 30 * time.Minute
)

// RecordDiagnosticEvent forwards a native client error message to `cage diagnostic-event`.
func RecordDiagnosticEvent(message string) {
	exe, err := CageBinaryPath()
	if err != nil {
		return
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	payload, err := json.Marshal(map[string]string{
		"source":  "native",
		"message": message,
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "diagnostic-event")
	cmd.Stdin = bytes.NewReader(payload)
	_ = cmd.Run()
}

// RunFileCommand invokes the bundled `cage` helper and parses its JSON stdout response.
func RunFileCommand(ctx context.Context, args []string, replyLimit int, timeout time.Duration) (map[string]any, error) {
	exe, err := CageBinaryPath()
	if err != nil {
		return nil, errors.New("无法找到文件传递组件")
	}
	if replyLimit <= 0 {
		replyLimit = DefaultFileCommandLimit
	}
	if timeout <= 0 {
		timeout = DefaultFileCommandTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, exe, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, max: replyLimit + 1}
	runErr := cmd.Run()
	data := stdout.Bytes()
	if len(data) > replyLimit {
		return nil, errors.New("文件操作未返回有效结果")
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		if runErr != nil {
			RecordDiagnosticEvent(runErr.Error())
		}
		return nil, errors.New("文件操作未返回有效结果")
	}
	if runErr != nil {
		msg, _ := parsed["message"].(string)
		if msg == "" {
			msg = "文件操作失败"
		}
		RecordDiagnosticEvent(msg)
		return nil, errors.New(msg)
	}
	return parsed, nil
}

// RunClipboardCacheCommand runs `cage clipboard-files --json cache <json>`.
func RunClipboardCacheCommand(ctx context.Context, action, cacheID string) (map[string]any, error) {
	req, err := json.Marshal(map[string]string{"action": action, "cache_id": cacheID})
	if err != nil {
		return nil, errors.New("剪贴板缓存请求无效")
	}
	return RunFileCommand(ctx, []string{"clipboard-files", "--json", "cache", string(req)}, 30<<20, 10*time.Second)
}

type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	rem := w.max - w.buf.Len()
	if rem > 0 {
		take := min(rem, len(p))
		w.buf.Write(p[:take])
	}
	return len(p), nil
}

// ContainsCJK reports whether text contains Chinese characters (used to decide
// whether an error message is safe to display verbatim to the user).
func ContainsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// NormalizeConnection returns "local" when empty.
func NormalizeConnection(conn string) string {
	conn = strings.TrimSpace(conn)
	if conn == "" {
		return "local"
	}
	return conn
}
