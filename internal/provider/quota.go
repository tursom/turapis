package provider

import (
	"net/http"
	"strconv"
)

func ParseQuota(h http.Header) map[string]interface{} {
	getInt := func(k string) *int {
		v := h.Get(k)
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil
		}
		return &n
	}
	getFloat := func(k string) *float64 {
		v := h.Get(k)
		if v == "" {
			return nil
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	p := map[string]interface{}{}
	if v := getFloat("x-codex-primary-used-percent"); v != nil {
		p["used_percent"] = *v
	}
	if v := getInt("x-codex-primary-reset-after-seconds"); v != nil {
		p["reset_after_seconds"] = *v
	}
	if v := getInt("x-codex-primary-window-minutes"); v != nil {
		p["window_minutes"] = *v
	}
	s := map[string]interface{}{}
	if v := getFloat("x-codex-secondary-used-percent"); v != nil {
		s["used_percent"] = *v
	}
	if v := getInt("x-codex-secondary-reset-after-seconds"); v != nil {
		s["reset_after_seconds"] = *v
	}
	if v := getInt("x-codex-secondary-window-minutes"); v != nil {
		s["window_minutes"] = *v
	}
	q := map[string]interface{}{}
	if len(p) > 0 {
		q["primary"] = p
	}
	if len(s) > 0 {
		q["secondary"] = s
	}
	t := map[string]interface{}{}
	if v := getFloat("x-codex-tertiary-used-percent"); v != nil {
		t["used_percent"] = *v
	}
	if v := getInt("x-codex-tertiary-reset-after-seconds"); v != nil {
		t["reset_after_seconds"] = *v
	}
	if v := getInt("x-codex-tertiary-window-minutes"); v != nil {
		t["window_minutes"] = *v
	}
	if len(t) > 0 {
		q["tertiary"] = t
	}
	return q
}
