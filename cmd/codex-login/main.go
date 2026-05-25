package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tursom/turapis/internal/codexauth"
)

type loginOutput struct {
	Email      string          `json:"email"`
	AccountID  string          `json:"account_id"`
	UserID     string          `json:"user_id"`
	PlanType   string          `json:"plan_type"`
	Credential json.RawMessage `json:"credential"`
}

func formatOutput(result *codexauth.FlowResult) ([]byte, error) {
	credJSON, err := codexauth.TokenSetToCredentialJSON(result.Tokens)
	if err != nil {
		return nil, fmt.Errorf("credential conversion: %w", err)
	}

	out := loginOutput{
		Email:      result.Identity.Email,
		AccountID:  result.Identity.AccountID,
		UserID:     result.Identity.UserID,
		PlanType:   result.Identity.PlanType,
		Credential: credJSON,
	}

	return json.MarshalIndent(out, "", "  ")
}

func main() {
	port := flag.Int("port", 1455, "OAuth callback listener port")
	timeout := flag.Duration("timeout", 5*time.Minute, "max wait for OAuth callback")
	flag.Parse()

	cfg := codexauth.DefaultFlowConfig()
	cfg.CallbackPort = *port

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	flow := codexauth.NewAutoLoginFlow(cfg)
	authURL, waitFn, err := flow.StartLogin(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start login: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Open this URL in your browser to authorize:\n\n%s\n\n", authURL)

	waitCtx, waitCancel := context.WithTimeout(ctx, *timeout)
	defer waitCancel()

	result, err := waitFn(waitCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
		os.Exit(1)
	}

	output, err := formatOutput(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to format output: %v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(output)
	fmt.Fprintln(os.Stdout)

	fmt.Fprintf(os.Stderr, "\nLogged in as: %s (%s)\nAccount: %s\nPlan: %s\n",
		result.Identity.Email, result.Identity.UserID,
		result.Identity.AccountID, result.Identity.PlanType)
}
