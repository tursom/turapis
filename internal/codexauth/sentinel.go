package codexauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"net/http"
	"strings"
	"time"
)

const (
	sentinelReqURL      = "https://sentinel.openai.com/backend-api/sentinel/req"
	sentinelFrameURL    = "https://sentinel.openai.com/backend-api/sentinel/frame.html"
	sentinelSDKURL      = "https://sentinel.openai.com/sentinel/20260124ceb8/sdk.js"
	sentinelMaxAttempts = 500000
	sentinelErrorPrefix = "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"

	SentinelFlowRegister          = "username_password_create"
	SentinelFlowCreateAccount     = "oauth_create_account"
	SentinelFlowAuthorizeContinue = "authorize_continue"
	SentinelFlowPasswordVerify    = "password_verify"
)

var UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"

var secChUABrand = `"Chromium";v="145", "Not:A-Brand";v="24", "Google Chrome";v="145"`

// SentinelTokenGenerator generates OpenAI sentinel proof-of-work tokens
// required by auth.openai.com registration API endpoints.
type SentinelTokenGenerator struct {
	DeviceID  string
	UserAgent string
	SessionID string
}

// NewSentinelTokenGenerator creates a new generator with a random SessionID.
func NewSentinelTokenGenerator(deviceID string) *SentinelTokenGenerator {
	return &SentinelTokenGenerator{
		DeviceID:  deviceID,
		UserAgent: UserAgent,
		SessionID: generateUUID(),
	}
}

// fnv1a32 computes the FNV-1a 32-bit hash with avalanche mixing,
// returning an 8-char lowercase hex string. Output must match the
// chatgpt2api Python reference exactly.
func fnv1a32(text string) string {
	h := uint32(2166136261)
	for _, ch := range text {
		h ^= uint32(ch)
		h *= 16777619
	}
	// avalanche mixing — algorithm must match Python reference byte-for-byte
	h ^= h >> 16
	h *= 2246822507
	h ^= h >> 13
	h *= 3266489909
	h ^= h >> 16
	return fmt.Sprintf("%08x", h)
}

// getConfig returns an 18-element browser fingerprint array that mimics
// a real browser environment to pass server-side validation.
// Array layout:
//
//	[0]  screen resolution     [9]  random (replaced by elapsed ms in PoW)
//	[1]  current UTC time      [10] fake navigator property
//	[2]  screen color depth    [11] fake object property
//	[3]  random (replaced by iteration in PoW)  [12] fake type
//	[4]  user agent            [13] performance.now()
//	[5]  sentinel SDK URL      [14] session ID
//	[6]  nil                   [15] empty string
//	[7]  nil                   [16] CPU cores
//	[8]  language              [17] timestamp offset
func (g *SentinelTokenGenerator) getConfig() []any {
	now := time.Now().UTC()
	perfNow := randomFloat(1000, 50000)
	rng := newSeededRand()

	return []any{
		"1920x1080",
		now.Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)"),
		uint64(4294705152),
		rng.Float64(),
		g.UserAgent,
		sentinelSDKURL,
		nil,
		nil,
		"en-US",
		rng.Float64(),
		sentinelFakeNavigator(rng),
		sentinelFakeProperty(rng),
		sentinelFakeType(rng),
		perfNow,
		g.SessionID,
		"",
		sentinelFakeCPUCores(rng),
		float64(now.UnixMilli()) - perfNow,
	}
}

func b64Encode(data []any) string {
	raw, _ := json.Marshal(data)
	return base64.RawStdEncoding.EncodeToString(raw)
}

// GenerateRequirementsToken generates a requirements token (no PoW) used to
// request a sentinel challenge. Format: "gAAAAAC" + base64 config.
func (g *SentinelTokenGenerator) GenerateRequirementsToken() string {
	data := g.getConfig()
	data[3] = 1
	data[9] = float64(randomInt(5, 50))
	return "gAAAAAC" + b64Encode(data)
}

// GenerateToken solves a proof-of-work challenge by iterating through FNV-1a
// hashes. seed and difficulty are returned by the sentinel API.
// Returns "gAAAAAB" + base64 config + "~S" on success, or an error-prefixed
// token if MAX_ATTEMPTS is exhausted.
func (g *SentinelTokenGenerator) GenerateToken(seed, difficulty string) string {
	start := time.Now()
	data := g.getConfig()

	if difficulty == "" {
		difficulty = "0"
	}
	difficultyLen := len(difficulty)

	for i := range sentinelMaxAttempts {
		data[3] = i
		data[9] = float64(roundToInt(time.Since(start).Seconds() * 1000))

		payload := b64Encode(data)
		if fnv1a32(seed+payload)[:difficultyLen] <= difficulty {
			return "gAAAAAB" + payload + "~S"
		}
	}
	return "gAAAAAB" + sentinelErrorPrefix + b64Encode([]any{nil})
}

