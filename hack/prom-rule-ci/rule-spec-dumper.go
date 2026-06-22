// rule-spec-dumper writes the Prometheus rule groups to a file so that
// verify-rules.sh can pass them to promtool for linting and unit testing.
// This avoids committing a static YAML copy solely for CI purposes.
//
// Usage: rule-spec-dumper <output-file>
package main

import (
	"fmt"
	"os"

	"github.com/sradco/csi-volume-device-exporter/pkg/monitoring/rules"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: rule-spec-dumper <output-file>\n")
		os.Exit(1)
	}
	if err := rules.WritePrometheusRulesFile(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "rule-spec-dumper: %v\n", err)
		os.Exit(1)
	}
}
