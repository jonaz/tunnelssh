package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/urfave/cli/v2"
)

// newAgent runs the agent command's flag parsing with args and returns the
// agent it built.
func newAgent(t *testing.T, args ...string) *Agent {
	t.Helper()
	var a *Agent
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "master"},
			&cli.StringFlag{Name: "id"},
			&cli.StringFlag{Name: "id-file"},
			&cli.StringFlag{Name: "target"},
		},
		Action: func(c *cli.Context) error {
			a = NewAgentFromContext(c)
			return nil
		},
	}
	if err := app.Run(append([]string{"agent"}, args...)); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestNewAgentFromContext(t *testing.T) {
	a := newAgent(t, "--master", "wss://m", "--id", "a1", "--target", "127.0.0.1:2222")
	if a.Master != "wss://m" || a.ID != "a1" || a.Target != "127.0.0.1:2222" {
		t.Fatalf("agent = %+v", a)
	}
}

func TestIDFile(t *testing.T) {
	// Device tree strings such as serial-number end in a NUL.
	f := filepath.Join(t.TempDir(), "serial-number")
	if err := os.WriteFile(f, []byte("10000000abcdef01\n\x00"), 0o600); err != nil {
		t.Fatal(err)
	}

	if a := newAgent(t, "--id-file", f); a.ID != "10000000abcdef01" {
		t.Errorf("id = %q, want it trimmed of whitespace and NULs", a.ID)
	}
	if a := newAgent(t, "--id", "explicit", "--id-file", f); a.ID != "explicit" {
		t.Errorf("id = %q, --id should win over --id-file", a.ID)
	}
}

func TestGetLocalIps(t *testing.T) {
	ips := getLocalIps()
	for _, ip := range ips {
		if _, _, err := net.ParseCIDR(ip); err != nil {
			t.Errorf("%q is not a CIDR address: %s", ip, err)
		}
	}
	if !strings.Contains(strings.Join(ips, ","), "127.0.0.1/") {
		t.Errorf("loopback missing from %q", ips)
	}
}

// fakeMaster accepts one agent and hands its yamux session to the test.
func fakeMaster(t *testing.T, ctx context.Context, sessions chan<- *yamux.Session, headers chan<- http.Header) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/websocket-v1" || r.URL.Query().Get("id") != "a1" {
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		headers <- r.Header.Clone()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		sess, err := yamux.Server(websocket.NetConn(ctx, c, websocket.MessageBinary), nil)
		if err != nil {
			return
		}
		sessions <- sess
		<-sess.CloseChan()
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}

func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestRunForwardsStreamsToTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := make(chan *yamux.Session, 1)
	headers := make(chan http.Header, 1)
	a := &Agent{Master: fakeMaster(t, ctx, sessions, headers), ID: "a1", Target: echoServer(t)}

	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	if h := <-headers; h.Get("X-IPS") == "" {
		t.Error("agent sent no X-IPS header")
	}
	sess := <-sessions

	for _, msg := range []string{"SSH-2.0-test\r\n", "second stream"} {
		stream, err := sess.Open()
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := stream.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(stream, buf); err != nil {
			t.Fatalf("read: %s", err)
		}
		if string(buf) != msg {
			t.Fatalf("got %q, want %q", buf, msg)
		}
		stream.Close()
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunClosesStreamWhenTargetIsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := make(chan *yamux.Session, 1)
	headers := make(chan http.Header, 1)
	a := &Agent{Master: fakeMaster(t, ctx, sessions, headers), ID: "a1", Target: closedPort(t)}
	go func() { _ = a.Run(ctx) }()
	<-headers
	sess := <-sessions

	stream, err := sess.Open()
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = stream.Read(make([]byte, 1))
	if err != io.EOF {
		t.Fatalf("read = %v, want EOF when the target refuses", err)
	}
}

// When the master's session ends, the agent dials again.
func TestRunReconnects(t *testing.T) {
	old := retryDelay
	retryDelay = 10 * time.Millisecond
	t.Cleanup(func() { retryDelay = old })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := make(chan *yamux.Session, 2)
	headers := make(chan http.Header, 2)
	a := &Agent{Master: fakeMaster(t, ctx, sessions, headers), ID: "a1", Target: echoServer(t)}
	go func() { _ = a.Run(ctx) }()

	<-headers
	(<-sessions).Close()

	select {
	case <-headers:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not reconnect")
	}
}

// A failing master is retried until the context ends, and the wait between
// retries does not hold up shutdown.
func TestRunRetriesUntilCancelled(t *testing.T) {
	var hits atomic.Int32
	firstHit := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			close(firstHit)
		}
		http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{Master: "ws" + strings.TrimPrefix(srv.URL, "http"), ID: "a1", Target: "127.0.0.1:22"}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	<-firstHit
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run waited out the retry delay after cancel")
	}
}
