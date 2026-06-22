# CSI Volume Storage Path Health Monitoring for OpenShift Virtualization

**Author:** Shirly Radco
**Date:** May 2026
**Status:** Draft
**Components:** prometheus/procfs, prometheus/node_exporter, csi-volume-device-exporter, hyperconverged-cluster-operator


## 1. Problem Statement

When a storage path degrades on a node (e.g., a Fibre Channel link drops, an iSCSI target becomes unreachable, or an NVMe-oF controller dies), the impact is invisible at the Kubernetes workload level. Existing Prometheus metrics report storage path health at the node/device layer, but there is no way to correlate a degraded storage path with the specific PersistentVolume — and therefore the specific VM or pod — that it backs.

This creates a critical observability gap for OpenShift Virtualization operators:

- A multipath device loses redundancy, but the operator cannot identify which VMs are at risk.
- An NVMe-oF subsystem has a dead controller path, but there is no alert tying it to a workload.
- Storage teams see device-level failures but cannot communicate the blast radius to the virtualization team.

### User Stories

- As a cluster administrator, I want to be alerted when a storage path backing a VM's volume degrades, so I can take corrective action before the VM experiences I/O errors.
- As an SRE, I want a Prometheus metric that maps CSI volumes to their underlying block devices, so I can join storage path health metrics with workload identity.
- As a storage administrator, I want to see which multipath devices and NVMe-oF subsystems are in degraded state, with standard Prometheus metrics from node_exporter.


## 2. Solution Overview

The solution consists of three layers:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Prometheus Alert Join                        │
│  kube_persistentvolume_info                                    │
│       × csi_volume_node_device_info                            │
│            × node_dmmultipath_device_info                      │
│                 × node_dmmultipath_path_state                  │
│  ═══════════════════════════════════════                        │
│  → CSIVolumeMultipathDegraded alert                            │
└──────────┬───────────────────────────────┬─────────────────────┘
           │                               │
    ┌──────▼──────────────┐     ┌──────────▼──────────────┐
    │ csi-volume-device-  │     │ node_exporter            │
    │ exporter            │     │ (dmmultipath +           │
    │ (DaemonSet)         │     │  nvmesubsystem           │
    │                     │     │  collectors)             │
    │ Maps CSI volumes    │     │ Reads /sys/block/dm-*    │
    │ to block devices    │     │ and /sys/class/          │
    │ via kubelet         │     │ nvme-subsystem/          │
    │ vol_data.json +     │     │                          │
    │ /proc/1/mountinfo   │     │                          │
    └──────┬──────────────┘     └──────────┬──────────────┘
           │                               │
    ┌──────▼──────────────┐     ┌──────────▼──────────────┐
    │ prometheus/procfs    │     │ Linux sysfs             │
    │ (DM-multipath +     │     │ /sys/block/dm-*/dm/uuid │
    │  NVMe subsystem     │     │ /sys/block/dm-*/slaves/ │
    │  parsing)           │     │ /sys/class/nvme-subsys/  │
    └─────────────────────┘     └─────────────────────────┘
