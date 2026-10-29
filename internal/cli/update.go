package cli

import (
	"io"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
	"github.com/BrokkAi/slopcop/internal/update"
)

// noticeUpdates has client say on stderr, at most once a day, when the
// service reports a release newer than this one. Stdout stays the report.
func noticeUpdates(client *service.Client, stderr io.Writer, version string) {
	// Without a stamp path the notice still prints, once per process.
	stamp, _ := config.NoticePath()
	client.LatestVersion = update.New(stderr, version, stamp).Notify
}
