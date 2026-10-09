package nodeshim

import "time"

// SetShutdownDelay delays sockets' shutdown completions (net.go).
func SetShutdownDelay(d time.Duration) { shutdownDelay = d }
