package master

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/jonaz/tunnelssh/pkg/jwt"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"nhooyr.io/websocket"
)

type Master struct {
	Port string
	jwt  *jwt.JWTHandler

	wg *sync.WaitGroup
}

func NewMasterFromContext(c *cli.Context) *Master {
	m := &Master{
		Port: c.String("port"),
		jwt:  jwt.New(c.String("token")),
		wg:   &sync.WaitGroup{},
	}
	return m
}

type agentSession struct {
	yamux      *yamux.Session
	ExternalIP string
	InteralIPs []string
}

type agentSessions struct {
	mutex    sync.RWMutex
	sessions map[string]*agentSession
}

func NewAgentSessions() *agentSessions {
	return &agentSessions{
		sessions: make(map[string]*agentSession),
	}

}

func (s *agentSessions) Set(id string, sess *yamux.Session, ip string, internalIPs []string) {
	s.mutex.Lock()
	s.sessions[id] = &agentSession{
		yamux:      sess,
		ExternalIP: ip,
		InteralIPs: internalIPs,
	}
	s.mutex.Unlock()
}
func (s *agentSessions) Delete(id string) {
	s.mutex.Lock()
	delete(s.sessions, id)
	s.mutex.Unlock()
}
func (s *agentSessions) Get(id string) *agentSession {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.sessions[id]
}
func (s *agentSessions) List() map[string]agentSession {
	ret := make(map[string]agentSession)
	s.mutex.RLock()
	for id, sess := range s.sessions {
		ret[id] = *sess
	}
	s.mutex.RUnlock()
	return ret
}

func (a *Master) Run(pCtx context.Context) error {

	sessions := NewAgentSessions()
	http.HandleFunc("/connect/websocket-v1", func(w http.ResponseWriter, r *http.Request) {
		_, err := a.jwt.Validate(r)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

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
		defer adminSession.Close()

		adminConn, err := adminSession.Accept()
		if err != nil {
			logrus.Error(err)
			return
		}
		defer adminConn.Close()

		logrus.Debug("accepted connection from admin")
		agentSession := sessions.Get(id)
		if agentSession == nil {
			logrus.Error("found no agent connection")
			return
		}
		agentConn, err := agentSession.yamux.Open()
		if err != nil {
			logrus.Error("error agentSession open:", err)
			return
		}
		defer agentConn.Close()

		logrus.Debugf("opened connection from admin to %s", id)
		close := sync.Once{}
		cp := func(dst io.WriteCloser, src io.ReadCloser) {
			io.Copy(dst, src) // error is not important we just want to stop when ether side closes in any way.
			close.Do(func() {
				dst.Close()
				src.Close()
			})
		}
		go cp(adminConn, agentConn)
		cp(agentConn, adminConn)

	})

	http.HandleFunc("/agent/websocket-v1", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if sessions.Get(id) != nil {
			logrus.Errorf("session %s already exists", id)
			w.WriteHeader(http.StatusConflict)
			return
		}

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
		defer session.Close()

		logrus.Debugf("accepted session from %s", id)

		ipAddress := r.RemoteAddr
		fwdAddress := r.Header.Get("x-forwarded-for")

		if fwdAddress != "" {
			ips := strings.Split(fwdAddress, ",")
			if len(ips) > 0 {
				ipAddress = ips[0]
			}
		}
		sessions.Set(id, session, ipAddress, strings.Split(r.Header.Get("x-ips"), ","))
		defer sessions.Delete(id)
		<-session.CloseChan()
	})

	http.HandleFunc("/sessions-v1", a.listSessions(sessions))

	http.HandleFunc("/token-v1", a.createAdminToken)

	server := &http.Server{Addr: ":" + a.Port}
	go func() {
		err := server.ListenAndServe()
		if err != nil {
			logrus.Fatal(err)
		}
	}()
	logrus.Info("started webserver")
	<-pCtx.Done()
	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(ctxShutDown)
}

func (a *Master) listSessions(sessions *agentSessions) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		_, err := a.jwt.Validate(r)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		err = json.NewEncoder(w).Encode(sessions.List())
		if err != nil {
			logrus.Error(err)
			return
		}
	}
}

func (a *Master) createAdminToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ipStr, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		logrus.Error(err)
		return
	}

	ip := net.ParseIP(ipStr)

	isProxy := r.Header.Get("x-forwarded-for") != ""
	if !ip.IsLoopback() || isProxy {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	claims := jwt.DefaultClaims()
	token, err := a.jwt.GenerateJWT(claims)
	if err != nil {
		logrus.Error(err)
		return
	}

	err = json.NewEncoder(w).Encode(map[string]string{"jwt": token})
	if err != nil {
		logrus.Error(err)
	}
}
