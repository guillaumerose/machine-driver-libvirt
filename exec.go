package libvirt

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/code-ready/machine/libmachine/log"
)

func virsh(args ...string) *exec.Cmd {
	// #nosec G204
	cmd := exec.Command("virsh", append([]string{"--connect", connectionString}, args...)...)
	cmd.Stderr = os.Stderr
	return cmd
}

func execute(cmd *exec.Cmd, timeout <-chan time.Time) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	log.Infof("Running '%s %s'", cmd.Path, strings.Join(cmd.Args[1:], " ")) // skip arg[0] as it is printed separately
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("error starting %v:\nCommand stdout:\n%v\nstderr:\n%v\nerror:\n%v", cmd, cmd.Stdout, cmd.Stderr, err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()
	select {
	case err := <-errCh:
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				rc := int(ee.Sys().(syscall.WaitStatus).ExitStatus())
				log.Infof("rc: %d", rc)
			}
			return stdout.String(), fmt.Errorf("error running %v:\nCommand stdout:\n%v\nstderr:\n%v\nerror:\n%v", cmd, cmd.Stdout, cmd.Stderr, err)
		}
	case <-timeout:
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("timed out waiting for command %v:\nCommand stdout:\n%v\nstderr:\n%v", cmd, cmd.Stdout, cmd.Stderr)
	}
	log.Infof("stderr: %q", stderr.String())
	log.Infof("stdout: %q", stdout.String())
	return stdout.String(), nil
}
