//go:build windows

package claudecli

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeBrowserCaptureScript creates a batch script that captures the URL passed
// by the CLI's browser-opening mechanism instead of opening a real browser.
// Returns the script path (to set as BROWSER env) and the file where the
// captured URL will be written.
func writeBrowserCaptureScript(dir string) (scriptPath, urlFile string, err error) {
	urlFile = filepath.Join(dir, "browser_url")
	scriptPath = filepath.Join(dir, "browser.cmd")
	// The redirect target must be quoted: the temp path contains the
	// username, and an unquoted space there truncates the target and
	// silently drops the captured URL.
	content := fmt.Sprintf("@echo off\r\n<nul set /p=\"%%~1\" > \"%s\"\r\n", urlFile)
	if err := os.WriteFile(scriptPath, []byte(content), 0700); err != nil {
		return "", "", fmt.Errorf("write browser capture script: %w", err)
	}
	return scriptPath, urlFile, nil
}
