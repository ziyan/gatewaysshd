package debugutil

import (
	"bytes"
	"runtime/pprof"

	logging "github.com/op/go-logging"
)

var log = logging.MustGetLogger("debugutil")

func GetAllStacks() string {
	var buffer bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buffer, 1); err != nil {
		log.Errorf("failed to get all stacks: %s", err)
	}
	return buffer.String()
}
