// Package attribution learns which agent harness and models wrote a change,
// so a check can send it and the service can tie each finding to the model
// that wrote its file.
//
// Everything here is read from the machine and asserted by the client, so it
// is analytics only. It never fails a check: whatever cannot be read is left
// out.
package attribution

import (
	"os"

	"github.com/BrokkAi/slopcop/internal/service"
)

// SourceTranscript says the CLI read the model from the harness's own session
// record at check time.
const SourceTranscript = "transcript"

// maxModels mirrors the service's MAX_ATTRIBUTED_MODELS.
const maxModels = 32

// Detect returns the attribution of a check of the repository at repoRoot,
// run in the current process's environment, or nil when no harness is found.
func Detect(repoRoot string) *service.Attribution {
	return detect(os.Getenv, repoRoot)
}

func detect(getenv func(string) string, repoRoot string) *service.Attribution {
	if attribution := claudeCode(getenv, repoRoot); attribution != nil {
		return attribution
	}
	return nil
}
