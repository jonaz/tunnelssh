package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

// runMaster runs the real flag parsing of the master command, with its
// action replaced so no server starts, and returns the token it saw.
func runMaster(t *testing.T, args ...string) (string, error) {
	t.Helper()
	a := app()
	var token string
	for _, c := range a.Commands {
		if c.Name == "master" {
			c.Action = func(c *cli.Context) error {
				token = c.String("token")
				return nil
			}
		}
	}
	err := a.Run(append([]string{"tunnelssh", "master"}, args...))
	return token, err
}

func TestMasterTokenFromEnv(t *testing.T) {
	t.Setenv("TUNNELSSH_TOKEN", "from-env")
	token, err := runMaster(t)
	if err != nil {
		t.Fatal(err)
	}
	if token != "from-env" {
		t.Fatalf("token = %q, want %q", token, "from-env")
	}
}

func TestMasterTokenFlagWinsOverEnv(t *testing.T) {
	t.Setenv("TUNNELSSH_TOKEN", "from-env")
	token, err := runMaster(t, "--token", "from-flag")
	if err != nil {
		t.Fatal(err)
	}
	if token != "from-flag" {
		t.Fatalf("token = %q, want %q", token, "from-flag")
	}
}

// An empty key signs and accepts JWTs anyone can make, so the master must
// not start without one.
func TestMasterRefusesNoToken(t *testing.T) {
	for name, args := range map[string][]string{
		"empty env":  nil,
		"empty flag": {"--token", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TUNNELSSH_TOKEN", "")
			if _, err := runMaster(t, args...); err == nil {
				t.Fatal("master started without a token")
			}
		})
	}
}

func TestMasterRefusesUnsetToken(t *testing.T) {
	t.Setenv("TUNNELSSH_TOKEN", "")
	os.Unsetenv("TUNNELSSH_TOKEN")
	if _, err := runMaster(t); err == nil {
		t.Fatal("master started without a token")
	}
}

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	f()
	w.Close()
	return <-out
}

func TestCompletion(t *testing.T) {
	for shell, want := range map[string]string{
		"bash": "complete -o bashdefault -o default -o nospace -F _cli_bash_autocomplete",
		"zsh":  "compdef _cli_zsh_autocomplete",
	} {
		t.Run(shell, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() {
				err = app().Run([]string{"tunnelssh", "completion", shell})
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, want) {
				t.Fatalf("output lacks %q:\n%s", want, out)
			}
		})
	}
}

func TestLogLevel(t *testing.T) {
	t.Cleanup(func() { logrus.SetLevel(logrus.InfoLevel) })

	if err := app().Run([]string{"tunnelssh", "--log-level", "loud", "completion", "bash"}); err == nil {
		t.Fatal("unknown log level accepted")
	}

	captureStdout(t, func() {
		if err := app().Run([]string{"tunnelssh", "--log-level", "debug", "completion", "bash"}); err != nil {
			t.Fatal(err)
		}
	})
	if logrus.GetLevel() != logrus.DebugLevel {
		t.Fatalf("level = %s, want debug", logrus.GetLevel())
	}
}

// The commands' actions run the real agent, proxy and master. With the
// context already cancelled each returns at once instead of running.
func TestCommandsStopWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		args    []string
		wantErr bool
	}{
		// The agent retries until the context ends, then returns its error.
		{[]string{"agent", "--master", "ws://127.0.0.1:1", "--id", "a1"}, true},
		// The proxy gives up on the failed dial.
		{[]string{"proxy", "--master", "ws://127.0.0.1:1", "--id", "a1", "--token", "t"}, true},
		// The master shuts down cleanly.
		{[]string{"master", "--token", "t", "--port", "0"}, false},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			err := app().RunContext(ctx, append([]string{"tunnelssh"}, tc.args...))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
