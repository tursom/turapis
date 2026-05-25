package provider

import (
	"context"
	"net/http"
	"strings"

	"github.com/tursom/turapis/internal/models"
)

var skipHeaderKeys = map[string]bool{
	"host":              true,
	"content-length":    true,
	"transfer-encoding": true,
}

func ForwardClientHeaders(req *http.Request, ctx context.Context) {
	headers := models.ClientHeadersFromContext(ctx)
	if headers == nil {
		return
	}
	for key, values := range headers {
		if skipHeaderKeys[strings.ToLower(key)] {
			continue
		}
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
}
