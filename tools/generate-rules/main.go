// generate-rules writes the Prometheus rule YAML files derived from the typed
// alert definitions in pkg/monitoring/rules/alerts. Run via `make generate`.
//
// Flags:
//
//	-namespace <ns>   Kubernetes namespace embedded in the PrometheusRule manifest.
//	                  Falls back to the NAMESPACE environment variable, then
//	                  defaults to "NAMESPACE" as a placeholder when neither is set.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sradco/csi-volume-device-exporter/pkg/monitoring/rules"
)

func main() {
	namespace := flag.String("namespace", "", "Kubernetes namespace for the PrometheusRule manifest (overrides NAMESPACE env var)")
	flag.Parse()

	if *namespace == "" {
		*namespace = os.Getenv("NAMESPACE")
	}
	if *namespace == "" {
		*namespace = "NAMESPACE"
		fmt.Fprintln(os.Stderr, "generate-rules: namespace not set via -namespace flag or NAMESPACE env var; using placeholder \"NAMESPACE\"")
	}

	// Resolve the repository root relative to this file so the tool works
	// regardless of the working directory when invoked via `go run`.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	rulesDir := filepath.Join(repoRoot, "pkg", "monitoring", "rules")

	if err := rules.WritePrometheusRulesFile(filepath.Join(rulesDir, "alerts-rules.yaml")); err != nil {
		fmt.Fprintf(os.Stderr, "generate-rules: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("wrote pkg/monitoring/rules/alerts-rules.yaml")

	if err := rules.WritePrometheusRuleManifest(filepath.Join(rulesDir, "alerts.yaml"), *namespace); err != nil {
		fmt.Fprintf(os.Stderr, "generate-rules: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote pkg/monitoring/rules/alerts.yaml (namespace: %s)\n", *namespace)
}
