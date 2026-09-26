// Package journallog sends logrus entries to journald over its native
// protocol, so each line keeps its priority and logrus fields become journal
// fields. Plain stderr lines are all stored at priority info, which would hide
// warnings from a `journalctl -p warning` reader.
package journallog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/sirupsen/logrus"
)

// Install routes the standard logger to journald when stderr is connected to
// the journal (the process runs under systemd) and reports whether it did.
// Otherwise logging stays on stderr, e.g. under `make dev` and in tests.
func Install() bool {
	ok, err := journal.StderrIsJournalStream()
	if err != nil || !ok {
		return false
	}
	logrus.AddHook(New())
	logrus.SetOutput(io.Discard)
	return true
}

// SendFunc matches journal.Send.
type SendFunc func(message string, priority journal.Priority, vars map[string]string) error

// Hook is a logrus hook writing every entry to journald.
type Hook struct {
	send       SendFunc
	fallback   io.Writer
	formatter  logrus.Formatter
	identifier string
}

func New() *Hook {
	return newHook(journal.Send, os.Stderr)
}

func newHook(send SendFunc, fallback io.Writer) *Hook {
	return &Hook{
		send:       send,
		fallback:   fallback,
		formatter:  &logrus.TextFormatter{DisableColors: true},
		identifier: filepath.Base(os.Args[0]),
	}
}

func (h *Hook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *Hook) Fire(e *logrus.Entry) error {
	vars := make(map[string]string, len(e.Data)+1)
	for k, v := range e.Data {
		name := FieldName(k)
		if name == "" {
			continue
		}
		vars[name] = fmt.Sprint(v)
	}
	vars["SYSLOG_IDENTIFIER"] = h.identifier

	if err := h.send(e.Message, Priority(e.Level), vars); err != nil {
		// Write the entry to stderr instead so it isn't lost.
		if b, ferr := h.formatter.Format(e); ferr == nil {
			_, _ = h.fallback.Write(b)
		}
	}
	return nil
}

// Priority maps a logrus level to a syslog priority.
func Priority(l logrus.Level) journal.Priority {
	switch l {
	case logrus.PanicLevel, logrus.FatalLevel:
		return journal.PriCrit
	case logrus.ErrorLevel:
		return journal.PriErr
	case logrus.WarnLevel:
		return journal.PriWarning
	case logrus.InfoLevel:
		return journal.PriInfo
	default:
		return journal.PriDebug
	}
}

// reserved are journal fields a logrus field must not overwrite.
var reserved = []string{"MESSAGE", "PRIORITY", "SYSLOG_IDENTIFIER"}

// FieldName turns a logrus field key into a valid journal field name:
// uppercase, characters outside A-Z, 0-9 and _ replaced by _, and leading
// underscores dropped so a field can't pose as a trusted one. It returns ""
// for keys that can't be used.
func FieldName(key string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(key) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.TrimLeft(b.String(), "_")
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return ""
	}
	if slices.Contains(reserved, name) {
		return ""
	}
	return name
}
