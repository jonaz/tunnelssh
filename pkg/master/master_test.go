package master

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/jonaz/tunnelssh/pkg/agent"
	"github.com/jonaz/tunnelssh/pkg/jwt"
	"github.com/jonaz/tunnelssh/pkg/wsdial"
)

type listedSession struct {
	ExternalIP string
	InteralIPs []string
}

type testMaster struct {
	t       *testing.T
	ctx     context.Context
	srv     *httptest.Server
	metrics *httptest.Server
	ws      string // srv.URL with a ws:// scheme
	token   string
}

func newTestMaster(t *testing.T) *testMaster {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m := &Master{jwt: jwt.New("secret")}
	public, metrics := m.handlers(ctx)
	srv := httptest.NewServer(public)
	t.Cleanup(srv.Close)
	ms := httptest.NewServer(metrics)
	t.Cleanup(ms.Close)

	tm := &testMaster{t: t, ctx: ctx, srv: srv, metrics: ms, ws: "ws" + strings.TrimPrefix(srv.URL, "http")}
	tm.token = tm.adminToken()
	return tm
}

// adminToken gets a token the way an admin on the master's host does.
func (tm *testMaster) adminToken() string {
	tm.t.Helper()
	resp, err := http.Post(tm.srv.URL+"/token-v1", "", nil)
	if err != nil {
		tm.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tm.t.Fatalf("token-v1: %s", resp.Status)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		tm.t.Fatal(err)
	}
	return body["jwt"]
}

func (tm *testMaster) sessions(token string) (int, map[string]listedSession) {
	tm.t.Helper()
	req, _ := http.NewRequest("GET", tm.srv.URL+"/sessions-v1", nil)
	req.Header.Set("Authorization", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tm.t.Fatal(err)
	}
	defer resp.Body.Close()
	var list map[string]listedSession
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			tm.t.Fatal(err)
		}
	}
	return resp.StatusCode, list
}

