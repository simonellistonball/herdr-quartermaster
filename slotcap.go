package main

import "fmt"

// checkSlots refuses a new worker when the host already runs its slots'
// worth. Homelab patch: upstream 0.6.0 computes slots (host list) but never
// enforces them, and one workspace grew to 30+ resident Claude workers and
// OOMed. Every agent but the coordinator counts, idle or not: an idle worker
// still holds its memory. A focus of an existing worker never gets here.
func (c Config) checkSlots(host string, agents []Agent) error {
	n := 0
	for _, a := range agents {
		if a.Name != c.CoordinatorName {
			n++
		}
	}
	if limit := c.slots(host); n >= limit {
		return fmt.Errorf("%s is full: %d of %d worker slots in use. Finish or park a worker first "+
			"(quartermaster resolve <bead>, or ws-park in the workspace), or raise [hosts.%s] slots",
			host, n, limit, host)
	}
	return nil
}
