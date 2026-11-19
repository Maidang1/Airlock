package microvm

import (
	"net"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestSFTPUnregisterRevokesExistingConnection(t *testing.T) {
	srv := NewSFTPServer()
	if err := srv.Register(MountSpec{ID: "project", LocalPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	host, guest := net.Pipe()
	defer guest.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.HandleConn(host) }()
	if _, err := guest.Write([]byte("{\"mount_id\":\"project\"}\n")); err != nil {
		t.Fatal(err)
	}
	client, err := sftp.NewClientPipe(guest, guest)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Stat("/"); err != nil {
		t.Fatal(err)
	}
	srv.Unregister("project")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked SFTP connection remained open")
	}
	if _, err := client.Stat("/"); err == nil {
		t.Fatal("revoked share remained readable")
	}
}
