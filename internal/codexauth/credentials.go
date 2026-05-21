package codexauth

import "encoding/json"

// EmailCredentialToJSON 将 EmailCredential 序列化为 JSON。
func EmailCredentialToJSON(ec *EmailCredential) (json.RawMessage, error) {
	return json.Marshal(ec)
}

// EmailCredentialFromJSON 将 JSON 反序列化为 EmailCredential。
func EmailCredentialFromJSON(data json.RawMessage) (*EmailCredential, error) {
	var ec EmailCredential
	if err := json.Unmarshal(data, &ec); err != nil {
		return nil, err
	}
	return &ec, nil
}
