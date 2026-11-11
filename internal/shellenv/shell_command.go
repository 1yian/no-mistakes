package shellenv

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"
)

const groupProbeInterval = 25 * time.Millisecond

const groupMemberReportLimit = 4

const groupMemberCommandLimit = 120

type groupMember struct {
	pid     int
	command string
}

func formatGroupMembers(members []groupMember) string {
	names := make([]string, 0, min(len(members), groupMemberReportLimit)+1)
	for i, m := range members {
		if i == groupMemberReportLimit {
			names = append(names, fmt.Sprintf("and %d more", len(members)-i))
			break
		}
		command := m.command
		if len(command) > groupMemberCommandLimit {
			command = command[:groupMemberCommandLimit] + "..."
		}
		if command == "" {
			names = append(names, fmt.Sprintf("pid %d", m.pid))
		} else {
			names = append(names, fmt.Sprintf("pid %d (%s)", m.pid, command))
		}
	}
	return strings.Join(names, ", ")
}

type shellOutputPipe struct {
	reader *os.File
	writer *os.File
	dst    io.Writer
}

func RunShellCommand(cmd *exec.Cmd) error {
	pipes, err := prepareShellOutputPipes(cmd)
	if err != nil {
		return err
	}
	if len(pipes) > 0 {
		return runShellCommandWithOutputPipes(cmd, pipes)
	}
	oomBase, oomOK := OOMKillBaseline()
	if err := StartShellCommand(cmd); err != nil {
		return err
	}
	defer TerminateShellCommandGroup(cmd)
	return AttributeOOMKill(oomBase, oomOK, cmd.Wait())
}

func OutputShellCommand(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := RunShellCommand(cmd)
	return stdout.Bytes(), err
}

func CombinedOutputShellCommand(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if cmd.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := RunShellCommand(cmd)
	return output.Bytes(), err
}

func prepareShellOutputPipes(cmd *exec.Cmd) ([]shellOutputPipe, error) {
	var pipes []shellOutputPipe
	stdout := cmd.Stdout
	stderr := cmd.Stderr

	if stdout != nil && !isShellOutputFile(stdout) {
		pipe, err := newShellOutputPipe(stdout)
		if err != nil {
			return nil, err
		}
		cmd.Stdout = pipe.writer
		pipes = append(pipes, pipe)
		if sameShellOutputWriter(stdout, stderr) {
			cmd.Stderr = pipe.writer
			return pipes, nil
		}
	}

	if stderr != nil && !isShellOutputFile(stderr) {
		pipe, err := newShellOutputPipe(stderr)
		if err != nil {
			closeShellOutputPipes(pipes)
			return nil, err
		}
		cmd.Stderr = pipe.writer
		pipes = append(pipes, pipe)
	}

	return pipes, nil
}

func newShellOutputPipe(dst io.Writer) (shellOutputPipe, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return shellOutputPipe{}, err
	}
	return shellOutputPipe{reader: reader, writer: writer, dst: dst}, nil
}

func runShellCommandWithOutputPipes(cmd *exec.Cmd, pipes []shellOutputPipe) error {
	oomBase, oomOK := OOMKillBaseline()
	if err := StartShellCommand(cmd); err != nil {
		closeShellOutputPipes(pipes)
		return err
	}
	for _, pipe := range pipes {
		_ = pipe.writer.Close()
	}

	copyCh := make(chan error, len(pipes))
	for _, pipe := range pipes {
		go func(pipe shellOutputPipe) {
			_, err := io.Copy(pipe.dst, pipe.reader)
			_ = pipe.reader.Close()
			copyCh <- err
		}(pipe)
	}

	waitCh := make(chan error, 1)
	go func() {
		err := AttributeOOMKill(oomBase, oomOK, cmd.Wait())
		TerminateShellCommandGroup(cmd)
		waitCh <- err
	}()

	var waitErr error
	waitDone := false
	remainingCopies := len(pipes)
	var copyErr error
	var waitDelay <-chan time.Time
	var waitDelayTimer *time.Timer
	waitDelayExpired := false
	for !waitDone || remainingCopies > 0 {
		select {
		case err := <-waitCh:
			waitErr = err
			waitDone = true
			if remainingCopies > 0 && cmd.WaitDelay > 0 {
				waitDelayTimer = time.NewTimer(cmd.WaitDelay)
				waitDelay = waitDelayTimer.C
			}
		case err := <-copyCh:
			remainingCopies--
			if err != nil && copyErr == nil {
				copyErr = err
				TerminateShellCommandGroup(cmd)
			}
		case <-waitDelay:
			waitDelayExpired = true
			waitDelay = nil
			closeShellOutputPipeReaders(pipes)
		}
	}
	if waitDelayTimer != nil {
		waitDelayTimer.Stop()
	}
	if waitErr != nil {
		return waitErr
	}
	if waitDelayExpired {
		return exec.ErrWaitDelay
	}
	return copyErr
}

func closeShellOutputPipes(pipes []shellOutputPipe) {
	for _, pipe := range pipes {
		_ = pipe.reader.Close()
		_ = pipe.writer.Close()
	}
}

func closeShellOutputPipeReaders(pipes []shellOutputPipe) {
	for _, pipe := range pipes {
		_ = pipe.reader.Close()
	}
}

func isShellOutputFile(w io.Writer) bool {
	_, ok := w.(*os.File)
	return ok
}

func sameShellOutputWriter(a, b io.Writer) bool {
	if a == nil || b == nil {
		return false
	}
	av := reflect.ValueOf(a)
	bv := reflect.ValueOf(b)
	if av.Type() != bv.Type() || !av.Type().Comparable() {
		return false
	}
	return av.Equal(bv)
}
