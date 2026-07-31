package reporting

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteJUnitSARIFAndHTML(t *testing.T) {
	dir := t.TempDir()
	written, err := WriteFormats(dir, []string{"junit", "sarif", "html"}, Result{
		Outcome: "FAIL_CONTRACT_DRIFT",
		Summary: "one finding",
		Findings: []Finding{{
			RuleID:  "OPENAPI_SCHEMA_VIOLATION",
			Level:   "error",
			Title:   "<script>alert(1)</script>",
			Message: "field $.id expected integer",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 3 {
		t.Fatalf("written=%v", written)
	}
	junitData, _ := os.ReadFile(filepath.Join(dir, "infernosim-report.junit.xml"))
	var junit any
	if err := xml.Unmarshal(junitData, &junit); err != nil {
		t.Fatalf("invalid JUnit XML: %v", err)
	}
	sarifData, _ := os.ReadFile(filepath.Join(dir, "infernosim-report.sarif"))
	var sarif map[string]any
	if err := json.Unmarshal(sarifData, &sarif); err != nil {
		t.Fatalf("invalid SARIF JSON: %v", err)
	}
	if sarif["version"] != "2.1.0" {
		t.Fatalf("SARIF version=%v", sarif["version"])
	}
	htmlData, _ := os.ReadFile(filepath.Join(dir, "infernosim-report.html"))
	if strings.Contains(string(htmlData), "<script>alert") || !strings.Contains(string(htmlData), "&lt;script&gt;") {
		t.Fatal("HTML output was not escaped")
	}
}