```


## 3. Components and Pull Requests

### 3.1 prometheus/procfs — Sysfs Parsing Libraries

These PRs add the Go parsing layer that node_exporter's collectors depend on.

**PR 1: DM-Multipath sysfs parsing**
- Repository: prometheus/procfs
- PR: https://github.com/prometheus/procfs/pull/796
- Branch: add-dmmultipath-sysfs-parsing

Adds `DMMultipathDevices()` method to `blockdevice.FS` that reads `/sys/block/dm-*` to discover Device Mapper multipath devices. For each device it reads the device name, UUID, suspended state, size, and enumerates underlying path devices with their state from `/sys/block/<path>/device/state`.

Multipath devices are identified by checking that `dm/uuid` starts with `mpath-`, which distinguishes them from LVM or other DM device types.

**PR 2: NVMe-oF subsystem sysfs parsing**
- Repository: prometheus/procfs
- PR: https://github.com/prometheus/procfs/pull/797
- Branch: add-nvmesubsystem-sysfs-parsing

Adds `NVMeSubsystemClass()` method to `sysfs.FS` that reads `/sys/class/nvme-subsystem/` to discover NVMe over Fabrics subsystems and their controller paths. For each subsystem it reads the NQN, model, serial, I/O policy, and enumerates controllers with their state, transport, and address.

### 3.2 prometheus/node_exporter — Storage Path Collectors

These PRs add new disabled-by-default collectors that expose storage path health metrics.

**PR 3: DM-Multipath collector**
- Repository: prometheus/node_exporter
- PR: https://github.com/prometheus/node_exporter/pull/3581
- Branch: add_collector_dmmultipath
- Depends on: procfs PR #796

Exposed metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| node_dmmultipath_device_info | Gauge (info) | device, sysfs_name, uuid | Multipath device identity |
| node_dmmultipath_device_active | Gauge | device, sysfs_name | 1 if device is not suspended |
| node_dmmultipath_device_size_bytes | Gauge | device, sysfs_name | Device size in bytes |
| node_dmmultipath_device_paths | Gauge | device, sysfs_name | Total number of paths |
| node_dmmultipath_device_paths_active | Gauge | device, sysfs_name | Paths in running/live state |
| node_dmmultipath_device_paths_failed | Gauge | device, sysfs_name | Paths not in active state |
| node_dmmultipath_path_state | Gauge | device, path, state | Raw SCSI device state per path |

No special permissions required — reads only world-readable sysfs attributes.

**PR 4: NVMe-oF Subsystem collector**
- Repository: prometheus/node_exporter
- PR: https://github.com/prometheus/node_exporter/pull/3579
- Branch: add_collector_multipath
- Depends on: procfs PR #797 and node_exporter PR #3581 (stacked)

Exposed metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| node_nvmesubsystem_info | Gauge (info) | subsystem, nqn, model, serial, iopolicy | Subsystem identity |
| node_nvmesubsystem_paths | Gauge | subsystem | Total controller paths |
| node_nvmesubsystem_paths_live | Gauge | subsystem | Controller paths in live state |
| node_nvmesubsystem_path_state | Gauge | subsystem, controller, transport, state | Per-controller state |

### 3.3 csi-volume-device-exporter — Volume-to-Device Mapping

- Repository: https://github.com/sradco/csi-volume-device-exporter (temporary; to be moved to openshift-virtualization org)
- Commit: b612b0e

A Prometheus exporter DaemonSet that maps CSI volumes to their underlying node block devices. It bridges the gap between Kubernetes storage abstractions (PV/PVC) and node-level block devices.

**Discovery strategy:**

1. Driver-specific JSON — Reads Trident tracking files and HPE deviceInfo.json for direct volume-to-device mapping.
2. Universal fallback — Reads kubelet's vol_data.json + /proc/1/mountinfo for all other CSI drivers. This covers any CSI driver that stages volumes via kubelet.

**Exposed metric:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| csi_volume_node_device_info | Gauge (1) | node, volume_handle, driver, device | Maps a CSI volume to its block device |

**Self-monitoring metrics:**

| Metric | Type | Labels |
|--------|------|--------|
| csi_volume_device_exporter_discovery_duration_seconds | Histogram | discoverer |
| csi_volume_device_exporter_discovery_errors_total | Counter | discoverer |
| csi_volume_device_exporter_volumes_discovered | Gauge | driver |
| csi_volume_device_exporter_last_successful_discovery_timestamp_seconds | Gauge | — |

**Alerts:**

| Alert | Severity | For | Description |
|-------|----------|-----|-------------|
| CSIVolumeMultipathDegraded | warning | 5m | A PV-backed DM-multipath device has at least one non-active path |
| CSIVolumeDeviceExporterDown | warning | 5m | No exporter targets scraped for 5 minutes |

**Security model:**

- Runs as root (UID 0) — required because kubelet writes vol_data.json with 0600 permissions
- privileged: false, allowPrivilegeEscalation: false, capabilities.drop: [ALL]
- readOnlyRootFilesystem: true
- All hostPath mounts are read-only
- No Kubernetes API access (automountServiceAccountToken: false)
- hostPID: true (required to read /proc/1/mountinfo)
- Distroless base image, static binary (CGO_ENABLED=0)

### 3.4 hyperconverged-cluster-operator — Lifecycle Management

- Repository: https://github.com/kubevirt/hyperconverged-cluster-operator
- Branch: TBD (new PR)
- Depends on: csi-volume-device-exporter image availability

Integrates the csi-volume-device-exporter into HCO as a managed operand, following the wasp-agent DaemonSet pattern.

**What HCO manages:**

| Resource | Type | Namespace |
|----------|------|-----------|
| csi-volume-device-exporter | ServiceAccount | openshift-cnv |
| csi-volume-device-exporter | SecurityContextConstraints | cluster-scoped |
| csi-volume-device-exporter | DaemonSet | openshift-cnv |
| csi-volume-device-exporter | PodMonitor | openshift-cnv |
| csi-volume-path-health | PrometheusRule | openshift-cnv |

**Gating strategy:** Always deployed on OpenShift — no conditional gate. Storage path monitoring is infrastructure-level observability and should be active whenever OpenShift Virtualization is installed. Any node may host CSI volumes regardless of whether it runs VMs.

**Node placement:** Uses `spec.infra` placement (not `spec.workloads`), consistent with passt-binding-cni and wasp-agent. The exporter monitors infrastructure-level storage paths, not VM-specific workloads.

**Image delivery:** The exporter image is injected via the `CSI_VOLUME_DEVICE_EXPORTER_IMAGE` environment variable on the HCO operator deployment, following the same pattern as `WASP_AGENT_IMAGE`.

**Files to create/modify in HCO:**

| File | Action |
|------|--------|
| controllers/handlers/csi-volume-device-exporter/daemonset.go | New — DaemonSet spec builder + handler |
| controllers/handlers/csi-volume-device-exporter/service_account.go | New — ServiceAccount handler |
| controllers/handlers/csi-volume-device-exporter/scc.go | New — SCC handler |
| controllers/handlers/csi-volume-device-exporter/podmonitor.go | New — PodMonitor handler |
| controllers/handlers/csi-volume-device-exporter/prometheusrule.go | New — PrometheusRule handler |
| controllers/operandhandler/operandHandler.go | Modify — register handlers |
| pkg/util/consts.go | Modify — add image env constant |
| cmd/.../main.go | Modify — add env existence check |


## 4. Alert PromQL Deep Dive

### CSIVolumeMultipathDegraded

This alert performs a three-way join across metrics from three different exporters:

```promql
(
  label_replace(
    label_replace(
      kube_persistentvolume_info,
      "volume_handle", "$1", "csi_volume_handle", "(.+)"
    )
    * on(volume_handle) group_left(device, node, driver)
    csi_volume_node_device_info,
    "sysfs_name", "$1", "device", "(.*)"
  )
  * on(sysfs_name, node) group_left(device)
  node_dmmultipath_device_info
)
* on(device, node) group_left()
(
  count by(device, node) (node_dmmultipath_path_state{state!~"running|live"} == 1) > 0
)
```

**Join stages explained:**

1. kube_persistentvolume_info (from kube-state-metrics) — provides PV name and CSI volume handle.
2. csi_volume_node_device_info (from csi-volume-device-exporter) — maps volume_handle to kernel block device (e.g., dm-5) and node.
3. node_dmmultipath_device_info (from node_exporter) — maps sysfs name (dm-5) to multipath device name (mpathA).
4. node_dmmultipath_path_state (from node_exporter) — detects non-active paths (offline, blocked, transport-offline, dead) on the multipath device.

**Why state!~"running|live" instead of state="faulty":** The path_state metric carries the raw SCSI device state from /sys/block/<dev>/device/state. Valid states include running, live, offline, blocked, transport-offline, and dead. There is no "faulty" state in the SCSI layer — that term is used by multipath-tools at the dm-multipath layer.

**Prerequisite:** All three metric sources (kube-state-metrics, csi-volume-device-exporter, node_exporter) must be scraped by the same Prometheus instance. On OpenShift, this means the csi-volume-device-exporter must deploy into a namespace with `openshift.io/cluster-monitoring=true` (e.g., openshift-cnv).


## 5. Deployment Architecture

### OpenShift (via HCO)

```
openshift-cnv namespace
├── DaemonSet: csi-volume-device-exporter  (1 pod per node)
│   ├── hostPath: /var/lib/kubelet (readOnly)
│   ├── hostPath: /proc (readOnly, for mountinfo)
│   ├── hostPath: /sys (readOnly, for sysfs)
│   └── PodMonitor → platform Prometheus scrape
├── PrometheusRule: csi-volume-path-health
│   ├── CSIVolumeMultipathDegraded
│   └── CSIVolumeDeviceExporterDown
├── ServiceAccount: csi-volume-device-exporter
└── SCC: csi-volume-device-exporter (cluster-scoped)
```

The HCO reconciler creates and maintains all resources. On upgrade, the DaemonSet image is updated via the operator's environment variable. On uninstall, all resources are cleaned up.

### Standalone Kubernetes

For non-OpenShift clusters, manual deployment via kubectl apply of the YAML manifests in the deploy/ directory.


## 6. node_exporter Collector Enablement on OpenShift

The dmmultipath and nvmesubsystem collectors are disabled by default in upstream node_exporter. For OpenShift:

**Option A — OpenShift node_exporter configuration:** Request that the OpenShift Monitoring team enables these collectors in the cluster-monitoring-operator's node_exporter configuration. This is the preferred path since it requires no user action.

**Option B — User enablement:** Users can enable the collectors via the cluster-monitoring-operator ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster-monitoring-config
  namespace: openshift-monitoring
data:
  config.yaml: |
    nodeExporter:
      collectors:
        enabled:
          - dmmultipath
          - nvmesubsystem
```

