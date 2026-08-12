package reporting

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// SemanticVersion is injected by GoReleaser so SARIF identifies the exact
// InfernoSIM build that produced it.
var SemanticVersion = "dev"

var sarifSemanticVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

type Finding struct {
	RuleID   string `json:"rule_id"`
	Level    string `json:"level"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Location string `json:"location,omitempty"`
}

type Result struct {
	Tool      string    `json:"tool"`
	Outcome   string    `json:"outcome"`
	Summary   string    `json:"summary"`
	Generated time.Time `json:"generated"`
	Findings  []Finding `json:"findings"`
}

func WriteFormats(directory string, formats []string, result Result) ([]string, error) {
	if result.Tool == "" {
		result.Tool = "InfernoSIM"
	}
	if result.Generated.IsZero() {
		result.Generated = time.Now().UTC()
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	var written []string
	seen := make(map[string]struct{})
	for _, raw := range formats {
		format := strings.ToLower(strings.TrimSpace(raw))
		if format == "" {
			continue
		}
		if _, duplicate := seen[format]; duplicate {
			continue
		}
		seen[format] = struct{}{}
		var (
			name string
			data []byte
			err  error
		)
		switch format {
		case "junit":
			name = "infernosim-report.junit.xml"
			data, err = marshalJUnit(result)
		case "sarif":
			name = "infernosim-report.sarif"
			data, err = marshalSARIF(result)
		case "html":
			name = "infernosim-report.html"
			data, err = marshalHTML(result)
		default:
			return written, fmt.Errorf("unsupported report format %q (expected junit, sarif, or html)", format)
		}
		if err != nil {
			return written, err
		}
		path := filepath.Join(directory, name)
		if err := WritePrivateFile(path, data); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

// WritePrivateFile atomically replaces a generated artifact with owner-only
// permissions. The temporary file is created in the destination directory so
// a successful rename cannot cross filesystems.
func WritePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".infernosim-report-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		if info, statErr := os.Lstat(path); statErr == nil && info.IsDir() {
			return err
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if err := os.Rename(tempPath, path); err != nil {
			return err
		}
	}
	return nil
}

type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

func marshalJUnit(result Result) ([]byte, error) {
	suite := junitSuite{Name: result.Tool, Tests: len(result.Findings) + 1}
	suite.Cases = append(suite.Cases, junitCase{
		Name:      "replay-outcome",
		Classname: "infernosim.replay",
	})
	if strings.HasPrefix(result.Outcome, "FAIL") {
		suite.Failures++
		suite.Cases[0].Failure = &junitFailure{
			Message: result.Summary,
			Type:    result.Outcome,
			Body:    result.Summary,
		}
	}
	for index, finding := range result.Findings {
		testCase := junitCase{
			Name:      fmt.Sprintf("%s-%d", finding.RuleID, index+1),
			Classname: "infernosim.contract",
			Failure: &junitFailure{
				Message: finding.Title,
				Type:    finding.RuleID,
				Body:    finding.Message,
			},
		}
		suite.Failures++
		suite.Cases = append(suite.Cases, testCase)
	}
	data, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), data...), nil
}

type sarifDocument struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name            string      `json:"name"`
	InformationURI  string      `json:"informationUri"`
	SemanticVersion string      `json:"semanticVersion,omitempty"`
	Rules           []sarifRule `json:"rules,omitempty"`
}

type sarifRule struct {
	ID               string         `json:"id"`
	ShortDescription sarifMessage   `json:"shortDescription"`
	HelpURI          string         `json:"helpUri,omitempty"`
	Properties       map[string]any `json:"properties,omitempty"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

func marshalSARIF(result Result) ([]byte, error) {
	rulesByID := make(map[string]sarifRule)
	results := make([]sarifResult, 0, len(result.Findings)+1)
	if strings.HasPrefix(result.Outcome, "FAIL") {
		const outcomeRule = "INFERNOSIM_OUTCOME"
		rulesByID[outcomeRule] = sarifRule{
			ID:               outcomeRule,
			ShortDescription: sarifMessage{Text: "InfernoSIM command failed"},
		}
		results = append(results, sarifResult{
			RuleID: outcomeRule,
			Level:  "error",
			Message: sarifMessage{
				Text: result.Outcome + ": " + result.Summary,
			},
		})
	}
	for _, finding := range result.Findings {
		level := strings.ToLower(finding.Level)
		if level != "error" && level != "warning" && level != "note" {
			level = "error"
		}
		rulesByID[finding.RuleID] = sarifRule{
			ID:               finding.RuleID,
			ShortDescription: sarifMessage{Text: finding.Title},
		}
		entry := sarifResult{
			RuleID:  finding.RuleID,
			Level:   level,
			Message: sarifMessage{Text: finding.Message},
		}
		if finding.Location != "" {
			entry.Locations = []sarifLocation{{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifactLocation{URI: finding.Location},
				},
			}}
		}
		results = append(results, entry)
	}
	rules := make([]sarifRule, 0, len(rulesByID))
	for _, rule := range rulesByID {
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].ID < rules[j].ID
	})
	document := sarifDocument{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:            result.Tool,
				InformationURI:  "https://github.com/pranaysparihar/InfernoSIM",
				SemanticVersion: sarifSemanticVersion(SemanticVersion),
				Rules:           rules,
			}},
			Results: results,
		}},
	}
	return json.MarshalIndent(document, "", "  ")
}

func sarifSemanticVersion(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if !sarifSemanticVersionPattern.MatchString(value) {
		return ""
	}
	return value
}

var htmlReportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>InfernoSIM replay report</title>
  <style>
    :root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, sans-serif; }
    body { max-width: 1000px; margin: 3rem auto; padding: 0 1.25rem; line-height: 1.5; }
    header { border-bottom: 1px solid #8886; margin-bottom: 2rem; }
    .outcome { font-size: 1.4rem; font-weight: 700; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border-bottom: 1px solid #8885; padding: .7rem; text-align: left; vertical-align: top; }
    code { font-size: .9em; }
  </style>
</head>
<body>
<header><h1>InfernoSIM replay report</h1><p>Generated {{.Generated.Format "2006-01-02 15:04:05Z07:00"}}</p></header>
<p class="outcome">{{.Outcome}}</p><p>{{.Summary}}</p>
<h2>Contract and drift findings ({{len .Findings}})</h2>
{{if .Findings}}<table><thead><tr><th>Rule</th><th>Level</th><th>Finding</th><th>Location</th></tr></thead>
<tbody>{{range .Findings}}<tr><td><code>{{.RuleID}}</code></td><td>{{.Level}}</td><td><strong>{{.Title}}</strong><br>{{.Message}}</td><td>{{.Location}}</td></tr>{{end}}</tbody></table>
{{else}}<p>No findings.</p>{{end}}
</body></html>`))

func marshalHTML(result Result) ([]byte, error) {
	var output strings.Builder
	if err := htmlReportTemplate.Execute(&output, result); err != nil {
		return nil, err
	}
	return []byte(output.String()), nil
}
