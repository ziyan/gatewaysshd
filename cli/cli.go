package cli

import (
	"context"
	"fmt"
	"os"

	logging "github.com/op/go-logging"
	"github.com/urfave/cli/v3"
)

var log = logging.MustGetLogger("cli")

func Run(version, commit string, arguments []string) {
	command := &cli.Command{
		Name:                  "gatewaysshd",
		Version:               fmt.Sprintf("%s.%s", version, commit),
		Usage:                 "A daemon that provides a meeting place for all your SSH tunnels.",
		EnableShellCompletion: true,
		Flags:                 flags,
		Before: func(ctx context.Context, command *cli.Command) (context.Context, error) {
			formatter := logging.MustStringFormatter("%{color}%{time:2006-01-02T15:04:05.000-07:00} [%{level}] <%{pid}> [%{shortfile} %{shortfunc}] %{message}%{color:reset}")
			logging.SetBackend(logging.NewBackendFormatter(logging.NewLogBackend(os.Stderr, "", 0), formatter))
			if level, err := logging.LogLevel(command.String("log-level")); err == nil {
				logging.SetLevel(level, "")
			}
			log.Debugf("log level set to %s", logging.GetLevel(""))
			log.Noticef("started %s version %s", command.Name, command.Version)
			return ctx, nil
		},
		After: func(ctx context.Context, command *cli.Command) error {
			log.Noticef("exiting %s version %s ...", command.Name, command.Version)
			return nil
		},
		Action: run,
	}
	_ = command.Run(context.Background(), arguments)
}