// waitFor polls the session list until ok(list) holds.
func (tm *testMaster) waitFor(what string, ok func(map[string]listedSession) bool) map[string]listedSession {
	tm.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, list := tm.sessions(tm.token)
		if ok(list) {
			return list
		}
		if time.Now().After(deadline) {
			tm.t.Fatalf("timed out waiting for %s, sessions: %v", what, list)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// connect opens a tunnel to agent id the way the proxy does.
func (tm *testMaster) connect(id, token string) (net.Conn, error) {
	tm.t.Helper()
	ctx, cancel := context.WithTimeout(tm.ctx, 5*time.Second)
	defer cancel()
	ws, _, err := wsdial.Dial(ctx, tm.ws+"/connect/websocket-v1?id="+id, http.Header{"Authorization": {token}})
	if err != nil {
		return nil, err
	}
	c := websocket.NetConn(tm.ctx, ws, websocket.MessageBinary)
	sess, err := yamux.Client(c, nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	tm.t.Cleanup(func() { sess.Close() })
	return sess.Open()
}

// echoServer stands in for the sshd an agent tunnels to.
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

func startAgent(t *testing.T, tm *testMaster, id string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(tm.ctx)
	done := make(chan struct{})
	a := &agent.Agent{Master: tm.ws, ID: id, Target: echoServer(t)}
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %s", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %s", err)
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", buf, msg)
	}
}

func TestTunnelThroughAgent(t *testing.T) {
	tm := newTestMaster(t)
	stop := startAgent(t, tm, "a1")

	list := tm.waitFor("agent a1", func(l map[string]listedSession) bool { _, ok := l["a1"]; return ok })
	s := list["a1"]
	if !strings.HasPrefix(s.ExternalIP, "127.0.0.1:") {
		t.Errorf("ExternalIP = %q, want the agent's 127.0.0.1 address", s.ExternalIP)
	}
	if len(s.InteralIPs) == 0 || s.InteralIPs[0] == "" {
		t.Errorf("InteralIPs = %q, want the agent's X-IPS", s.InteralIPs)
	}

	c, err := tm.connect("a1", tm.token)
	if err != nil {
		t.Fatalf("connect: %s", err)
	}
	defer c.Close()
	roundTrip(t, c, "SSH-2.0-test\r\n")

	// A second tunnel to the same agent works alongside the first.
	c2, err := tm.connect("a1", tm.token)
	if err != nil {
		t.Fatalf("connect: %s", err)
	}
	defer c2.Close()
	roundTrip(t, c2, "second")

	// When the agent goes away, its session is dropped.
	stop()
	tm.waitFor("a1 to go away", func(l map[string]listedSession) bool { _, ok := l["a1"]; return !ok })
}

func TestDuplicateAgentIDRefused(t *testing.T) {
	tm := newTestMaster(t)
	startAgent(t, tm, "a1")
	tm.waitFor("agent a1", func(l map[string]listedSession) bool { _, ok := l["a1"]; return ok })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := wsdial.Dial(ctx, tm.ws+"/agent/websocket-v1?id=a1", nil)
	if err == nil {
		t.Fatal("second agent with the same id accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("resp = %v, want 409", resp)
	}
}

func TestExternalIPFromForwardedFor(t *testing.T) {
	tm := newTestMaster(t)

	ctx, cancel := context.WithTimeout(tm.ctx, 5*time.Second)
	defer cancel()
	ws, _, err := wsdial.Dial(ctx, tm.ws+"/agent/websocket-v1?id=behind-proxy", http.Header{
		"X-Forwarded-For": {"203.0.113.7, 10.0.0.1"},
		"X-Ips":           {"10.1.0.2/24,fe80::1/64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := websocket.NetConn(tm.ctx, ws, websocket.MessageBinary)
	sess, err := yamux.Client(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	list := tm.waitFor("behind-proxy", func(l map[string]listedSession) bool { _, ok := l["behind-proxy"]; return ok })
	s := list["behind-proxy"]
	if s.ExternalIP != "203.0.113.7" {
		t.Errorf("ExternalIP = %q, want the first X-Forwarded-For entry", s.ExternalIP)
	}
	if strings.Join(s.InteralIPs, ",") != "10.1.0.2/24,fe80::1/64" {
		t.Errorf("InteralIPs = %q", s.InteralIPs)
	}
}

func TestConnectToUnknownAgentCloses(t *testing.T) {
	tm := newTestMaster(t)
	c, err := tm.connect("nobody", tm.token)
	if err != nil {
		t.Fatalf("connect: %s", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("read from a tunnel to an unknown agent succeeded")
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("tunnel to an unknown agent was left open")
	}
}

func TestAuthRequired(t *testing.T) {
	tm := newTestMaster(t)
	forged, err := jwt.New("wrong").GenerateJWT(jwt.DefaultClaims())
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{"none": "", "wrong key": forged} {
		t.Run(name, func(t *testing.T) {
			if code, _ := tm.sessions(token); code != http.StatusUnauthorized {
				t.Errorf("sessions-v1: %d, want 401", code)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, resp, err := wsdial.Dial(ctx, tm.ws+"/connect/websocket-v1?id=a1", http.Header{"Authorization": {token}})
			if err == nil {
				t.Fatal("connect-v1 accepted")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("connect-v1: %v, want 401", resp)
			}
		})
	}
}

func TestCreateAdminToken(t *testing.T) {
	m := &Master{jwt: jwt.New("secret")}

	for _, tc := range []struct {
		name       string
		method     string
		remoteAddr string
		forwarded  string
		want       int
	}{
		{"loopback v4", http.MethodPost, "127.0.0.1:5000", "", http.StatusOK},
		{"loopback v6", http.MethodPost, "[::1]:5000", "", http.StatusOK},
		{"GET", http.MethodGet, "127.0.0.1:5000", "", http.StatusMethodNotAllowed},
		{"remote", http.MethodPost, "192.0.2.1:5000", "", http.StatusUnauthorized},
		// A local reverse proxy makes every request look like loopback.
		{"via proxy", http.MethodPost, "127.0.0.1:5000", "192.0.2.1", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/token-v1", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			w := httptest.NewRecorder()
			m.createAdminToken(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.want != http.StatusOK {
				return
			}
			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if _, err := m.jwt.ValidateToken(body["jwt"]); err != nil {
				t.Fatalf("issued token does not validate: %s", err)
			}
		})
	}
}

func TestAgentSessions(t *testing.T) {
	s := NewAgentSessions()
	if s.Get("a") != nil {
		t.Fatal("empty sessions returned a session")
	}
	first, second := &yamux.Session{}, &yamux.Session{}
	if !s.Add("a", first, "192.0.2.1", []string{"10.0.0.1/24"}) {
		t.Fatal("Add to an empty id refused")
	}
	if s.Add("a", second, "192.0.2.2", nil) {
		t.Fatal("Add to a taken id accepted")
	}
	if got := s.Get("a"); got == nil || got.yamux != first || got.ExternalIP != "192.0.2.1" {
		t.Fatalf("Get = %+v, want the first session", got)
	}
	list := s.List()
	if len(list) != 1 || list["a"].InteralIPs[0] != "10.0.0.1/24" {
		t.Fatalf("List = %+v", list)
	}

	// Only the session holding the id can free it.
	s.Delete("a", second)
	if s.Get("a") == nil {
		t.Fatal("Delete with another session removed the entry")
	}
	s.Delete("a", first)
	if s.Get("a") != nil || len(s.List()) != 0 {
		t.Fatal("session still there after Delete")
	}
}

// Run must stop cleanly when its context ends, not exit the process: before
// ErrServerClosed was ignored, Shutdown made the listener goroutine call
// logrus.Fatal.
func TestRunStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Master{Port: "0", jwt: jwt.New("secret")}
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	time.Sleep(50 * time.Millisecond) // let ListenAndServe start
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %s", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	// Give the listener goroutine time to see ErrServerClosed; a Fatal there
	// would end the test binary.
	time.Sleep(50 * time.Millisecond)
}

// rawAgent is an agent that echoes every stream the master opens.
type rawAgent struct {
	sess *yamux.Session
}

func dialRawAgent(tm *testMaster, id string) (*rawAgent, error) {
	ctx, cancel := context.WithTimeout(tm.ctx, 5*time.Second)
	defer cancel()
	ws, _, err := wsdial.Dial(ctx, tm.ws+"/agent/websocket-v1?id="+id, nil)
	if err != nil {
		return nil, err
	}
	sess, err := yamux.Client(websocket.NetConn(tm.ctx, ws, websocket.MessageBinary), nil)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			s, err := sess.Accept()
			if err != nil {
				return
			}
			go func() {
				defer s.Close()
				_, _ = io.Copy(s, s)
			}()
		}
	}()
	return &rawAgent{sess: sess}, nil
}

func (a *rawAgent) open() bool {
	select {
	case <-a.sess.CloseChan():
		return false
	default:
		return true
	}
}

// Agents racing with the same id: exactly one is kept and stays reachable,
// the rest are refused or disconnected, and the losers going away does not
// drop the winner.
func TestDuplicateAgentIDRace(t *testing.T) {
	tm := newTestMaster(t)

	const n = 20
	agents := make(chan *rawAgent, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if a, err := dialRawAgent(tm, "dup"); err == nil {
				agents <- a
			}
		}()
	}
	close(start)
	wg.Wait()
	close(agents)
	var connected []*rawAgent
	for a := range agents {
		connected = append(connected, a)
		t.Cleanup(func() { a.sess.Close() })
	}

	// The master closes every upgraded agent that lost the race.
	var alive []*rawAgent
	deadline := time.Now().Add(2 * time.Second)
	for {
		alive = alive[:0]
		for _, a := range connected {
			if a.open() {
				alive = append(alive, a)
			}
		}
		if len(alive) <= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(alive) != 1 {
		t.Fatalf("%d of %d agents with the same id still connected, want 1", len(alive), len(connected))
	}

	// The winner is the one the master tunnels to.
	tm.waitFor("dup", func(l map[string]listedSession) bool { _, ok := l["dup"]; return ok })
	c, err := tm.connect("dup", tm.token)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, "to the winner")
	c.Close()
	if !alive[0].open() {
		t.Fatal("winner was disconnected")
	}

	// Once the winner leaves, the id is free again.
	alive[0].sess.Close()
	tm.waitFor("dup to go away", func(l map[string]listedSession) bool { _, ok := l["dup"]; return !ok })
}
