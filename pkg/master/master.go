package master

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/jonaz/tunnelssh/pkg/jwt"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

type Master struct {
	Port string
	// MetricsAddr is where /metrics is served, apart from Port so that it
	// is never reachable through whatever exposes Port. Empty serves none.
	MetricsAddr string
	jwt         *jwt.JWTHandler

	wg *sync.WaitGroup
}

func NewMasterFromContext(c *cli.Context) *Master {
	m := &Master{
		Port:        c.String("port"),
		MetricsAddr: c.String("metrics-addr"),
		jwt:         jwt.New(c.String("token")),
		wg:          &sync.WaitGroup{},
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

// Add registers sess for id unless id is already taken, and reports
// whether it did. Checking and registering under one lock is what keeps two
// agents with the same id from both being registered.
func (s *agentSessions) Add(id string, sess *yamux.Session, ip string, internalIPs []string) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if _, ok := s.sessions[id]; ok {
		return false
	}
	s.sessions[id] = &agentSession{
		yamux:      sess,
		ExternalIP: ip,
		InteralIPs: internalIPs,
	}
	return true
}

// Delete removes id if it is still registered to sess, so a session can
// never remove another's entry.
func (s *agentSessions) Delete(id string, sess *yamux.Session) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if cur, ok := s.sessions[id]; ok && cur.yamux == sess {
		delete(s.sessions, id)
	}
}

func (s *agentSessions) Get(id string) *agentSession {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.sessions[id]
}

// IDs returns the ids that have a session.
func (s *agentSessions) IDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	return ids
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
	public, metrics := a.handlers(pCtx)
	servers := []*http.Server{{Addr: ":" + a.Port, Handler: public}}
	if a.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics)
		servers = append(servers, &http.Server{Addr: a.MetricsAddr, Handler: mux})
	}
	for _, server := range servers {
		go func() {
			err := server.ListenAndServe()
			// Shutdown makes ListenAndServe return ErrServerClosed; that is a
			// clean stop, not a reason to exit 1.
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				logrus.Fatal(err)
			}
		}()
		logrus.Infof("started webserver on %s", server.Addr)
	}
	<-pCtx.Done()
	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var errs []error
	for _, server := range servers {
		errs = append(errs, server.Shutdown(ctxShutDown))
	}
	return errors.Join(errs...)
}

// handlers returns the master's public routes and its /metrics handler,
// which share the agent sessions. The sessions live as long as pCtx.
func (a *Master) handlers(pCtx context.Context) (public, metricsHandler http.Handler) {
	mux := http.NewServeMux()
	sessions := NewAgentSessions()
	metrics := newMetrics(sessions)
	mux.HandleFunc("/connect/websocket-v1", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("/agent/websocket-v1", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if !validID(id) {
			logrus.Errorf("refused agent with invalid id %q", id)
			metrics.rejected.WithLabelValues(rejectInvalidID).Inc()
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if sessions.Get(id) != nil {
			logrus.Errorf("session %s already exists", id)
			metrics.rejected.WithLabelValues(rejectDuplicateID).Inc()
			w.WriteHeader(http.StatusConflict)
			return
		}

		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			logrus.Error(err)
			metrics.rejected.WithLabelValues(rejectHandshake).Inc()
			return
		}
		logrus.Debugf("accepted connection from %s", id)
		conn := websocket.NetConn(pCtx, c, websocket.MessageBinary)
		defer conn.Close()

		session, err := yamux.Server(conn, nil)
		if err != nil {
			logrus.Error(err)
			metrics.rejected.WithLabelValues(rejectHandshake).Inc()
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
		// The check before Accept gives the usual duplicate a 409, but
		// another agent with this id may have connected since. Too late
		// for a status code: close, and the agent's retry gets the 409.
		if !sessions.Add(id, session, ipAddress, strings.Split(r.Header.Get("x-ips"), ",")) {
			logrus.Errorf("session %s already exists", id)
			metrics.rejected.WithLabelValues(rejectDuplicateID).Inc()
			return
		}
		metrics.connects.Inc()
		defer metrics.disconnects.Inc()
		defer sessions.Delete(id, session)
		<-session.CloseChan()
	})

	mux.HandleFunc("/sessions-v1", a.listSessions(sessions))

	mux.HandleFunc("/token-v1", a.createAdminToken)
	return mux, metrics.handler()
}

// maxIDLength is the longest agent id the master accepts.
const maxIDLength = 64

// validID reports whether id is one the master registers: 1 to maxIDLength
// characters of A-Z, a-z, 0-9, '.', '_' and '-'. Agents connect without
// authentication, and the id becomes a label of tunnelssh_agent_connected
// and a key of /sessions-v1, so an empty id, or one with quotes, newlines
// or kilobytes in it, is refused before the upgrade.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLength {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
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
