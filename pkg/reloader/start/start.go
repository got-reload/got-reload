// The sole purpose of this package is to start the reloader process, merely
// by being imported by the process.

package start

import (
	"os"

	"github.com/got-reload/got-reload/pkg/reloader"
)

func init() {
	if val, ok := os.LookupEnv(reloader.StartReloaderEnv); ok && val == "1" {
		reloader.Start()
	}
}
