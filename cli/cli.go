package cli

import (
	"fmt"
	"os"

	logging "github.com/op/go-logging"
	"github.com/urfave/cli"
)

var log = logging.MustGetLogger("cli")

func Run(version, commit string, args []string) {
	app := cli.NewApp()
	app.EnableBashCompletion = true
	app.Name = "gatewaysshd"
	app.Version = fmt.Sprintf("%s.%s", version, commit)
	app.Usage = "A daemon that provides a meeting place for all your SSH tunnels."
	app.Flags = flags
	app.Before = func(c *cli.Context) error {
		formatter := logging.MustStringFormatter("%{color}%{time:2006-01-02T15:04:05.000-07:00} [%{level}] <%{pid}> [%{shortfile} %{shortfunc}] %{message}%{color:reset}")
		logging.SetBackend(logging.NewBackendFormatter(logging.NewLogBackend(os.Stderr, "", 0), formatter))
		if level, err := logging.LogLevel(c.String("log-level")); err == nil {
			logging.SetLevel(level, "")
		}
		log.Debugf("log level set to %s", logging.GetLevel(""))
		log.Noticef("started %s version %s", app.Name, app.Version)
		return nil
	}
	app.After = func(c *cli.Context) error {
		log.Noticef("exiting %s version %s ...", app.Name, app.Version)
		return nil
	}
	app.Action = run
	_ = app.Run(args)
}
