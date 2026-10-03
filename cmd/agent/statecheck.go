package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"infernosim/pkg/agentrunner"
	"strings"
	"time"
)

type stateFlags struct {
	setup, check, ids *string
	timeout           *time.Duration
}

func registerStateFlags(fs *flag.FlagSet) stateFlags {
	return stateFlags{
		fs.String("setup-command-json", "", "Explicit setup argv JSON; resets external test state before each run"),
		fs.String("check-command-json", "", "Explicit state-check argv JSON; emits {assertions:[{id,passed}]}"),
		fs.String("check-ids", "", "Comma-separated required application-state assertion IDs"),
		fs.Duration("check-timeout", 10*time.Second, "Maximum time for each setup/check hook"),
	}
}
func (f stateFlags) options() (agentrunner.StateCheckOptions, error) {
	o := agentrunner.StateCheckOptions{Timeout: *f.timeout}
	for _, entry := range []struct {
		raw  string
		dest *[]string
	}{{*f.setup, &o.SetupCommand}, {*f.check, &o.Command}} {
		if entry.raw != "" {
			if err := json.Unmarshal([]byte(entry.raw), entry.dest); err != nil || len(*entry.dest) == 0 {
				return o, fmt.Errorf("state hook must be a nonempty JSON argv array")
			}
		}
	}
	for _, id := range strings.Split(*f.ids, ",") {
		if id = strings.TrimSpace(id); id != "" {
			o.IDs = append(o.IDs, id)
		}
	}
	return o, o.Validate()
}
