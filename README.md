# csi-volume-device-exporter

A Prometheus exporter that maps CSI volumes to their underlying node block devices. Enables correlating storage path health metrics (DM-multipath, NVMe-oF) with specific Kubernetes workloads.

## Problem

`node_exporter` metrics expose storage path health at the node level (`node_dmmultipath_path_state`, `node_nvmesubsystem_path_state`), but no metric exists to map a CSI volume (PV/PVC) to its underlying node block device. Without this mapping, operators cannot determine which workloads are impacted when a storage path degrades.

## How It Works

The exporter runs as a DaemonSet on every node, reads kubelet's CSI staging metadata and the Linux mount table, and emits a single info metric:

```
csi_volume_node_device_info{node="worker-1", volume_handle="csi-vol-id", driver="csi.trident.netapp.io", device="dm-5"} 1
```

### Discovery Strategy

- **Driver-specific JSON:** Reads Trident tracking files and HPE `deviceInfo.json` for direct volume-to-device mapping.
- **Universal fallback:** Reads kubelet's `vol_data.json` + `/proc/1/mountinfo` for all other CSI drivers.

### Supported Drivers

Works with **any** CSI driver that stages volumes via kubelet, including:
- NetApp Trident (iSCSI/FC/NVMe-oF)
- Dell PowerStore, PowerFlex, Unity XT (iSCSI/FC/NVMe-oF)
- HPE CSI (iSCSI/FC)
- IBM Block CSI (FC/iSCSI)
- Any future CSI driver

## Quick Start

```bash
# Build
make build

# Run locally (for testing against a real kubelet root)
NODE_NAME=$(hostname) ./bin/csi-volume-device-exporter \
  --listen-address=:9091 \
  --poll-interval=30s \
  --host-proc=/proc \
  --host-sys=/sys \
  --host-kubelet=/var/lib/kubelet \
  --kubelet-root=/var/lib/kubelet
```

### Kubernetes

```bash
kubectl apply -f deploy/daemonset.yaml
```

### OpenShift

The exporter must be scraped by the **platform Prometheus** (`openshift-monitoring`), not user
workload monitoring. This is required because the PromQL alert joins
`csi_volume_node_device_info` with `node_dmmultipath_path_state` from `node-exporter`, and both
metrics must live in the same Prometheus instance for the join to work.

The recommended namespace is **`openshift-cnv`** — it already carries
`openshift.io/cluster-monitoring=true`, which causes the platform Prometheus to scrape it
directly, exactly like it scrapes KubeVirt's own metrics.

```bash
# 1. Apply the SCC and bind the service account in openshift-cnv
oc apply -n openshift-cnv -f deploy/scc.yaml
oc adm policy add-scc-to-user csi-volume-device-exporter \
  -z csi-volume-device-exporter -n openshift-cnv

# 2. Deploy the DaemonSet into openshift-cnv
oc apply -n openshift-cnv -f deploy/daemonset.yaml

# 3. Deploy the PodMonitor — platform Prometheus picks it up automatically
#    because openshift-cnv has openshift.io/cluster-monitoring=true
oc apply -n openshift-cnv -f deploy/podmonitor.yaml
```

> **Why not user workload monitoring?**
> If the exporter is deployed into a user workload namespace, its metrics land in a separate
> Prometheus instance. The PromQL join with `node_dmmultipath_path_state` (which is in the
> platform Prometheus) will always return empty. Additionally, Thanos federation can cause
> the metric to appear twice in the console — once from each Prometheus.

> **Do not** label the exporter namespace with `openshift.io/cluster-monitoring=true` unless
> it is `openshift-cnv` or another OpenShift-managed namespace. Doing so on a custom namespace
> causes the user workload Prometheus to skip it while the platform Prometheus may not pick
> it up either, depending on cluster configuration.

## Configuration

| Flag | Default | Description |
|---|---|---|
| `--listen-address` | `:9091` | Address for metrics and healthz endpoints |
| `--poll-interval` | `30s` | Interval between discovery cycles |
| `--log-level` | `info` | Log level (debug, info, warn, error) |
| `--host-proc` | `/host/proc` | Path to host `/proc` mount inside the container |
| `--host-sys` | `/host/sys` | Path to host `/sys` mount inside the container |
| `--host-kubelet` | `/host/kubelet` | Path to host kubelet root mount inside the container (for reading files) |
| `--kubelet-root` | `/var/lib/kubelet` | Actual kubelet root path on the host (for matching mountinfo entries) |
| `--host-trident-tracking` | `/host/trident/tracking` | Path to host Trident tracking dir inside the container |
| `--version` | — | Print version and exit |

