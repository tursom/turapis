package sms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

const fiveSimBaseURL = "https://5sim.net/v1"

// FiveSim implements SMSProvider for 5sim.net.
type FiveSim struct {
	apiKey     string
	httpClient *http.Client
}

// NewFiveSim creates a new FiveSim SMS provider.
func NewFiveSim(cfg SMSProviderConfig) *FiveSim {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
			return d.DialContext(ctx, "tcp4", addr)
		},
	}

	if cfg.ProxyURL != "" {
		if u, err := url.Parse(cfg.ProxyURL); err == nil && u.Host != "" {
			switch u.Scheme {
			case "socks5":
				if dialer, err := proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct); err == nil {
					transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
						return dialer.Dial(network, addr)
					}
				}
			default:
				transport.Proxy = http.ProxyURL(u)
			}
		}
	}

	return &FiveSim{
		apiKey: cfg.APIKey,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

func (f *FiveSim) Name() string { return "5sim" }

func (f *FiveSim) GetNumber(ctx context.Context, service string) (*NumberInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/user/buy/activation/usa/any/%s", fiveSimBaseURL, service), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("5sim buy activation: %w", err)
	}
	defer resp.Body.Close()

	if resp.Body == nil {
		return nil, fmt.Errorf("5sim buy returned empty response")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("5sim buy read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("5sim buy returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID      int    `json:"id"`
		Phone   string `json:"phone"`
		Status  string `json:"status"`
		Product string `json:"product"`
		Price   int    `json:"price"`
		Expires string `json:"expires"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("5sim buy parse: %w (body: %s)", err, string(body))
	}
	if result.ID == 0 || result.Phone == "" {
		return nil, fmt.Errorf("5sim buy invalid response: %s", string(body))
	}

	return &NumberInfo{
		Number:       result.Phone,
		ActivationID: fmt.Sprintf("%d", result.ID),
		Service:      service,
		Provider:     "5sim",
		Extra: map[string]string{
			"price":   fmt.Sprintf("%d", result.Price),
			"expires": result.Expires,
		},
	}, nil
}

func (f *FiveSim) GetMessages(ctx context.Context, num *NumberInfo) ([]SMSMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/user/check/%s", fiveSimBaseURL, num.ActivationID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("5sim check: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("5sim check read body: %w", err)
	}

	var result struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
		SMS    []struct {
			ID        int    `json:"id"`
			CreatedAt string `json:"created_at"`
			Date      string `json:"date"`
			Sender    string `json:"sender"`
			Text      string `json:"text"`
			Code      string `json:"code"`
		} `json:"sms"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("5sim check parse: %w (body: %s)", err, string(body))
	}

	var msgs []SMSMessage
	for _, s := range result.SMS {
		msgs = append(msgs, SMSMessage{
			ID:   fmt.Sprintf("%d", s.ID),
			From: s.Sender,
			Text: s.Text,
			Date: s.Date,
		})
	}
	return msgs, nil
}

func (f *FiveSim) WaitForCode(ctx context.Context, num *NumberInfo, timeout time.Duration) (*SMSMessage, error) {
	deadline := time.Now().Add(timeout)
	interval := DefaultPollInterval

	for time.Now().Before(deadline) {
		msgs, err := f.GetMessages(ctx, num)
		if err != nil {
			return nil, fmt.Errorf("5sim poll: %w", err)
		}
		for i := range msgs {
			if ExtractVerificationCode(&msgs[i]) != "" {
				return &msgs[i], nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
	return nil, fmt.Errorf("timed out waiting for SMS to %s at 5sim after %v", num.Number, timeout)
}

func (f *FiveSim) Cancel(ctx context.Context, num *NumberInfo) error {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/user/cancel/%s", fiveSimBaseURL, num.ActivationID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("5sim cancel: %w", err)
	}
	defer resp.Body.Close()
	return nil
}
