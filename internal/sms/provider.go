package sms

import (
	"context"
	"regexp"
	"time"
)

// DefaultPollInterval is the default interval between polling for SMS messages.
const DefaultPollInterval = 5 * time.Second

// DefaultPollTimeout is the default maximum time to wait for an SMS message.
const DefaultPollTimeout = 120 * time.Second

// SMSMessage represents a received SMS message on the phone number.
type SMSMessage struct {
	ID   string `json:"id"`
	From string `json:"from"`
	Text string `json:"text"`
	Date string `json:"date"`
}

// NumberInfo holds information about a rented phone number from the SMS platform.
type NumberInfo struct {
	Number       string            `json:"number"`
	ActivationID string            `json:"activation_id"`
	Service      string            `json:"service"`
	Provider     string            `json:"provider"`
	Extra        map[string]string `json:"extra,omitempty"`
}

// SMSProviderConfig holds configuration for creating an SMSProvider instance.
type SMSProviderConfig struct {
	ProxyURL string
	APIKey   string
}

// SMSProvider defines the interface for SMS verification code providers (接码平台).
//
// The flow:
//  1. GetNumber(service) - request a phone number for the specified service
//  2. Use the number in the target application (e.g., enter on OpenAI's add-phone page)
//  3. WaitForCode(num) - poll until a verification SMS arrives
//  4. Extract the code and submit it
//  5. Cancel(num) - optionally release the number
type SMSProvider interface {
	GetNumber(ctx context.Context, service string) (*NumberInfo, error)

	GetMessages(ctx context.Context, num *NumberInfo) ([]SMSMessage, error)

	WaitForCode(ctx context.Context, num *NumberInfo, timeout time.Duration) (*SMSMessage, error)

	Cancel(ctx context.Context, num *NumberInfo) error

	Name() string
}

// verificationCodeRE matches 6-digit verification codes commonly used by OpenAI.
var verificationCodeRE = regexp.MustCompile(`\b(\d{4,8})\b`)

// ExtractVerificationCode extracts a verification code from an SMS message body.
// It returns the first 4-8 digit number found, which covers most verification code formats.
func ExtractVerificationCode(msg *SMSMessage) string {
	matches := verificationCodeRE.FindStringSubmatch(msg.Text)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}