**Recommendation:** Work with the OpenShift Monitoring team to enable these collectors by default in a future OpenShift release for clusters that use multipath or NVMe-oF storage.


## 7. Testing

### Unit Tests

- procfs: TestDMMultipathDevices, TestDMMultipathDevicesFiltersNonMultipath, TestNVMeSubsystemClass, TestNVMeSubsystemClassNotPresent, TestNVMeSubsystemClassEmpty, TestNVMeSubsystemMultipleSubsystems
- node_exporter: TestDMMultipathMetrics, TestDMMultipathNoDevices, TestIsPathActive, TestNVMeSubsystemMetrics, TestNVMeSubsystemNoDevices, TestNormalizeControllerState
- csi-volume-device-exporter: full pkg/ test suite + promtool alert rule unit tests
- HCO: handler unit tests following wasp-agent/passt test patterns

### Cluster Validation (Completed)

Tested on a live OpenShift 4.21 cluster (6 nodes, Ceph RBD + Cinder storage):

1. Built custom node_exporter with both collectors, deployed as DaemonSet with synthetic sysfs fixtures (fake DM-multipath and NVMe-oF devices via ConfigMap + init container).
2. Verified all metrics ingested by platform Prometheus (openshift-monitoring/k8s).
3. Validated the three-way PromQL join — each stage produces correct results:
   - Stage 1: kube_persistentvolume_info → csi_volume_node_device_info (2 PVs mapped)
   - Stage 2: sysfs_name → multipath device name mapping via device_info
   - Stage 3: Non-active path detection (synthetic sdl in offline state detected)
