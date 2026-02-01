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

// viewLogsDarwin reads from the log files created by launchd.
func viewLogsDarwin(opts LogOptions) error {
	// launchd services log to files in /var/log/
	outLog := fmt.Sprintf("/var/log/%s.out.log", opts.ServiceName)
	errLog := fmt.Sprintf("/var/log/%s.err.log", opts.ServiceName)

	if opts.Follow {
		// Use tail -f to follow both log files
		args := []string{"-f", outLog, errLog}
		cmd := exec.Command("tail", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()
	}

	// Show last N lines from both files combined
	// First check if files exist
	outExists := fileExists(outLog)
	errExists := fileExists(errLog)

	if !outExists && !errExists {
		fmt.Printf("No log files found for service %q\n", opts.ServiceName)
		fmt.Printf("Expected log files:\n")
		fmt.Printf("  - %s\n", outLog)
		fmt.Printf("  - %s\n", errLog)
		return nil
	}

	// Show stderr first (errors), then stdout
	if errExists {
		fmt.Println("=== Errors ===")
		cmd := exec.Command("tail", "-n", strconv.Itoa(opts.Lines), errLog)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}

	if outExists {
		if errExists {
			fmt.Println("\n=== Output ===")
		}
		cmd := exec.Command("tail", "-n", strconv.Itoa(opts.Lines), outLog)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}

	return nil
}

// fileExists checks if a file exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
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
