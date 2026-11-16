// Package wsdial dials websockets without reusing pooled HTTP connections.
package wsdial

import (
	"context"
	"net/http"

	"nhooyr.io/websocket"
)

// httpClient has keep-alives turned off. When a handshake fails with a short
// error body (a proxy's 502, say), the websocket library reads it to EOF and
// net/http would return the connection to the idle pool. The next dial would
// then reuse it, skip the DNS lookup and keep talking to the same, possibly
// stale, IP. A websocket connection is never reused for HTTP anyway, so
// nothing is lost by turning keep-alives off.
var httpClient = newHTTPClient()

func newHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	return &http.Client{Transport: tr}
}

// Dial is websocket.Dial with a client that dials a fresh connection, and
// resolves DNS again, every time.
func Dial(ctx context.Context, u string, header http.Header) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient: httpClient,
		HTTPHeader: header,
	})
}
