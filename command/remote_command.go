package command

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/10gen-labs/slogger/v1"
	"github.com/evergreen-ci/evergreen"
)

type RemoteCommand struct {
	CmdString string

	Stdout io.Writer
	Stderr io.Writer

	// info necessary for sshing into the remote host
	RemoteHostName string
	User           string
	Options        []string
	Background     bool

	// optional flag for hiding sensitive commands from log output
	LoggingDisabled bool

	// set after the command is started
	Cmd *exec.Cmd
}

func (rc *RemoteCommand) Run() error {
	err := rc.Start()
	if err != nil {
		return err
	}
	return rc.Cmd.Wait()
}

func (rc *RemoteCommand) Wait() error {
	return rc.Cmd.Wait()
}

func (rc *RemoteCommand) Start() error {

	// build the remote connection, in user@host format
	remote := rc.RemoteHostName
	if rc.User != "" {
		remote = fmt.Sprintf("%v@%v", rc.User, remote)
	}

	// build the command
	cmdArray := append(rc.Options, remote)

	// set to the background, if necessary
	cmdString := rc.CmdString
	if rc.Background {
		cmdString = fmt.Sprintf("nohup %v > /tmp/start 2>&1 &", cmdString)
	}
	cmdArray = append(cmdArray, cmdString)

	if !rc.LoggingDisabled {
		evergreen.Logger.Logf(slogger.WARN, "Remote command executing: '%#v'",
			strings.Join(cmdArray, " "))
	}

	// set up execution
	cmd := exec.Command("ssh", cmdArray...)
	cmd.Stdout = rc.Stdout
	cmd.Stderr = rc.Stderr

	// cache the command running
	rc.Cmd = cmd
	return cmd.Start()
}

func (rc *RemoteCommand) Stop() error {
	if rc.Cmd != nil && rc.Cmd.Process != nil {
		return rc.Cmd.Process.Kill()
	}
	evergreen.Logger.Logf(slogger.WARN, "Trying to stop command but Cmd / Process was nil")
	return nil
}
