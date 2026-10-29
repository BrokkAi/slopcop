package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/BrokkAi/slopcop/internal/service"
)

type completedCheck struct {
	Fingerprint string `json:"fingerprint"`
}

func fingerprintCheck(serviceURL, token string, request service.Request) string {
	credential := sha256.Sum256([]byte(token))
	body := struct {
		ServiceURL string             `json:"serviceUrl"`
		Credential string             `json:"credential"`
		RepoURL    string             `json:"repoUrl"`
		BaseCommit string             `json:"baseCommit"`
		Change     string             `json:"change"`
		Selection  *service.Selection `json:"selection,omitempty"`
		FailOn     string             `json:"failOn,omitempty"`
	}{
		ServiceURL: serviceURL,
		Credential: hex.EncodeToString(credential[:]),
		RepoURL:    request.RepoURL,
		BaseCommit: request.BaseCommit,
		Change:     request.Change,
		Selection:  request.Selection,
		FailOn:     request.FailOn,
	}
	encoded, _ := json.Marshal(body)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func duplicateHookCheck(prepared preparedCheck, input *hookInput) bool {
	path, ok := completedCheckPath(prepared, nil, input)
	if !ok {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var previous completedCheck
	return json.Unmarshal(data, &previous) == nil && previous.Fingerprint == prepared.fingerprint
}

func rememberCompletedCheck(prepared preparedCheck, attribution *service.Attribution, input *hookInput) {
	path, ok := completedCheckPath(prepared, attribution, input)
	if !ok {
		return
	}
	data, err := json.Marshal(completedCheck{Fingerprint: prepared.fingerprint})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return
	}
	_ = os.Chmod(path, 0o600)
}

func completedCheckPath(prepared preparedCheck, attribution *service.Attribution, input *hookInput) (string, bool) {
	harness, session := "", ""
	if input != nil {
		harness, session = input.Harness, input.SessionID
	} else if attribution != nil {
		harness, session = attribution.Harness, attribution.SessionID
	}
	if harness == "" || session == "" {
		return "", false
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}
	identity := harness + "\x00" + session + "\x00" + prepared.repoRoot
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(dir, "slopcop", "checks", hex.EncodeToString(digest[:])+".json"), true
}