| Environment Variable | Required | Description |
|---|---|---|
| `NODE_NAME` | Yes | Kubernetes node name (set via Downward API `spec.nodeName`) |
| `RUNBOOK_URL_TEMPLATE` | No | printf-style template for alert runbook URLs, e.g. `https://example.com/runbooks/%s.md` |

## Metrics

### Volume mapping

| Metric | Type | Labels | Description |
|---|---|---|---|
| `csi_volume_node_device_info` | Gauge (always 1) | `node`, `volume_handle`, `driver`, `device` | Maps a CSI volume to its block device |

### Operational (self-monitoring)

| Metric | Type | Labels |
|---|---|---|
| `csi_volume_device_exporter_discovery_duration_seconds` | Histogram | `discoverer` |
| `csi_volume_device_exporter_discovery_errors_total` | Counter | `discoverer` |
| `csi_volume_device_exporter_volumes_discovered` | Gauge | `driver` |
| `csi_volume_device_exporter_last_successful_discovery_timestamp_seconds` | Gauge | — |

## Alerts

Alert rules are defined in Go under `pkg/monitoring/rules/alerts/` and rendered to Kubernetes-deployable YAML:

| Alert | Severity | Description |
|---|---|---|
| `CSIVolumeMultipathDegraded` | warning | A PV-backed DM-multipath device has at least one faulty path |
| `CSIVolumeDeviceExporterDown` | warning | No exporter targets have been scraped for 5 minutes |

Deploy the alert rules:

```bash
kubectl apply -f pkg/monitoring/rules/alerts.yaml
```

Runbooks are in [`docs/runbooks/`](docs/runbooks/).

## PromQL Examples

**Which PVs have degraded multipath?**

```promql
(
  label_replace(kube_persistentvolume_info, "volume_handle", "$1", "csi_volume_handle", "(.+)")
  * on(volume_handle) group_left(device, node, driver)
  csi_volume_node_device_info
)
* on(device, node) group_left()
label_replace(
  count by(sysfs_name, node) (node_dmmultipath_path_state{state="faulty"} == 1) > 0,
  "device", "$1", "sysfs_name", "(.*)"
)
```

> Requires node_exporter scrape relabeling that copies `instance` → `node` (stripping the port).

## Security

The exporter runs as root (UID 0) with:
- `privileged: false`
- `allowPrivilegeEscalation: false`
- `capabilities.drop: [ALL]`
- `readOnlyRootFilesystem: true`
- `seccompProfile: RuntimeDefault`
- `seLinuxOptions.level: "s0"`
- No Kubernetes API server access (`automountServiceAccountToken: false`)
- All hostPath mounts are read-only
- JSON file reads limited to 1 MiB (prevents memory exhaustion)
- Path traversal protection (all paths must be absolute, no `..` allowed)
- Recursion depth-limited sysfs walks (prevents stack overflow)
- Distroless base image (no shell, no package manager)
- Static binary (`CGO_ENABLED=0`)

Root is required because kubelet writes `vol_data.json` with `0600` permissions.

## Development

```bash
make build          # Build binary
make test           # Run unit tests with race detector
make test-e2e       # Run e2e tests (requires built binary)
make test-alerts    # Lint and unit-test Prometheus alert rules via promtool
make generate       # Regenerate alert YAML files from Go definitions
make lint           # Run golangci-lint
make vet            # Run go vet
make image          # Build container image
make push           # Push container image
make clean          # Remove build artifacts
```

### Repository Structure

```
cmd/exporter/           — binary entrypoint
pkg/
  discovery/            — volume-to-block-device mapping (kubelet, Trident, HPE)
  monitoring/
    metrics/            — Prometheus metric definitions
    rules/
      alerts/           — typed alert rule definitions (Go)
      alerts.yaml       — Kubernetes PrometheusRule CRD manifest
      alerts-rules.yaml — plain Prometheus groups file (for reference)
      rules.go          — PrometheusRule builder
deploy/                 — DaemonSet, PodMonitor, SCC manifests
docs/runbooks/          — alert runbooks
hack/prom-rule-ci/      — CI tooling: promtool lint + unit tests
tools/generate-rules/   — regenerates alert YAML from Go definitions
```

## License

Apache License 2.0
