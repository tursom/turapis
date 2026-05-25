package codexauth

const PlatformClientID = "app_2SKx67EdpoN0G6j64rFvigXD"

type TokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
	ExpiresAt    int64  `json:"expires_at"`
}

type AccountIdentity struct {
	Email     string
	AccountID string
	UserID    string
	PlanType  string
}

type FlowConfig struct {
	CallbackPort int
	TokenURL     string
}

type FlowResult struct {
	Tokens   *TokenSet
	Identity *AccountIdentity
}

func DefaultFlowConfig() FlowConfig {
	return FlowConfig{CallbackPort: 1455}
}
