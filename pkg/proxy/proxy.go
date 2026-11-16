package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/jonaz/tunnelssh/pkg/wsdial"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

type Proxy struct {
	Master string
	ID     string
	Token  string
	wg     *sync.WaitGroup

	// stdin and stdout are the ends of the tunnel: os.Stdin and os.Stdout
	// when ssh runs the proxy as its ProxyCommand.
	stdin  io.ReadCloser
	stdout io.WriteCloser
}

func NewProxyFromContext(c *cli.Context) *Proxy {
	m := &Proxy{
		Master: c.String("master"),
		ID:     c.String("id"),
		Token:  c.String("token"),
		wg:     &sync.WaitGroup{},
		stdin:  os.Stdin,
		stdout: os.Stdout,
	}
	return m
}

func (p *Proxy) Run(pCtx context.Context) error {
	ctx, cancel := context.WithTimeout(pCtx, time.Minute)
	defer cancel()

	u := fmt.Sprintf("%s/connect/websocket-v1?id=%s", p.Master, p.ID)
	wsClient, _, err := wsdial.Dial(ctx, u, http.Header{"Authorization": {p.Token}})
	if err != nil {
		return err
	}

	c := websocket.NetConn(context.Background(), wsClient, websocket.MessageBinary)
	defer c.Close()

	sess, err := yamux.Client(c, nil)
	if err != nil {
		return err
	}
	defer sess.Close()

	conn, err := sess.Open()
	if err != nil {
		return err
	}
	defer conn.Close()

	close := sync.Once{}
	cp := func(dst io.WriteCloser, src io.ReadCloser) {
		_, err := io.Copy(dst, src)
		if err != nil {
			logrus.Errorf("io.Copy error: %s", err)
		}
		close.Do(func() {
			dst.Close()
			src.Close()
		})
	}
	go cp(conn, p.stdin)
	cp(p.stdout, conn)

	return nil

}
