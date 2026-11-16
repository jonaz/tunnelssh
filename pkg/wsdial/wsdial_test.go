package wsdial

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// newCountingServer returns a server and a counter of the TCP connections it
// has accepted.
func newCountingServer(t *testing.T, h http.Handler) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &conns
}

func badGateway() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
	})
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestFailedHandshakeDoesNotReuseConnection(t *testing.T) {
	srv, conns := newCountingServer(t, badGateway())

	const dials = 3
	for i := 0; i < dials; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _, err := Dial(ctx, wsURL(srv), nil)
		cancel()
		if err == nil {
			t.Fatal("expected handshake error")
		}
	}

	if got := conns.Load(); got != dials {
		t.Fatalf("server accepted %d connections for %d dials, want one per dial", got, dials)
	}
}

// TestDefaultClientReusesConnection documents the behaviour Dial exists to
// avoid. If it starts failing, the library no longer pools failed handshakes.
func TestDefaultClientReusesConnection(t *testing.T) {
	srv, conns := newCountingServer(t, badGateway())

	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _, err := websocket.Dial(ctx, wsURL(srv), &websocket.DialOptions{HTTPClient: client})
		cancel()
		if err == nil {
			t.Fatal("expected handshake error")
		}
	}

	if got := conns.Load(); got != 1 {
		t.Fatalf("server accepted %d connections, want 1 (pooled)", got)
	}
}

func TestDialUpgrades(t *testing.T) {
	srv, _ := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" {
			http.Error(w, "missing header", http.StatusBadRequest)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		_ = c.Write(r.Context(), websocket.MessageText, []byte("hello"))
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := Dial(ctx, wsURL(srv), http.Header{"X-Test": {"yes"}})
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	_, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %s", err)
	}
	if string(msg) != "hello" {
		t.Fatalf("got %q, want hello", msg)
	}
}
