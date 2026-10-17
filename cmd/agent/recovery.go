package main

import (
	"flag"
	"fmt"
	"time"

	"infernosim/pkg/liverecovery"
	"infernosim/pkg/privacy"
)

type recoveryFlags struct {
	endpoint, origin, model, out, policy, review *string
	requests, tokens                             *int
	timeout                                      *time.Duration
	sensitive                                    *bool
}

func registerRecoveryFlags(fs *flag.FlagSet) recoveryFlags {
	return recoveryFlags{
		fs.String("live-ollama-endpoint", "", "Opt in to bounded live recovery at this numeric loopback HTTP origin"),
		fs.String("live-model-origin", "", "Application model origin to intercept (default: live endpoint)"),
		fs.String("live-ollama-model", "", "Explicit local model name for recovery"),
		fs.String("live-capture-dir", "", "New private directory for the recorded prefix and live branch"),
		fs.String("live-privacy-policy", "", "Privacy policy for retained recovery traffic"),
		fs.String("live-capture-review", "", "Review of application dependencies for captured branch conversion"),
		fs.Int("live-max-requests", 1, "Maximum model requests in the live recovery window (1..16)"),
		fs.Int("live-max-tokens", 256, "Maximum measured output tokens across recovery requests (1..8192)"),
		fs.Duration("live-timeout", 30*time.Second, "Total live recovery window"),
		fs.Bool("live-capture-sensitive-data", false, "Explicitly retain recovery bodies without a privacy policy; synthetic data only"),
	}
}
func (f recoveryFlags) options() (*liverecovery.Options, error) {
	if *f.endpoint == "" {
		if *f.origin != "" || *f.model != "" || *f.out != "" || *f.policy != "" || *f.sensitive || *f.review != "" {
			return nil, fmt.Errorf("live recovery configuration requires explicit live-ollama-endpoint")
		}
		return nil, nil
	}
	o := &liverecovery.Options{Endpoint: *f.endpoint, Origin: *f.origin, Model: *f.model, OutputDir: *f.out, MaxRequests: *f.requests, MaxTokens: *f.tokens, Timeout: *f.timeout, CaptureSensitiveData: *f.sensitive, Review: *f.review}
	if *f.policy != "" {
		p, err := privacy.Load(*f.policy)
		if err != nil {
			return nil, err
		}
		o.Privacy = p
	}
	return o, nil
}
