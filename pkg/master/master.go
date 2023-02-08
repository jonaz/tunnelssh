package master

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"nhooyr.io/websocket"
)

type Master struct {
	Port  string
	Token string
	wg    *sync.WaitGroup
	mutex sync.RWMutex
}

func NewMasterFromContext(c *cli.Context) *Master {
	m := &Master{
		Port:  c.String("port"),
		Token: c.String("token"),
		wg:    &sync.WaitGroup{},
	}
	return m
}

type sessions struct {
	mutex    sync.RWMutex
	sessions map[string]*yamux.Session
}

func (s *sessions) Set(id string, sess *yamux.Session) {
	s.mutex.Lock()
	s.sessions[id] = sess
	s.mutex.Unlock()
}
func (s *sessions) Get(id string) *yamux.Session {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.sessions[id]
}

func (a *Master) Run(pCtx context.Context) error {

	sessions := sessions{
		sessions: make(map[string]*yamux.Session),
	}

	http.HandleFunc("/connect/websocket-v1", func(w http.ResponseWriter, r *http.Request) {
		// TODO validate agent token
		id := r.URL.Query().Get("id")
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			logrus.Error(err)
			return
		}
		conn := websocket.NetConn(pCtx, c, websocket.MessageBinary)
		defer conn.Close()

		adminSession, err := yamux.Server(conn, nil)
		if err != nil {
			logrus.Error(err)
			return
		}

		adminConn, err := adminSession.Accept()
		if err != nil {
			logrus.Error(err)
			return
		}

		logrus.Debug("accepted connection from admin")
		agentSession := sessions.Get(id)
		if agentSession == nil {
			logrus.Error("found no agent connection")
			return
		}
		agentConn, err := agentSession.Open()
		if err != nil {
			logrus.Error(err)
			return
		}

		logrus.Debugf("opened connection from admin to %s", id)
		close := sync.Once{}
		cp := func(dst io.WriteCloser, src io.ReadCloser) {
			// b := buffers.Get()
			// defer buffers.Put(b)
			// io.CopyBuffer(dst, src, b)
			io.Copy(dst, src)
			close.Do(func() {
				dst.Close()
				src.Close()
			})
		}
		go cp(adminConn, agentConn)
		cp(agentConn, adminConn)

	})

	http.HandleFunc("/agent/websocket-v1", func(w http.ResponseWriter, r *http.Request) {
		// TODO validate agent token
		id := r.URL.Query().Get("id")

		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			logrus.Error(err)
			return
		}
		logrus.Debugf("accepted connection from %s", id)
		conn := websocket.NetConn(pCtx, c, websocket.MessageBinary)
		defer conn.Close()

		session, err := yamux.Server(conn, nil)
		if err != nil {
			logrus.Error(err)
			return
		}
		logrus.Debugf("accepted session from %s", id)
		sessions.Set(id, session)
		<-session.CloseChan()
	})

	server := &http.Server{Addr: ":" + a.Port}
	logrus.Info("started webserver")
	go server.ListenAndServe()
	<-pCtx.Done()
	return server.Shutdown(context.TODO())
}
