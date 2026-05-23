package codexauth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

func pythonCodexBrowserLogin(ctx context.Context, authURL, proxyURL, email, password string, callbackPort int, getCode func() (string, error), getPhone func() (string, error), getSMSCode func(string) (string, error)) (string, error) {
	scriptPath := "/usr/local/bin/codex_oauth_browser"

	args := []string{
		"--callback-port", fmt.Sprintf("%d", callbackPort),
		"--auth-url", authURL,
		"--timeout", "120",
	}
	if email != "" {
		args = append(args, "--email", email)
	}
	if password != "" {
		args = append(args, "--password", password)
	}
	if proxyURL != "" {
		args = append(args, "--proxy", proxyURL)
	}

	cmd := exec.CommandContext(ctx, scriptPath, args...)

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start python: %w", err)
	}

	codeReady := make(chan struct{})
	var stderrLines []string
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			stderrLines = append(stderrLines, line)
			if strings.Contains(line, "waiting_for_code") {
				close(codeReady)
			}
		}
	}()

	if email != "" {
		// Start polling for the verification code immediately (before Python
		// even enters the email), so we don't miss the email if it arrives
		// during the few seconds Python takes to navigate.
		type codeResult struct {
			code string
			err  error
		}
		codeCh := make(chan codeResult, 1)
		go func() {
			c, err := getCode()
			codeCh <- codeResult{c, err}
		}()

		select {
		case <-codeReady:
		case <-ctx.Done():
			stderrStr := strings.Join(stderrLines, "\n")
			return "", fmt.Errorf("python login: %w (stderr so far: %s)", ctx.Err(), stderrStr)
		}
		// Python is now waiting for the code; get it from the polling goroutine
		select {
		case cr := <-codeCh:
			if cr.err != nil {
				return "", fmt.Errorf("get verification code: %w", cr.err)
			}
			code := cr.code
			codeURL := fmt.Sprintf("http://127.0.0.1:%d/auth/code", callbackPort)
			req, _ := http.NewRequestWithContext(ctx, "POST", codeURL, strings.NewReader(code))
			req.Header.Set("Content-Type", "text/plain")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return "", fmt.Errorf("post verification code: %w", err)
			}
			resp.Body.Close()
		case <-ctx.Done():
			stderrStr := strings.Join(stderrLines, "\n")
			return "", fmt.Errorf("waiting for verification code: %w (stderr: %s)", ctx.Err(), stderrStr)
		}
	}

	if getPhone != nil {
		phoneReady := make(chan struct{})
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				time.Sleep(100 * time.Millisecond)
				all := strings.Join(stderrLines, "\n")
				if strings.Contains(all, "waiting_for_phone_number") {
					close(phoneReady)
					return
				}
			}
		}()
		select {
		case <-phoneReady:
		case <-ctx.Done():
			stderrStr := strings.Join(stderrLines, "\n")
			return "", fmt.Errorf("python login: %w (stderr: %s)", ctx.Err(), stderrStr)
		}
		phone, err := getPhone()
		if err != nil {
			return "", fmt.Errorf("get phone number: %w", err)
		}
		phoneURL := fmt.Sprintf("http://127.0.0.1:%d/auth/code", callbackPort)
		req, _ := http.NewRequestWithContext(ctx, "POST", phoneURL, strings.NewReader(phone))
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("post phone number: %w", err)
		}
		resp.Body.Close()

		if getSMSCode != nil {
			smsReady := make(chan struct{})
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					default:
					}
					time.Sleep(100 * time.Millisecond)
					all := strings.Join(stderrLines, "\n")
					if strings.Contains(all, "waiting_for_sms_code") {
						close(smsReady)
						return
					}
				}
			}()
			select {
			case <-smsReady:
			case <-ctx.Done():
				stderrStr := strings.Join(stderrLines, "\n")
				return "", fmt.Errorf("python login: %w (stderr: %s)", ctx.Err(), stderrStr)
			}
			code, err := getSMSCode(phone)
			if err != nil {
				return "", fmt.Errorf("get sms code: %w", err)
			}
			codeURL := fmt.Sprintf("http://127.0.0.1:%d/auth/code", callbackPort)
			req2, _ := http.NewRequestWithContext(ctx, "POST", codeURL, strings.NewReader(code))
			req2.Header.Set("Content-Type", "text/plain")
			resp2, err := http.DefaultClient.Do(req2)
			if err != nil {
				return "", fmt.Errorf("post sms code: %w", err)
			}
			resp2.Body.Close()
		}
	}

	if err := cmd.Wait(); err != nil {
		stderrStr := strings.Join(stderrLines, "\n")
		return "", fmt.Errorf("python codex auth: %w (stdout: %s, stderr: %s)", err, stdout.String(), stderrStr)
	}

	var result struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return "", fmt.Errorf("parse python output: %w (stdout: %s)", err, stdout.String())
	}
	if result.Error != "" {
		return "", fmt.Errorf("python codex auth: %s", result.Error)
	}
	if result.Code == "" {
		return "", fmt.Errorf("python codex auth: no code in output (stdout: %s)", stdout.String())
	}

	return result.Code, nil
}

func marshalCookiesForPython(cookies []*http.Cookie) (string, error) {
	type cookieJSON struct {
		Name     string `json:"name"`
		Value    string `json:"value"`
		Domain   string `json:"domain"`
		Path     string `json:"path"`
		HttpOnly bool   `json:"httpOnly"`
		Secure   bool   `json:"secure"`
	}
	out := make([]cookieJSON, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, cookieJSON{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			HttpOnly: c.HttpOnly,
			Secure:   c.Secure,
		})
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}
