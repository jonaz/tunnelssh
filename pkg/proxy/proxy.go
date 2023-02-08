package proxy

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"nhooyr.io/websocket"
)

type Proxy struct {
	Master string
	ID     string
	Token  string
	wg     *sync.WaitGroup
	mutex  sync.RWMutex
}

func NewProxyFromContext(c *cli.Context) *Proxy {
	m := &Proxy{
		Master: c.String("master"),
		ID:     c.String("id"),
		Token:  c.String("token"),
		wg:     &sync.WaitGroup{},
	}
	return m
}

func (p *Proxy) Run(pCtx context.Context) error {
	ctx, cancel := context.WithTimeout(pCtx, time.Minute)
	defer cancel()

	//TODO send token in header
	u := fmt.Sprintf("%s/connect/websocket-v1?id=%s", p.Master, p.ID)
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

	conn, err := sess.Open()
	if err != nil {
		return err
	}

	close := sync.Once{}
	cp := func(dst io.WriteCloser, src io.ReadCloser) {
		// b := buffers.Get()
		// defer buffers.Put(b)
		// io.CopyBuffer(dst, src, b)
		_, err := io.Copy(dst, src)
		if err != nil {
			logrus.Errorf("io.Copy error: %s", err)
		}
		close.Do(func() {
			dst.Close()
			src.Close()
		})
	}
	go cp(conn, os.Stdin)
	cp(os.Stdout, conn)

	return nil

}
