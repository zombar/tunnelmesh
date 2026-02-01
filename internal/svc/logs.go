package svc

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

// LogOptions configures log viewing behavior.
type LogOptions struct {
	ServiceName string
	Follow      bool
	Lines       int
}

// ViewLogs displays service logs using platform-appropriate tools.
func ViewLogs(opts LogOptions) error {
	if opts.Lines <= 0 {
		opts.Lines = 50
	}

	switch runtime.GOOS {
	case "linux":
		return viewLogsLinux(opts)
	case "darwin":
		return viewLogsDarwin(opts)
	case "windows":
		return viewLogsWindows(opts)
	default:
		return fmt.Errorf("log viewing not supported on %s", runtime.GOOS)
	}
}

// viewLogsLinux uses journalctl to view systemd service logs.
func viewLogsLinux(opts LogOptions) error {
	args := []string{"-u", opts.ServiceName, "-n", strconv.Itoa(opts.Lines), "--no-pager"}
	if opts.Follow {
		args = append(args, "-f")
	}

	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// viewLogsDarwin uses the macOS log command to view launchd service logs.
func viewLogsDarwin(opts LogOptions) error {
	// On macOS, launchd services log to the unified logging system
	// We use the 'log' command to view them
	predicate := fmt.Sprintf("subsystem == \"com.tunnelmesh.%s\" OR process == \"tunnelmesh\"", opts.ServiceName)

	if opts.Follow {
		// Use 'log stream' for following
		args := []string{"stream", "--predicate", predicate, "--style", "compact"}
		cmd := exec.Command("log", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()
	}

	// Use 'log show' for historical logs
	// Estimate time based on lines (roughly 1 line per second for active service)
	minutes := opts.Lines / 10
	if minutes < 5 {
		minutes = 5
	}

	args := []string{
		"show",
		"--predicate", predicate,
		"--style", "compact",
		"--last", strconv.Itoa(minutes) + "m",
	}

	cmd := exec.Command("log", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// viewLogsWindows uses PowerShell to query Windows Event Log.
func viewLogsWindows(opts LogOptions) error {
	// On Windows, we query the Event Log using PowerShell
	// Services typically log to the Application log

	var psScript string
	if opts.Follow {
		// For follow mode, we poll the event log
		psScript = fmt.Sprintf(`
$lastTime = (Get-Date).AddMinutes(-5)
Write-Host "Following logs for %s (Ctrl+C to stop)..."
while ($true) {
    $events = Get-WinEvent -FilterHashtable @{
        LogName = 'Application'
        ProviderName = '%s'
        StartTime = $lastTime
    } -ErrorAction SilentlyContinue

    if ($events) {
        $events | Sort-Object TimeCreated | ForEach-Object {
            Write-Host "$($_.TimeCreated) [$($_.LevelDisplayName)] $($_.Message)"
        }
        $lastTime = ($events | Sort-Object TimeCreated | Select-Object -Last 1).TimeCreated.AddSeconds(1)
    }
    Start-Sleep -Seconds 2
}
`, opts.ServiceName, opts.ServiceName)
	} else {
		psScript = fmt.Sprintf(`
$events = Get-WinEvent -FilterHashtable @{
    LogName = 'Application'
    ProviderName = '%s'
} -MaxEvents %d -ErrorAction SilentlyContinue

if ($events) {
    $events | Format-Table -Property TimeCreated, LevelDisplayName, Message -AutoSize -Wrap
} else {
    Write-Host "No log entries found for service '%s'"
    Write-Host "Try checking Event Viewer > Windows Logs > Application"
}
`, opts.ServiceName, opts.Lines, opts.ServiceName)
	}

	cmd := exec.Command("powershell", "-NoProfile", "-Command", psScript)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	if err != nil {
		// If PowerShell fails, give user manual instructions
		fmt.Fprintf(os.Stderr, "\nCould not read Windows Event Log automatically.\n")
		fmt.Fprintf(os.Stderr, "To view logs manually:\n")
		fmt.Fprintf(os.Stderr, "  1. Open Event Viewer (eventvwr.msc)\n")
		fmt.Fprintf(os.Stderr, "  2. Navigate to Windows Logs > Application\n")
		fmt.Fprintf(os.Stderr, "  3. Filter by Source: %s\n", opts.ServiceName)
		return err
	}

	return nil
}