type sentinelReqPayload struct {
	P    string `json:"p"`
	ID   string `json:"id"`
	Flow string `json:"flow"`
}

type sentinelRespData struct {
	Token       string          `json:"token"`
	ProofOfWork *sentinelPoWReq `json:"proofofwork"`
}

type sentinelPoWReq struct {
	Seed       string `json:"seed"`
	Difficulty string `json:"difficulty"`
}

// BuildSentinelToken executes the full sentinel token construction flow:
// submits a requirements token, fetches the PoW challenge, solves it,
// and returns the serialized sentinel token JSON.
func (g *SentinelTokenGenerator) BuildSentinelToken(ctx context.Context, httpClient *http.Client, flow string) (string, error) {
	reqPayload := sentinelReqPayload{
		P:    g.GenerateRequirementsToken(),
		ID:   g.DeviceID,
		Flow: flow,
	}

	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("marshal sentinel request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", sentinelReqURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return "", fmt.Errorf("create sentinel request: %w", err)
	}

	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Referer", sentinelFrameURL)
	req.Header.Set("Origin", "https://sentinel.openai.com")
	req.Header.Set("User-Agent", g.UserAgent)
	req.Header.Set("sec-ch-ua", secChUABrand)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sentinel request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("sentinel_req_failed_%d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return "", fmt.Errorf("read sentinel response: %w", err)
	}

	var respData sentinelRespData
	if err := json.Unmarshal(raw, &respData); err != nil {
		return "", fmt.Errorf("parse sentinel response: %w", err)
	}

	token := strings.TrimSpace(respData.Token)
	if token == "" {
		return "", fmt.Errorf("sentinel_no_token")
	}

	var seed, diff string
	if respData.ProofOfWork != nil {
		seed = respData.ProofOfWork.Seed
		diff = respData.ProofOfWork.Difficulty
	}

	pValue := g.GenerateToken(seed, diff)
	if strings.Contains(pValue, sentinelErrorPrefix) {
		return "", fmt.Errorf("sentinel_pow_failed")
	}

	finalToken := map[string]any{
		"p":    pValue,
		"t":    "",
		"c":    token,
		"id":   g.DeviceID,
		"flow": flow,
	}

	finalJSON, err := json.Marshal(finalToken)
	if err != nil {
		return "", fmt.Errorf("marshal sentinel token: %w", err)
	}

	return string(finalJSON), nil
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func newSeededRand() *mrand.Rand {
	seed, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return mrand.New(mrand.NewSource(time.Now().UnixNano()))
	}
	return mrand.New(mrand.NewSource(seed.Int64()))
}

func randomFloat(low, high float64) float64 {
	rng := newSeededRand()
	return low + rng.Float64()*(high-low)
}

func randomInt(low, high int) int {
	rng := newSeededRand()
	return low + rng.Intn(high-low+1)
}

func roundToInt(f float64) int {
	if f < 0 {
		return int(f - 0.5)
	}
	return int(f + 0.5)
}

var (
	fakeNavigatorOpts = []string{
		"vendorSub-undefined",
		"plugins-undefined",
		"mimeTypes-undefined",
		"hardwareConcurrency-undefined",
	}
	fakePropertyOpts = []string{"location", "implementation", "URL", "documentURI", "compatMode"}
	fakeTypeOpts     = []string{"Object", "Function", "Array", "Number", "parseFloat", "undefined"}
	fakeCPUCoresOpts = []int{4, 8, 12, 16}
)

func sentinelFakeNavigator(rng *mrand.Rand) string {
	return fakeNavigatorOpts[rng.Intn(len(fakeNavigatorOpts))]
}

func sentinelFakeProperty(rng *mrand.Rand) string {
	return fakePropertyOpts[rng.Intn(len(fakePropertyOpts))]
}

func sentinelFakeType(rng *mrand.Rand) string {
	return fakeTypeOpts[rng.Intn(len(fakeTypeOpts))]
}

func sentinelFakeCPUCores(rng *mrand.Rand) int {
	return fakeCPUCoresOpts[rng.Intn(len(fakeCPUCoresOpts))]
}
