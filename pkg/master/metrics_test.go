package master

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonaz/tunnelssh/pkg/jwt"
	"github.com/jonaz/tunnelssh/pkg/wsdial"
)

// scrape returns the master's /metrics as series (name with labels) to value.
func scrape(t *testing.T, url string) map[string]float64 {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", url, resp.Status)
	}
	series := map[string]float64{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("%q: %s", line, err)
		}
		series[line[:i]] = v
	}
	return series
}

// waitMetric polls /metrics until series has value want; a negative want
// waits for the series to be gone.
func (tm *testMaster) waitMetric(series string, want float64) {
	tm.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, ok := scrape(tm.t, tm.metrics.URL)[series]
		if (want < 0 && !ok) || (ok && got == want) {
			return
		}
		if time.Now().After(deadline) {
			tm.t.Fatalf("%s = %v (present: %v), want %v", series, got, ok, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const (
	sessionsSeries    = "tunnelssh_agent_sessions"
	connectsSeries    = "tunnelssh_agent_connects_total"
	disconnectsSeries = "tunnelssh_agent_disconnects_total"
	duplicateSeries   = `tunnelssh_agent_rejected_total{reason="duplicate_id"}`
	handshakeSeries   = `tunnelssh_agent_rejected_total{reason="handshake"}`
)

func TestMetricsFollowAgents(t *testing.T) {
	tm := newTestMaster(t)

	// Everything is there from the start, at zero.
	start := scrape(t, tm.metrics.URL)
	for _, s := range []string{sessionsSeries, connectsSeries, disconnectsSeries, duplicateSeries, handshakeSeries} {
		if v, ok := start[s]; !ok || v != 0 {
			t.Errorf("%s = %v (present: %v) before any agent, want 0", s, v, ok)
		}
	}

	stop := startAgent(t, tm, "a1")
	tm.waitMetric(sessionsSeries, 1)
	tm.waitMetric(`tunnelssh_agent_connected{id="a1"}`, 1)
	tm.waitMetric(connectsSeries, 1)

	// A second agent with the id gets the 409 and is counted as refused,
	// not connected.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := wsdial.Dial(ctx, tm.ws+"/agent/websocket-v1?id=a1", nil); err == nil {
		t.Fatal("second agent with the same id accepted")
	}
	tm.waitMetric(duplicateSeries, 1)
	tm.waitMetric(connectsSeries, 1)
	tm.waitMetric(sessionsSeries, 1)

	stop()
	tm.waitMetric(sessionsSeries, 0)
	tm.waitMetric(`tunnelssh_agent_connected{id="a1"}`, -1)
	tm.waitMetric(disconnectsSeries, 1)
}

func TestMetricsCountFailedHandshake(t *testing.T) {
	tm := newTestMaster(t)
	// A plain GET, no websocket upgrade.
	resp, err := http.Get(tm.srv.URL + "/agent/websocket-v1?id=a1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	tm.waitMetric(handshakeSeries, 1)
	tm.waitMetric(connectsSeries, 0)
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

// /metrics is served on MetricsAddr only, never on Port, which is the one
// a reverse proxy exposes.
func TestRunServesMetricsApart(t *testing.T) {
	port, metricsPort := freePort(t), freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	m := &Master{Port: port, MetricsAddr: "127.0.0.1:" + metricsPort, jwt: jwt.New("secret")}
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %s", err)
		}
	}()

	get := func(url string) (int, string) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			resp, err := http.Get(url)
			if err == nil {
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, string(body)
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	code, body := get("http://127.0.0.1:" + metricsPort + "/metrics")
	if code != http.StatusOK || !strings.Contains(body, sessionsSeries+" 0") {
		t.Fatalf("metrics addr /metrics: %d\n%s", code, body)
	}
	if code, _ := get("http://127.0.0.1:" + port + "/metrics"); code != http.StatusNotFound {
		t.Fatalf("port /metrics: %d, want 404", code)
	}
}

func TestInvalidAgentIDRefused(t *testing.T) {
	tm := newTestMaster(t)
	for i, id := range []string{"", strings.Repeat("a", maxIDLength+1), "a b", `a"b`, "a\nb", "å"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, resp, err := wsdial.Dial(ctx, tm.ws+"/agent/websocket-v1?id="+url.QueryEscape(id), nil)
		cancel()
		if err == nil {
			t.Fatalf("agent with id %q accepted", id)
		}
		if resp == nil || resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("id %q: resp = %v, want 400", id, resp)
		}
		tm.waitMetric(`tunnelssh_agent_rejected_total{reason="invalid_id"}`, float64(i+1))
	}
	tm.waitMetric(sessionsSeries, 0)
	tm.waitMetric(connectsSeries, 0)
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"a", "10000000c5e632d2", "host-1.lan_2", strings.Repeat("a", maxIDLength)} {
		if !validID(id) {
			t.Errorf("validID(%q) = false, want true", id)
		}
	}
}
