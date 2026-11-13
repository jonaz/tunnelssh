package main

import (
	"os"
	"testing"

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
