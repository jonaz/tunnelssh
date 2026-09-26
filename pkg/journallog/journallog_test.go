package journallog

import (
	"bytes"
	"errors"
	"testing"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestPriority(t *testing.T) {
	tests := map[logrus.Level]journal.Priority{
		logrus.PanicLevel: 2,
		logrus.FatalLevel: 2,
		logrus.ErrorLevel: 3,
		logrus.WarnLevel:  4,
		logrus.InfoLevel:  6,
		logrus.DebugLevel: 7,
		logrus.TraceLevel: 7,
	}
	for level, want := range tests {
		assert.Equal(t, want, Priority(level), level.String())
	}
}

func TestFieldName(t *testing.T) {
	tests := map[string]string{
		"meter":         "METER",
		"Meter_id":      "METER_ID",
		"meter-id":      "METER_ID",
		"a.b c":         "A_B_C",
		"_SYSTEMD_UNIT": "SYSTEMD_UNIT",
		"__cursor":      "CURSOR",
		"åäö":           "",
		"___":           "",
		"1abc":          "",
		"message":       "",
		"priority":      "",
	}
	for key, want := range tests {
		assert.Equal(t, want, FieldName(key), key)
	}
}

type sent struct {
	message  string
	priority journal.Priority
	vars     map[string]string
}

func TestHookSendsEntry(t *testing.T) {
	var got []sent
	h := newHook(func(message string, priority journal.Priority, vars map[string]string) error {
		got = append(got, sent{message, priority, vars})
		return nil
	}, &bytes.Buffer{})
	h.identifier = "nergycontroller"

	logger := logrus.New()
	logger.SetOutput(&bytes.Buffer{})
	logger.AddHook(h)
	logger.WithField("meter", "abc").WithField("_PID", 1).Warn("meter unreachable")

	assert.Equal(t, []sent{{
		message:  "meter unreachable",
		priority: journal.PriWarning,
		vars:     map[string]string{"METER": "abc", "PID": "1", "SYSLOG_IDENTIFIER": "nergycontroller"},
	}}, got)
}

func TestHookFallsBackToStderr(t *testing.T) {
	fallback := &bytes.Buffer{}
	h := newHook(func(string, journal.Priority, map[string]string) error {
		return errors.New("no socket")
	}, fallback)

	logger := logrus.New()
	logger.SetOutput(&bytes.Buffer{})
	logger.AddHook(h)
	logger.WithField("meter", "abc").Error("boom")

	assert.Contains(t, fallback.String(), "level=error")
	assert.Contains(t, fallback.String(), `msg=boom`)
	assert.Contains(t, fallback.String(), "meter=abc")
}
