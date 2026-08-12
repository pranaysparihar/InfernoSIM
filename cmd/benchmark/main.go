// Command benchmark runs InfernoSIM's reproducible, incident-derived baseline.
// It intentionally produces raw JSON rather than a marketing score.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"infernosim/pkg/heal"
	"infernosim/pkg/testgen"
)

type runResult struct {
	Run              int     `json:"run"`
	HealingMillis    float64 `json:"healing_ms"`
	GenerationMillis float64 `json:"generation_ms"`
	ConfigHash       string  `json:"config_hash"`
	HarnessHash      string  `json:"harness_hash"`
	Accepted         int     `json:"accepted"`
	Rejected         int     `json:"rejected"`
	Ambiguities      int     `json:"ambiguities"`
	SecretLeaks      int     `json:"secret_leaks"`
}

type report struct {
	Schema              string      `json:"schema"`
	Generated           time.Time   `json:"generated"`
	Fixture             string      `json:"fixture"`
	FixtureHash         string      `json:"fixture_hash"`
	Runs                int         `json:"runs"`
	Deterministic       bool        `json:"deterministic"`
	BehaviorPassed      bool        `json:"behavior_passed"`
	UniqueConfigHashes  int         `json:"unique_config_hashes"`
	UniqueHarnessHashes int         `json:"unique_harness_hashes"`
	TotalSecretLeaks    int         `json:"total_secret_leaks"`
	HealingP50Millis    float64     `json:"healing_p50_ms"`
	HealingP95Millis    float64     `json:"healing_p95_ms"`
	GenerationP50Millis float64     `json:"generation_p50_ms"`
	GenerationP95Millis float64     `json:"generation_p95_ms"`
	Results             []runResult `json:"results"`
}

func main() {
	fixture := flag.String("fixture", "benchmarks/fixtures/rest-volatile", "Sanitized benchmark incident")
	runs := flag.Int("runs", 100, "Number of independent runs")
	out := flag.String("out", "benchmarks/results/infernosim.json", "Raw JSON result path")
	flag.Parse()
	if *runs < 1 || *runs > 10000 {
		fatal("runs must be between 1 and 10000")
	}
	absoluteFixture, err := filepath.Abs(*fixture)
	if err != nil {
		fatal(err.Error())
	}
	outputRoot, err := os.MkdirTemp("", "infernosim-benchmark-*")
	if err != nil {
		fatal(err.Error())
	}
	defer os.RemoveAll(outputRoot)
	fixtureHash, err := hashTree(absoluteFixture)
	if err != nil {
		fatal(err.Error())
	}

	result := report{
		Schema: "infernosim.category-benchmark.v1", Generated: time.Now().UTC(),
		Fixture: filepath.ToSlash(filepath.Clean(*fixture)), FixtureHash: fixtureHash,
		Runs: *runs, BehaviorPassed: true,
	}
	hashes := make(map[string]bool)
	harnessHashes := make(map[string]bool)
	var healing, generation []float64
	for index := 0; index < *runs; index++ {
		runDir := filepath.Join(outputRoot, fmt.Sprintf("run-%05d", index+1))
		proposed := filepath.Join(runDir, "replay.proposed.yaml")
		reports := filepath.Join(runDir, "reports")
		start := time.Now()
		healed, err := heal.Run(heal.Options{IncidentDirs: []string{absoluteFixture}, OutputPath: proposed, ReportDir: reports})
		if err != nil {
			fatal(fmt.Sprintf("run %d heal: %v", index+1, err))
		}
		healingMillis := durationMillis(time.Since(start))
		generatedDir := filepath.Join(runDir, "generated")
		start = time.Now()
		if _, err := testgen.Generate(testgen.Options{
			IncidentDir: absoluteFixture, OutputDir: generatedDir,
			Framework: testgen.FrameworkGoTestcontainers, Image: "infernosim:benchmark",
		}); err != nil {
			fatal(fmt.Sprintf("run %d testgen: %v", index+1, err))
		}
		generationMillis := durationMillis(time.Since(start))
		data, err := os.ReadFile(proposed)
		if err != nil {
			fatal(err.Error())
		}
		configHash := hashBytes(data)
		hashes[configHash] = true
		harnessHash, err := hashTree(generatedDir)
		if err != nil {
			fatal(err.Error())
		}
		harnessHashes[harnessHash] = true
		leaks, err := scanLeaks(runDir, "BENCHMARK_SECRET_DO_NOT_LEAK")
		if err != nil {
			fatal(err.Error())
		}
		result.Results = append(result.Results, runResult{
			Run: index + 1, HealingMillis: healingMillis, GenerationMillis: generationMillis,
			ConfigHash: configHash, HarnessHash: harnessHash, Accepted: healed.Accepted, Rejected: healed.Rejected,
			Ambiguities: healed.Ambiguities, SecretLeaks: leaks,
		})
		if healed.Accepted != 1 || healed.Rejected != 1 || healed.Ambiguities != 0 || !healed.ValidationPassed {
			result.BehaviorPassed = false
		}
		healing = append(healing, healingMillis)
		generation = append(generation, generationMillis)
		result.TotalSecretLeaks += leaks
	}
	result.UniqueConfigHashes = len(hashes)
	result.UniqueHarnessHashes = len(harnessHashes)
	result.Deterministic = len(hashes) == 1 && len(harnessHashes) == 1 && result.TotalSecretLeaks == 0 && result.BehaviorPassed
	result.HealingP50Millis = percentile(healing, 0.50)
	result.HealingP95Millis = percentile(healing, 0.95)
	result.GenerationP50Millis = percentile(generation, 0.50)
	result.GenerationP95Millis = percentile(generation, 0.95)
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal(err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		fatal(err.Error())
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o600); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("benchmark complete: runs=%d config_hashes=%d harness_hashes=%d behavior=%t leaks=%d healing_p95=%.3fms testgen_p95=%.3fms output=%s\n",
		result.Runs, result.UniqueConfigHashes, result.UniqueHarnessHashes, result.BehaviorPassed,
		result.TotalSecretLeaks, result.HealingP95Millis, result.GenerationP95Millis, *out)
	if !result.Deterministic {
		os.Exit(1)
	}
}

func scanLeaks(root, marker string) (int, error) {
	leaks := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("benchmark output contains unsupported symlink %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		leaks += strings.Count(string(data), marker)
		return nil
	})
	return leaks, err
}

func hashTree(root string) (string, error) {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("benchmark input contains unsupported symlink %s", path)
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func percentile(values []float64, quantile float64) float64 {
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	index := int(float64(len(copyValues)-1) * quantile)
	return copyValues[index]
}

func durationMillis(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "benchmark:", message)
	os.Exit(1)
}