4. Confirmed CSIVolumeDeviceExporterDown alert is correctly NOT firing when exporter is healthy.
5. Verified no metric duplication in the platform Prometheus.

### Alert Unit Tests

promtool-based unit tests covering:
- Alert fires after 5m of continuous offline path, clears on recovery
- Alert does not fire when all paths are running
- Alert does not fire for non-CSI PVs (empty csi_volume_handle)
- Alert does not fire when offline path is on a different node than the PV
- CSIVolumeDeviceExporterDown fires after 5m absence, clears on target return


## 8. Dependencies and Ordering

```
procfs PR #796 (DM-multipath parsing)
    └──► node_exporter PR #3581 (dmmultipath collector)
              └──► node_exporter PR #3579 (nvmesubsystem collector)

procfs PR #797 (NVMe subsystem parsing)
    └──► node_exporter PR #3579 (nvmesubsystem collector)

csi-volume-device-exporter (standalone, no upstream deps)
    └──► HCO integration PR (depends on exporter image)

OpenShift CMO collector enablement (independent track)
```

**Merge order:**
1. procfs PRs #796 and #797 (can merge in parallel)
2. node_exporter PR #3581 (dmmultipath), then #3579 (nvmesubsystem, stacked)
3. csi-volume-device-exporter repo creation under openshift-virtualization org
4. HCO integration PR
5. OpenShift CMO collector enablement (separate request to monitoring team)


## 9. Decisions

1. **Repository location:** The csi-volume-device-exporter will live under github.com/openshift-virtualization/.

2. **node_exporter collector enablement:** We will request default enablement in OpenShift's node_exporter via the cluster-monitoring-operator. This ensures zero user configuration and consistent observability across all OpenShift Virtualization clusters.

3. **HCO feature gate:** No feature gate. The exporter is always-on — deployed unconditionally when OpenShift Virtualization is installed. Storage path observability is infrastructure-level and should be active from day one.

4. **NVMe-oF alert:** Yes, we will add a CSIVolumeNVMeSubsystemDegraded alert following the same pattern as CSIVolumeMultipathDegraded. The PromQL join will use the NVMe subsystem/controller path model (node_nvmesubsystem_path_state, node_nvmesubsystem_info) instead of the DM device/slave model.

5. **Dashboard:** Yes, we will add a Perses dashboard panel showing storage path health per VM. This will leverage the same PromQL joins used by the alerts to visualize multipath and NVMe-oF path status correlated to workloads.


## 10. References

- csi-volume-device-exporter: https://github.com/sradco/csi-volume-device-exporter
- node_exporter dmmultipath PR: https://github.com/prometheus/node_exporter/pull/3581
- node_exporter nvmesubsystem PR: https://github.com/prometheus/node_exporter/pull/3579
- procfs DM-multipath PR: https://github.com/prometheus/procfs/pull/796
- procfs NVMe subsystem PR: https://github.com/prometheus/procfs/pull/797
- Alert runbook — CSIVolumeMultipathDegraded: docs/runbooks/CSIVolumeMultipathDegraded.md
- Alert runbook — CSIVolumeDeviceExporterDown: docs/runbooks/CSIVolumeDeviceExporterDown.md
- KubeVirt monitoring patterns: https://github.com/kubevirt/kubevirt/tree/main/pkg/monitoring
- HCO wasp-agent pattern: hyperconverged-cluster-operator/controllers/handlers/wasp-agent/
