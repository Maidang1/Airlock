package microvm

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

func TestSFTPServerConfinementAndGitGuard(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()

	secretFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("host-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".git", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git", "config"), []byte("[core]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretFile, filepath.Join(projectDir, "escape-link")); err != nil {
		t.Fatal(err)
	}

	srv := NewSFTPServer()
	if err := srv.Register(MountSpec{
		ID:        "project",
		LocalPath: projectDir,
		GuestPath: "/workspace",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.HandleConn(serverConn)
	}()

	hs, _ := json.Marshal(SFTPHandshake{MountID: "project"})
	if _, err := clientConn.Write(append(hs, '\n')); err != nil {
		t.Fatal(err)
	}

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("sftp.NewClientPipe: %v", err)
	}

	// 1. Virtual root hides the host path.
	cwd, err := client.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if cwd != "/" {
		t.Fatalf("expected virtual root '/', got %q", cwd)
	}

	// 2. Normal file read & write succeed inside project dir.
	f, err := client.Open("/hello.txt")
	if err != nil {
		t.Fatalf("Open /hello.txt: %v", err)
	}
	got, _ := io.ReadAll(f)
	_ = f.Close()
	if string(got) != "hello" {
		t.Fatalf("unexpected content %q", string(got))
	}
	wf, err := client.Create("/created.txt")
	if err != nil {
		t.Fatalf("Create /created.txt: %v", err)
	}
	_, _ = wf.Write([]byte("written-by-guest"))
	_ = wf.Close()
	hostCreated, err := os.ReadFile(filepath.Join(projectDir, "created.txt"))
	if err != nil || string(hostCreated) != "written-by-guest" {
		t.Fatalf("host file mismatch: %v %q", err, string(hostCreated))
	}

	// 3. .git is read-only: reading works, writing or creating hooks fails.
	gf, err := client.Open("/.git/config")
	if err != nil {
		t.Fatalf("expected read of /.git/config to succeed: %v", err)
	}
	_ = gf.Close()
	if _, err := client.OpenFile("/.git/config", os.O_WRONLY|os.O_TRUNC); err == nil {
		t.Fatal("expected write to /.git/config to be denied")
	}
	if _, err := client.Create("/.git/hooks/pre-commit"); err == nil {
		t.Fatal("expected write to /.git/hooks/pre-commit to be denied")
	}

	// 4. Symlink pointing outside projectDir cannot escape os.Root.
	if ef, err := client.Open("/escape-link"); err == nil {
		_ = ef.Close()
		t.Fatal("expected symlink escape outside projectDir to be denied")
	}

	_ = client.Close()
	_ = clientConn.Close()
	if err := <-errCh; err != nil && err != io.EOF {
		t.Fatalf("HandleConn returned unexpected error: %v", err)
	}
}

func TestSFTPServerRejectsUnknownMountAndBadHandshake(t *testing.T) {
	srv := NewSFTPServer()

	for _, payload := range []string{
		"{\"mount_id\":\"nonexistent\"}\n",
		"{\"bad\":\"json\"}\n",
		"not-json\n",
	} {
		serverConn, clientConn := net.Pipe()
		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.HandleConn(serverConn)
		}()
		_, _ = clientConn.Write([]byte(payload))
		err := <-errCh
		_ = clientConn.Close()
		if err == nil {
			t.Fatalf("expected HandleConn to reject payload %q", payload)
		}
	}
}
