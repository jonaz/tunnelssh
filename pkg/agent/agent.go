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
	Target string
	wg     *sync.WaitGroup
}

func NewAgentFromContext(c *cli.Context) *Agent {
	m := &Agent{
		Master: c.String("master"),
		ID:     c.String("id"),
		Token:  c.String("token"),
		Target: c.String("target"),
		wg:     &sync.WaitGroup{},
	}
	return m
}

func (a *Agent) Run(pCtx context.Context) error {
	for {
		err := a.run(pCtx)
		if err != nil {
			logrus.Error(err)
		}
		if pCtx.Err() != nil {
			return pCtx.Err()
		}
		time.Sleep(time.Second * 5)
	}
}
func (a *Agent) run(pCtx context.Context) error {
	ctx, cancel := context.WithTimeout(pCtx, time.Second*10)
	defer cancel()

	u := fmt.Sprintf("%s/agent/websocket-v1?id=%s", a.Master, a.ID)
	logrus.Debugf("connecting to websocket: %s", u)
	wsClient, _, err := websocket.Dial(ctx, u, nil)
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

	acceptCh := make(chan net.Conn)
	go func() {
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
			remote, err := d.DialContext(ctx1, "tcp", a.Target)
			if err != nil {
				conn.Close()
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
			conn.Close()
			remote.Close()
		}
	}
}
