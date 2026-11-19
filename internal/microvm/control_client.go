package microvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// DialFunc opens a connection to a guest vsock port.
type DialFunc func(ctx context.Context, port uint32) (net.Conn, error)

// GuestClient talks to airlock-init over PortControl, PortPTY, and PortBrowser.
type GuestClient struct {
	dial       DialFunc
	httpClient *http.Client
}

// NewGuestClient wraps a vsock DialFunc with HTTP/JSON and stream helpers.
func NewGuestClient(dial DialFunc) *GuestClient {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if dial == nil {
				return nil, errors.New("nil guest dialer")
			}
			return dial(ctx, PortControl)
		},
		DisableKeepAlives: true,
	}
	return &GuestClient{
		dial: dial,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   25 * time.Second,
		},
	}
}

func (c *GuestClient) doJSON(ctx context.Context, method, path string, reqBody, respOut any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://airlock.vsock"+path, bodyReader)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errBody) == nil && errBody.Error != "" {
			return fmt.Errorf("guest %s %s failed (HTTP %d): %s", method, path, resp.StatusCode, errBody.Error)
		}
		return fmt.Errorf("guest %s %s failed (HTTP %d)", method, path, resp.StatusCode)
	}
	if respOut == nil {
		return nil
	}
	return json.Unmarshal(data, respOut)
}

// Health fetches the guest init status and memory telemetry.
func (c *GuestClient) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	err := c.doJSON(ctx, http.MethodGet, "/health", nil, &out)
	return out, err
}

// WaitReady polls Health with bounded backoff until airlock-init reports ready.
func (c *GuestClient) WaitReady(ctx context.Context) (HealthResponse, error) {
	backoffs := []time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond}
	idx := 0
	var lastErr error
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		h, err := c.Health(checkCtx)
		cancel()
		if err == nil && h.Ready {
			return h, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("guest init reported not ready")
		}
		delay := backoffs[idx]
		if idx < len(backoffs)-1 {
			idx++
		}
		select {
		case <-ctx.Done():
			return HealthResponse{}, fmt.Errorf("timed out waiting for guest init: %w (last error: %v)", ctx.Err(), lastErr)
		case <-time.After(delay):
		}
	}
}

// Bootstrap pushes randomized identity, timezone, locale, and network config.
func (c *GuestClient) Bootstrap(ctx context.Context, cfg BootstrapConfig) error {
	return c.doJSON(ctx, http.MethodPost, "/bootstrap", cfg, nil)
}

// Probe executes a native boundary or network diagnostic check inside the guest.
func (c *GuestClient) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	var out ProbeResult
	err := c.doJSON(ctx, http.MethodPost, "/probe", req, &out)
	return out, err
}

// Mount asks the guest to mount a registered vsock:1003 SFTP share.
func (c *GuestClient) Mount(ctx context.Context, req MountRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/mounts/add", req, nil)
}

// Unmount asks the guest to unmount a previously mounted project directory.
func (c *GuestClient) Unmount(ctx context.Context, req UnmountRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/mounts/remove", req, nil)
}

// StartBrowser starts the isolated browser session inside the guest.
func (c *GuestClient) StartBrowser(ctx context.Context, req BrowserStartRequest) (BrowserStatusResponse, error) {
	var out BrowserStatusResponse
	err := c.doJSON(ctx, http.MethodPost, "/browser/start", req, &out)
	return out, err
}

// StopBrowser stops the isolated browser and display services to reclaim RAM.
func (c *GuestClient) StopBrowser(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/browser/stop", struct{}{}, nil)
}

// BrowserStatus queries whether the isolated browser is running inside the guest.
func (c *GuestClient) BrowserStatus(ctx context.Context) (BrowserStatusResponse, error) {
	var out BrowserStatusResponse
	err := c.doJSON(ctx, http.MethodGet, "/browser/status", nil, &out)
	return out, err
}

// OpenPTY opens an interactive PTY stream over PortPTY and sends PTYMsgOpen.
func (c *GuestClient) OpenPTY(ctx context.Context, req PTYOpenRequest) (net.Conn, error) {
	if c.dial == nil {
		return nil, errors.New("nil guest dialer")
	}
	conn, err := c.dial(ctx, PortPTY)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := WritePTYFrame(conn, PTYMsgOpen, payload); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// DialBrowserChannel opens a PortBrowser tunnel for "vnc" or "cdp".
func (c *GuestClient) DialBrowserChannel(ctx context.Context, channel string) (net.Conn, error) {
	if c.dial == nil {
		return nil, errors.New("nil guest dialer")
	}
	if channel != "vnc" && channel != "cdp" {
		return nil, fmt.Errorf("unsupported browser channel %q", channel)
	}
	conn, err := c.dial(ctx, PortBrowser)
	if err != nil {
		return nil, err
	}
	if err := writeFull(conn, []byte(channel+"\n")); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
