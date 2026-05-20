package gateway

import (
	"testing"

	"github.com/tursom/turapis/internal/config"
)

func TestAccessLogCollectorSetQuotaBackfillsSuccessfulAttempt(t *testing.T) {
	c := &AccessLogCollector{}
	c.RecordAttempt(config.AttemptRecord{
		Provider:   "codex-failed",
		Success:    false,
		AttemptNum: 1,
	})
	c.RecordAttempt(config.AttemptRecord{
		Provider:   "codex-success",
		Success:    true,
		AttemptNum: 2,
	})

	before := `{"primary":{"used_percent":11}}`
	after := `{"primary":{"used_percent":42}}`
	c.SetQuota(before, after)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quotaBefore != before || c.quotaAfter != after {
		t.Fatalf("collector quota = %q/%q, want %q/%q", c.quotaBefore, c.quotaAfter, before, after)
	}
	if len(c.attempts) != 2 {
		t.Fatalf("attempts len = %d, want 2", len(c.attempts))
	}
	got := c.attempts[1]
	if got.QuotaBefore != before || got.QuotaAfter != after {
		t.Fatalf("success attempt quota = %q/%q, want %q/%q", got.QuotaBefore, got.QuotaAfter, before, after)
	}
	if c.attempts[0].QuotaBefore != "" || c.attempts[0].QuotaAfter != "" {
		t.Fatalf("failed attempt quota should not be backfilled: %#v", c.attempts[0])
	}
}

func TestAccessLogCollectorSetQuotaDoesNotClearExistingQuota(t *testing.T) {
	before := `{"primary":{"used_percent":11}}`
	after := `{"primary":{"used_percent":42}}`
	c := &AccessLogCollector{}
	c.RecordAttempt(config.AttemptRecord{
		Provider:    "codex-success",
		QuotaBefore: before,
		QuotaAfter:  after,
		Success:     true,
		AttemptNum:  1,
	})

	c.SetQuota("", "")

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quotaBefore != before || c.quotaAfter != after {
		t.Fatalf("collector quota = %q/%q, want preserved %q/%q", c.quotaBefore, c.quotaAfter, before, after)
	}
}

func TestQuotaFromAttemptsFallsBackToFailedAttemptQuota(t *testing.T) {
	before := `{"primary":{"used_percent":90}}`
	after := `{"primary":{"used_percent":99}}`
	gotBefore, gotAfter := quotaFromAttempts("", "", []config.AttemptRecord{
		{
			Provider:    "codex-failed",
			QuotaBefore: before,
			QuotaAfter:  after,
			Success:     false,
			AttemptNum:  1,
		},
		{
			Provider:   "no-quota-success",
			Success:    true,
			AttemptNum: 2,
		},
	})

	if gotBefore != before || gotAfter != after {
		t.Fatalf("fallback quota = %q/%q, want %q/%q", gotBefore, gotAfter, before, after)
	}
}

func TestAccessLogCollectorRecordAttemptSetsSuccessfulQuota(t *testing.T) {
	before := `{"primary":{"used_percent":11}}`
	after := `{"primary":{"used_percent":42}}`
	c := &AccessLogCollector{}

	c.RecordAttempt(config.AttemptRecord{
		Provider:    "codex-success",
		QuotaBefore: before,
		QuotaAfter:  after,
		Success:     true,
		AttemptNum:  1,
	})

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quotaBefore != before || c.quotaAfter != after {
		t.Fatalf("collector quota = %q/%q, want %q/%q", c.quotaBefore, c.quotaAfter, before, after)
	}
}
