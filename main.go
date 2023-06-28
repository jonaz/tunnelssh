package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jonaz/tunnelssh/pkg/agent"
	"github.com/jonaz/tunnelssh/pkg/master"
	"github.com/jonaz/tunnelssh/pkg/proxy"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

var (
	// TODO set on build
	Version     = "dev"
	BuildTime   = ""
	BuildCommit = ""
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM)
	defer stop()
	err := app().RunContext(ctx, os.Args)
	if err != nil {
		logrus.Error(err)
		os.Exit(1)
	}
}

func app() *cli.App {
	app := cli.NewApp()
	app.Name = "tunnelssh"
	app.Usage = "tunnelssh, tunnel ssh over websocket in reverse"
	app.Version = fmt.Sprintf(`Version: "%s", BuildTime: "%s", Commit: "%s"`, Version, BuildTime, BuildCommit)
	app.Before = globalBefore
	app.EnableBashCompletion = true
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "log-level",
			Value: "info",
			Usage: "available levels are: " + strings.Join(getLevels(), ","),
		},
	}

	app.Commands = []*cli.Command{
		{
			Name:  "agent",
			Usage: "starts the agent and connect to the master websocket",
			Action: func(c *cli.Context) error {
				agent := agent.NewAgentFromContext(c)
				return agent.Run(c.Context)
			},
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "master",
					Usage: "which master to connect to.",
				},
				&cli.StringFlag{
					Name:  "id",
					Usage: "agent id to present",
				},
				&cli.StringFlag{
					Name:  "id-file",
					Usage: "agent id from file",
				},
				&cli.StringFlag{
					Name:  "target",
					Value: "127.0.0.1:22",
					Usage: "ip:port to tunnel to",
				},
			},
		},
		{
			Name:  "proxy",
			Usage: "connect to a agent through the master",
			Action: func(c *cli.Context) error {
				proxy := proxy.NewProxyFromContext(c)
				return proxy.Run(c.Context)
			},
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "master",
					Usage: "which master to connect to.",
				},
				&cli.StringFlag{
					Name:  "token",
					Usage: "secret to connect to the master",
				},
				&cli.StringFlag{
					Name:  "id",
					Usage: "agent id to connect to",
				},
			},
		},
		{
			Name:  "master",
			Usage: "starts the master that syncs from git and decodes secrets.",
			Action: func(c *cli.Context) error {
				master := master.NewMasterFromContext(c)
				return master.Run(c.Context)
			},
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "token",
					Usage: "used to sign the jwt's",
				},
				&cli.StringFlag{
					Name:  "port",
					Value: "8080",
					Usage: "webserver port to listen to",
				},
			},
		},
		{
			Name:  "completion",
			Usage: "generate completion for shells",
			Subcommands: []*cli.Command{
				{
					Name:   "bash",
					Usage:  "put in .bashrc: 'source <(" + os.Args[0] + " completion bash)'",
					Action: bashCompletion,
				},
				{
					Name:   "zsh",
					Usage:  "put in .zshrc: 'source <(" + os.Args[0] + " completion zsh)'",
					Action: zshCompletion,
				},
			},
		},
	}
	return app
}

func globalBefore(c *cli.Context) error {
	logrus.SetFormatter(&logrus.TextFormatter{TimestampFormat: time.RFC3339Nano, FullTimestamp: true})
	lvl, err := logrus.ParseLevel(c.String("log-level"))
	if err != nil {
		return err
	}
	if lvl != logrus.InfoLevel {
		fmt.Fprintf(os.Stderr, "using loglevel: %s\n", lvl.String())
	}
	logrus.SetLevel(lvl)
	return nil
}

func bashCompletion(_ *cli.Context) error {
	binaryName := os.Args[0]
	script := fmt.Sprintf(`#!/bin/bash
_cli_bash_autocomplete() {
  if [[ "${COMP_WORDS[0]}" != "source" ]]; then
    local cur opts base
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [[ "$cur" == "-"* ]]; then
      opts=$( ${COMP_WORDS[@]:0:$COMP_CWORD} ${cur} --generate-bash-completion )
    else
      opts=$( ${COMP_WORDS[@]:0:$COMP_CWORD} --generate-bash-completion )
    fi
    COMPREPLY=( $(compgen -W "${opts}" -- ${cur}) )
    return 0
  fi
}
complete -o bashdefault -o default -o nospace -F _cli_bash_autocomplete %s
`, binaryName)
	fmt.Print(script)
	// fmt.Println(os.Args)
	return nil
}

func zshCompletion(_ *cli.Context) error {
	binaryName := os.Args[0]
	script := fmt.Sprintf(`#!/bin/zsh
_cli_zsh_autocomplete() {
  local -a opts
  local cur
  cur=${words[-1]}
  if [[ "$cur" == "-"* ]]; then
    opts=("${(@f)$(${words[@]:0:#words[@]-1} ${cur} --generate-bash-completion)}")
  else
    opts=("${(@f)$(${words[@]:0:#words[@]-1} --generate-bash-completion)}")
  fi
  if [[ "${opts[1]}" != "" ]]; then
    _describe 'values' opts
  else
    _files
  fi
}
compdef _cli_zsh_autocomplete %s
`, binaryName)
	fmt.Print(script)
	// fmt.Println(os.Args)
	return nil
}
func getLevels() []string {
	lvls := make([]string, len(logrus.AllLevels))
	for k, v := range logrus.AllLevels {
		lvls[k] = v.String()
	}
	return lvls
}
