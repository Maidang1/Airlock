package microvm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/lima-vm/sshocker/pkg/reversesshfs"
)

// MountSpec describes one explicitly approved host directory exposed to the guest.
type MountSpec struct {
	ID            string
	LocalPath     string
	GuestPath     string
	ReadOnly      bool
	ReadonlyNames []string
}

// SFTPServer serves registered host shares over vsock using confined os.Root
// and .git/.hg write protection without exposing host filesystem paths.
type SFTPServer struct {
	mu          sync.RWMutex
	mounts      map[string]MountSpec
	connections map[string]map[net.Conn]struct{}
}

// NewSFTPServer creates an empty confined SFTP server registry.
func NewSFTPServer() *SFTPServer {
	return &SFTPServer{mounts: make(map[string]MountSpec), connections: make(map[string]map[net.Conn]struct{})}
}

// Register validates and adds or replaces a confined mount specification.
func (s *SFTPServer) Register(spec MountSpec) error {
	if spec.ID == "" {
		return errors.New("mount id must not be empty")
	}
	if !filepath.IsAbs(spec.LocalPath) {
		return fmt.Errorf("mount path %q must be absolute", spec.LocalPath)
	}
	clean := filepath.Clean(spec.LocalPath)
	info, err := os.Stat(clean)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("mount path %q is not a directory", clean)
	}
	spec.LocalPath = clean
	spec.ReadonlyNames = normalizeReadonlyNames(spec.ReadonlyNames)

	s.mu.Lock()
	for conn := range s.connections[spec.ID] {
		_ = conn.Close()
	}
	s.mounts[spec.ID] = spec
	s.mu.Unlock()
	return nil
}

func normalizeReadonlyNames(names []string) []string {
	out := make([]string, 0, len(names)+2)
	add := func(name string) {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	add(".git")
	add(".hg")
	for _, name := range names {
		add(name)
	}
	return out
}

// Unregister removes a mount registration by ID.
func (s *SFTPServer) Unregister(id string) {
	s.mu.Lock()
	delete(s.mounts, id)
	for conn := range s.connections[id] {
		_ = conn.Close()
	}
	s.mu.Unlock()
}

// Get looks up a registered mount by ID.
func (s *SFTPServer) Get(id string) (MountSpec, bool) {
	s.mu.RLock()
	spec, ok := s.mounts[id]
	s.mu.RUnlock()
	return spec, ok
}

// HandleConn authenticates the 1-line JSON handshake on conn and serves the
// matching directory via reversesshfs.ServeConfinedVirtual.
func (s *SFTPServer) HandleConn(conn net.Conn) error {
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(conn, 1024)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("sftp handshake read failed: %w", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	var hs SFTPHandshake
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(line)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&hs); err != nil || hs.MountID == "" {
		return errors.New("invalid sftp handshake")
	}
	s.mu.Lock()
	spec, ok := s.mounts[hs.MountID]
	if ok {
		if s.connections[hs.MountID] == nil {
			s.connections[hs.MountID] = make(map[net.Conn]struct{})
		}
		s.connections[hs.MountID][conn] = struct{}{}
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown sftp mount id %q", hs.MountID)
	}
	defer func() {
		s.mu.Lock()
		delete(s.connections[hs.MountID], conn)
		if len(s.connections[hs.MountID]) == 0 {
			delete(s.connections, hs.MountID)
		}
		s.mu.Unlock()
	}()
	var transport io.ReadWriteCloser = conn
	if buffered := reader.Buffered(); buffered > 0 {
		rest := make([]byte, buffered)
		if _, err := io.ReadFull(reader, rest); err != nil {
			return err
		}
		transport = &prefixReadWriteCloser{
			Conn:   conn,
			reader: io.MultiReader(bytes.NewReader(rest), conn),
		}
	}
	return reversesshfs.ServeConfinedVirtual(transport, spec.LocalPath, spec.ReadOnly, spec.ReadonlyNames)
}

type prefixReadWriteCloser struct {
	net.Conn
	reader io.Reader
}

func (p *prefixReadWriteCloser) Read(b []byte) (int, error) {
	return p.reader.Read(b)
}
