// Command authtest exercises the AuthLogin flow with full debug logging.
//
// Usage:
//
//	go run ./cmd/authtest
//	go run ./cmd/authtest -method console
//	go run ./cmd/authtest -status-only
//	go run ./cmd/authtest -config-dir /tmp/acct   # sign in a throwaway account
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	claudecli "github.com/allbin/claudecli-go"
)

func main() {
	method := flag.String("method", "", "auth method: claudeai or console")
	statusOnly := flag.Bool("status-only", false, "only check auth status, don't attempt login")
	configDir := flag.String("config-dir", "", "CLAUDE_CONFIG_DIR for the CLI (default: the process's)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var defaults []claudecli.Option
	if *configDir != "" {
		defaults = append(defaults, claudecli.WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": *configDir}))
	}
	client := claudecli.NewClient([]claudecli.ClientOption{claudecli.WithLogger(logger)}, defaults...)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Step 1: Check current auth status.
	fmt.Fprintln(os.Stderr, "\n=== Auth Status ===")
	status, err := client.AuthStatus(ctx)
	if err != nil {
		logger.Error("AuthStatus failed", "error", err)
	} else {
		logger.Info("auth status result",
			"status", status.Status,
			"message", status.Message,
			"loggedIn", status.LoggedIn,
			"authMethod", status.AuthMethod,
			"apiProvider", status.APIProvider,
			"email", status.Email,
			"orgName", status.OrgName,
			"subscriptionType", status.SubscriptionType,
		)
	}

	if *statusOnly {
		return
	}

	// Step 2: Start login flow with WithNoBrowser.
	fmt.Fprintln(os.Stderr, "\n=== Auth Login (WithNoBrowser) ===")

	var opts []claudecli.AuthLoginOption
	opts = append(opts, claudecli.WithNoBrowser())
	switch *method {
	case "console":
		opts = append(opts, claudecli.WithAuthMethod(claudecli.AuthMethodConsole))
	case "claudeai":
		opts = append(opts, claudecli.WithAuthMethod(claudecli.AuthMethodClaudeAI))
	case "":
		// default
	default:
		logger.Error("unknown method", "method", *method)
		os.Exit(1)
	}

	proc, err := client.AuthLogin(ctx, opts...)
	if err != nil {
		logger.Error("AuthLogin failed", "error", err)
		os.Exit(1)
	}
	if proc == nil {
		logger.Info("AuthLogin returned nil — already logged in")
		return
	}

	logger.Info("login process started",
		"manualURL", proc.URL,
		"autoOpenURL", proc.AutoOpenURL,
		"callbackPort", proc.CallbackPort(),
	)

	fmt.Println()
	fmt.Println("Open this URL on any device, authorize, then paste the code Claude's page shows:")
	fmt.Println(proc.URL)
	if proc.AutoOpenURL != "" {
		fmt.Println()
		fmt.Println("Or open this one; if the localhost page fails to load, paste its address:")
		fmt.Println(proc.AutoOpenURL)
	}
	fmt.Print("> ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		logger.Error("stdin read failed", "error", err)
		_ = proc.Cancel()
		return
	}

	fmt.Fprintln(os.Stderr, "\n=== Submit ===")
	submitCtx, submitCancel := context.WithTimeout(ctx, 60*time.Second)
	err = proc.Submit(submitCtx, line)
	submitCancel()
	var le *claudecli.LoginError
	switch {
	case err == nil:
		logger.Info("Submit: login successful")
	case errors.As(err, &le):
		logger.Error("Submit: CLI rejected the code", "message", le.Message, "retryable", le.Retryable)
	default:
		logger.Error("Submit failed", "error", err)
	}

	fmt.Fprintln(os.Stderr, "\n=== Waiting ===")
	waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
	defer waitCancel()

	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			logger.Warn("Wait returned error", "error", err)
		} else {
			logger.Info("login process completed OK")
		}
	case <-waitCtx.Done():
		logger.Warn("timed out waiting for login process (30s)")
		_ = proc.Cancel()
	}

	// Step 3: Verify auth status after login.
	fmt.Fprintln(os.Stderr, "\n=== Post-Login Auth Status ===")
	status, err = client.AuthStatus(context.Background())
	if err != nil {
		logger.Error("post-login AuthStatus failed", "error", err)
	} else {
		logger.Info("post-login auth status",
			"status", status.Status,
			"loggedIn", status.LoggedIn,
			"email", status.Email,
		)
	}
}
