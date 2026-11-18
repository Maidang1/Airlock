package workbench

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// A short native popup capture can hold the serialized RFB reader. Mouse
// bursts during it must arrive in order once capture finishes, not disconnect.
func TestVNCInputBurstSurvivesBriefGuestCapture(t *testing.T) {
	s := testServer(t)
	remote, peer := net.Pipe()
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		s.streamVNC(w, r, remote, remote, func() { _ = remote.Close() })
	}))
	s.origin = server.URL
	defer func() { remote.Close(); peer.Close(); server.Close() }()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"Origin": {server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	want := make([]byte, 0, 120)
	for i := byte(0); i < 20; i++ {
		packet := []byte{5, 0, 0, i, 0, i}
		want = append(want, packet...)
		if err := ws.WriteMessage(websocket.BinaryMessage, packet); err != nil {
			t.Fatal(err)
		}
	}
	// The previous four-message / one-second queue failed this condition.
	select {
	case <-finished:
		t.Fatal("display disconnected during a brief capture")
	case <-time.After(1300 * time.Millisecond):
	}
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("input was dropped or reordered")
	}
	ws.Close()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel transport")
	}
}

type observedVNCTransport struct {
	net.Conn
	readEntered, readExited   chan struct{}
	writeEntered, writeExited chan struct{}
	readOnce, writeOnce       sync.Once
}

func (c *observedVNCTransport) Read(data []byte) (int, error) {
	c.readOnce.Do(func() { close(c.readEntered) })
	n, err := c.Conn.Read(data)
	if err != nil {
		close(c.readExited)
	}
	return n, err
}

func (c *observedVNCTransport) Write(data []byte) (int, error) {
	c.writeOnce.Do(func() { close(c.writeEntered) })
	n, err := c.Conn.Write(data)
	if err != nil {
		close(c.writeExited)
	}
	return n, err
}

func TestVNCClosesBlockedTransportAndWaitsForReader(t *testing.T) {
	for _, condition := range []string{"cancel-read", "cancel-write", "disconnect-read", "disconnect-write"} {
		t.Run(condition, func(t *testing.T) {
			s := testServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			remote, peer := net.Pipe()
			transport := &observedVNCTransport{Conn: remote, readEntered: make(chan struct{}), readExited: make(chan struct{}), writeEntered: make(chan struct{}), writeExited: make(chan struct{})}
			finished := make(chan struct{})
			var closed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				s.streamVNC(w, r.WithContext(ctx), transport, transport, func() {
					closed.Add(1)
					_ = remote.Close()
				})
			}))
			s.origin = server.URL
			defer func() { cancel(); _ = remote.Close(); _ = peer.Close(); server.Close() }()
			ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"Origin": {server.URL}})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			awaitJobSignal(t, transport.readEntered)
			writeBlocked := strings.HasSuffix(condition, "write")
			if writeBlocked {
				if err := ws.WriteMessage(websocket.BinaryMessage, []byte("client input")); err != nil {
					t.Fatal(err)
				}
				// The peer never reads: writing to the remote transport cannot finish.
				awaitJobSignal(t, transport.writeEntered)
			}
			if strings.HasPrefix(condition, "cancel") {
				cancel()
			} else {
				_ = ws.Close()
			}
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("VNC handler survived cancellation or client disconnection with blocked transport I/O")
			}
			if closed.Load() != 1 {
				t.Fatalf("transport closed %d times", closed.Load())
			}
			select {
			case <-transport.readExited:
			default:
				t.Fatal("VNC handler returned before the remote reader exited")
			}
			if writeBlocked {
				select {
				case <-transport.writeExited:
				default:
					t.Fatal("VNC handler returned before the remote writer exited")
				}
			}
		})
	}
}

func TestBrowserVNCUsesInstanceSSHWithHostPortOccupied(t *testing.T) {
	guestAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer guestAPI.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	var vncChannels atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					var target struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if incoming.ChannelType() != "direct-tcpip" || ssh.Unmarshal(incoming.ExtraData(), &target) != nil || target.Host != "127.0.0.1" {
						_ = incoming.Reject(ssh.Prohibited, "unexpected destination")
						continue
					}
					switch target.Port {
					case 18500:
						channel, reqs, err := incoming.Accept()
						if err != nil {
							return
						}
						go ssh.DiscardRequests(reqs)
						remote, err := net.Dial("tcp", guestAPI.Listener.Addr().String())
						if err != nil {
							_ = channel.Close()
							return
						}
						go func() { _, _ = io.Copy(remote, channel); _ = remote.Close(); _ = channel.Close() }()
						go func() { _, _ = io.Copy(channel, remote); _ = remote.Close(); _ = channel.Close() }()
					case 15900:
						vncChannels.Add(1)
						channel, reqs, err := incoming.Accept()
						if err != nil {
							return
						}
						go ssh.DiscardRequests(reqs)
						_, _ = io.WriteString(channel, "RFB 003.008\n")
						buf := make([]byte, 64)
						for {
							if _, err := channel.Read(buf); err != nil {
								_ = channel.Close()
								return
							}
						}
					default:
						_ = incoming.Reject(ssh.Prohibited, "unexpected port")
					}
				}
			}()
		}
	}()

	s := testServer(t)
	s.ready = true
	s.credentials.Token = "isolated-test-token"
	dir := s.instanceDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sshConfig := fmt.Sprintf("Host *\n HostName 127.0.0.1\n Port %d\n User cage\n StrictHostKeyChecking no\n UserKnownHostsFile %s\n", listener.Addr().(*net.TCPAddr).Port, os.DevNull)
	if err := os.WriteFile(filepath.Join(dir, "ssh.config"), []byte(sshConfig), 0600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.vnc(w, r)
	}))
	defer server.Close()
	s.origin = server.URL

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"Origin": {server.URL}})
	if err != nil {
		t.Fatalf("dial browser vnc websocket: %v", err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read rfb banner: %v", err)
	}
	if string(msg) != "RFB 003.008\n" {
		t.Fatalf("unexpected banner: %q", string(msg))
	}
	if vncChannels.Load() == 0 {
		t.Fatal("expected VNC connection to tunnel through instance SSH")
	}
}
