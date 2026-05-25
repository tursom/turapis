package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// importAccounts 从 JSONL 数据批量导入 OAuth / API Key 账号为 Provider
//
// POST /admin/import-accounts
//
// 请求体格式：
//
//	{
//	  "site_id": 4,            // 目标站点 ID，可选；提供后覆盖每条记录自身的 site_id
//	  "accounts": [            // JSON 数组，每项一个账号对象
//	    {
//	      "site_id": "5",
//	      "username": "user@mail.com",
//	      "access_token": "eyJ...",
//	      "refresh_token": "rt_...",   // 可选，存在则存储用于后续刷新
//	      "api_token": "..."
//	    }
//	  ]
//	}
//
// 处理逻辑：
//   - oauth 站点：用 access_token 构造 credential {"tokens":{"access_token":"...","refresh_token":"..."}}
//   - api_key 站点：直接使用 api_token
//   - 重复的 provider 名称（username）会自动跳过
//
// 返回：{imported: N, skipped: N, errors: [...]}
func (a *Admin) importAccounts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SiteID   *int              `json:"site_id"`  // 可选，覆盖每条记录自身的 site_id
		Accounts []json.RawMessage `json:"accounts"` // JSONL 解析后的账号数组
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	r.Body.Close()

	var imported, skipped int
	var errs []string

	for _, raw := range body.Accounts {
		var acc map[string]string
		if err := json.Unmarshal(raw, &acc); err != nil {
			errs = append(errs, "parse account: "+err.Error())
			skipped++
			continue
		}

		siteID, _ := strconv.Atoi(acc["site_id"])
		if body.SiteID != nil {
			siteID = *body.SiteID // 请求体的 site_id 覆盖记录自身的值
		}
		site, getErr := a.store.GetSite(siteID)
		if getErr != nil {
			errs = append(errs, "site "+acc["site_id"]+" not found: "+getErr.Error())
			skipped++
			continue
		}

		accessToken := acc["access_token"]
		username := strings.TrimSpace(acc["username"])
		if username == "" {
			username = "unknown"
		}

		var apiKey string
		var oauthJSON json.RawMessage

		switch site.AuthMode {
		case "oauth":
			if accessToken == "" {
				errs = append(errs, "missing access_token for "+username)
				skipped++
				continue
			}
			tokens := map[string]interface{}{
				"access_token": accessToken,
			}
			if rt := acc["refresh_token"]; rt != "" {
				tokens["refresh_token"] = rt
			}
		creds, _ := json.Marshal(map[string]interface{}{
			"credential": map[string]interface{}{"tokens": tokens},
		})
			oauthJSON = creds
		case "api_key":
			apiKey = acc["api_token"]
		}

		_, _, createErr := a.store.CreateProviderFromSite(siteID, username, apiKey, oauthJSON)
		if createErr != nil {
			errs = append(errs, createErr.Error())
			skipped++
			continue
		}
		imported++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
		"errors":   errs,
	})
}
