package daemon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func installWindowsTask(p *paths.Paths, exe string) error {
	current, err := serviceCurrentUser()
	if err != nil {
		return fmt.Errorf("resolve current user for scheduled task: %w", err)
	}
	account := current.Username // DOMAIN\user on Windows
	if strings.TrimSpace(account) == "" {
		return errors.New("resolve current user for scheduled task: empty account name")
	}
	definition, err := os.CreateTemp("", "no-mistakes-task-*.xml")
	if err != nil {
		return fmt.Errorf("write scheduled task definition: %w", err)
	}
	defer os.Remove(definition.Name())
	_, writeErr := definition.Write(encodeUTF16LEWithBOM(renderWindowsTaskXML(exe, p.Root(), account)))
	if closeErr := definition.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fmt.Errorf("write scheduled task definition: %w", writeErr)
	}
	args := []string{
		"/Create",
		"/TN", windowsTaskName(p),
		"/XML", definition.Name(),
		"/F",
	}
	if _, err := serviceCommandRunner("schtasks", args...); err != nil {
		return fmt.Errorf("schtasks create: %w", err)
	}
	cleanupLegacyWindowsTask(p)
	return nil
}

func renderWindowsTaskXML(exe, root, account string) string {
	arguments := "daemon run --root " + quoteWindowsTaskArg(root)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + xmlEscaped(account) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + xmlEscaped(account) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <IdleSettings>
      <Duration>PT10M</Duration>
      <WaitTimeout>PT1H</WaitTimeout>
      <StopOnIdleEnd>true</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscaped(exe) + `</Command>
      <Arguments>` + xmlEscaped(arguments) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

func encodeUTF16LEWithBOM(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for i, unit := range units {
		binary.LittleEndian.PutUint16(out[2+2*i:], unit)
	}
	return out
}

func cleanupLegacyWindowsTask(p *paths.Paths) {
	data, err := serviceCommandRunner("schtasks", "/Query", "/TN", legacyWindowsTaskName, "/XML")
	if err != nil || !serviceDefinitionMatchesRoot(data, p) {
		return
	}
	_, _ = serviceCommandRunner("schtasks", "/End", "/TN", legacyWindowsTaskName)
	_, _ = serviceCommandRunner("schtasks", "/Delete", "/TN", legacyWindowsTaskName, "/F")
}

func startWindowsTask(p *paths.Paths) error {
	_, err := serviceCommandRunner("schtasks", "/Run", "/TN", windowsTaskName(p))
	if err != nil {
		return fmt.Errorf("schtasks run: %w", err)
	}
	return nil
}

func stopWindowsTask(p *paths.Paths) error {
	_, err := serviceCommandRunner("schtasks", "/End", "/TN", windowsTaskName(p))
	if err != nil {
		return fmt.Errorf("schtasks end: %w", err)
	}
	return nil
}

type windowsManagedDaemonObservation struct {
	state         int
	runGeneration string
}

func inspectWindowsManagedDaemon(p *paths.Paths) (windowsManagedDaemonObservation, error) {
	taskName := strings.ReplaceAll(windowsTaskName(p), "'", "''")
	command := "$task=Get-ScheduledTask -TaskName '" + taskName + "';$info=Get-ScheduledTaskInfo -TaskName '" + taskName + "';Write-Output \"$([int]$task.State)|$($info.LastRunTime.Ticks)|$($info.LastTaskResult)\""
	output, err := serviceCommandRunner(
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		command,
	)
	if err != nil {
		return windowsManagedDaemonObservation{}, err
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(fields) != 3 {
		return windowsManagedDaemonObservation{}, fmt.Errorf("parse scheduled task state: unexpected field count %d", len(fields))
	}
	state, err := strconv.Atoi(fields[0])
	if err != nil {
		return windowsManagedDaemonObservation{}, fmt.Errorf("parse scheduled task state: %w", err)
	}
	if _, err := strconv.ParseInt(fields[1], 10, 64); err != nil {
		return windowsManagedDaemonObservation{}, fmt.Errorf("parse scheduled task last run: %w", err)
	}
	if _, err := strconv.ParseUint(fields[2], 10, 32); err != nil {
		return windowsManagedDaemonObservation{}, fmt.Errorf("parse scheduled task result: %w", err)
	}
	return windowsManagedDaemonObservation{state: state, runGeneration: fields[1]}, nil
}

func windowsManagedDaemonState(p *paths.Paths, launch managedServiceLaunch) (managedServiceState, error) {
	observation, err := inspectWindowsManagedDaemon(p)
	if err != nil {
		return managedServiceUnknown, err
	}
	switch observation.state {
	case 4:
		return managedServiceRunning, nil
	case 1:
		return managedServiceExited, nil
	case 3:
		if launch.windowsRunGeneration != "" && observation.runGeneration != launch.windowsRunGeneration {
			return managedServiceExited, nil
		}
		return managedServiceUnknown, nil
	default:
		return managedServiceUnknown, nil
	}
}

func quoteWindowsTaskArg(arg string) string {
	if !strings.ContainsAny(arg, " \t\"") {
		return arg
	}
	return strconv.Quote(arg)
}
