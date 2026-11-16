package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/urfave/cli/v2"
)

func TestNewProxyFromContext(t *testing.T) {
	var p *Proxy
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "master"},
			&cli.StringFlag{Name: "id"},
			&cli.StringFlag{Name: "token"},
		},
		Action: func(c *cli.Context) error {
			p = NewProxyFromContext(c)
			return nil
		},
	}
	if err := app.Run([]string{"proxy", "--master", "wss://m", "--id", "a1", "--token", "tok"}); err != nil {
		t.Fatal(err)
	}
	if p.Master != "wss://m" || p.ID != "a1" || p.Token != "tok" {
		t.Fatalf("proxy = %+v", p)
	}
	if p.stdin != os.Stdin || p.stdout != os.Stdout {
		t.Fatal("proxy does not tunnel stdin and stdout")
	}
}

// fakeMaster accepts a proxy with token "tok" for agent a1 and echoes the
// stream it opens, as an agent tunnelling to an echo server would.
func fakeMaster(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/connect/websocket-v1" || r.URL.Query().Get("id") != "a1" {
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		sess, err := yamux.Server(websocket.NetConn(ctx, c, websocket.MessageBinary), nil)
		if err != nil {
			return
		}
		defer sess.Close()
		stream, err := sess.Accept()
		if err != nil {
			return
		}
		defer stream.Close()
		_, _ = io.Copy(stream, stream)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestRunTunnelsStdio(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &Proxy{Master: fakeMaster(t), ID: "a1", Token: "tok", stdin: inR, stdout: outW}

	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()

	const msg = "SSH-2.0-test\r\n"
	go func() { _, _ = inW.Write([]byte(msg)) }()
	buf := make([]byte, len(msg))
	read := make(chan error, 1)
	go func() { _, err := io.ReadFull(outR, buf); read <- err }()
	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("read: %s", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no echo on stdout")
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", buf, msg)
	}

	// ssh closing its end of the pipe ends the proxy.
	inW.Close()
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %s", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after stdin closed")
	}
}

func TestRunWrongToken(t *testing.T) {
	inR, _ := io.Pipe()
	_, outW := io.Pipe()
	p := &Proxy{Master: fakeMaster(t), ID: "a1", Token: "wrong", stdin: inR, stdout: outW}
	err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Run = %v, want a 401 handshake error", err)
	}
}
