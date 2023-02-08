package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"nhooyr.io/websocket"
)

type Agent struct {
	Master string
	ID     string
	Token  string
	wg     *sync.WaitGroup
	mutex  sync.RWMutex
}

func NewAgentFromContext(c *cli.Context) *Agent {
	m := &Agent{
		Master: c.String("master"),
		ID:     c.String("id"),
		Token:  c.String("token"),
		wg:     &sync.WaitGroup{},
	}
	return m
}

func (a *Agent) Run(pCtx context.Context) error {
	//TODO retry loop
	ctx, cancel := context.WithTimeout(pCtx, time.Second*10)
	defer cancel()

	//TODO send token in header
	u := fmt.Sprintf("%s/agent/websocket-v1?id=%s", a.Master, a.ID)
	wsClient, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		return err
	}

	c := websocket.NetConn(context.TODO(), wsClient, websocket.MessageBinary)
	defer c.Close()

	sess, err := yamux.Client(c, nil)
	if err != nil {
		return err
	}
	defer sess.Close()

	acceptCh := make(chan net.Conn)
	go func() {
		/* TODO */
		for {
			conn, err := sess.Accept()
			if err != nil {
				logrus.Errorf("accept error: %s", err)
				close(acceptCh)
				return
			}
			acceptCh <- conn
		}
	}()

	for {
		select {
		case <-pCtx.Done():
			return nil
		case conn, ok := <-acceptCh:
			if !ok {
				return nil
			}

			ctx1, cancel1 := context.WithTimeout(pCtx, time.Second*10)
			defer cancel1()
			var d net.Dialer
			remote, err := d.DialContext(ctx1, "tcp", "127.0.0.1:22")
			if err != nil {
				return err
			}

			close := sync.Once{}
			cp := func(dst io.WriteCloser, src io.ReadCloser) {
				// b := buffers.Get()
				// defer buffers.Put(b)
				// io.CopyBuffer(dst, src, b)
				_, err := io.Copy(dst, src)
				if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
					logrus.Errorf("io.Copy error: %s", err)
				}
				close.Do(func() {
					dst.Close()
					src.Close()
				})
			}
			go cp(conn, remote)
			cp(remote, conn)
		}
	}
}
