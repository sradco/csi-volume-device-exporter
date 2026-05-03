package discovery

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/sys/mountinfo"
)

// ParseMountInfo reads and parses /proc/1/mountinfo from the given procfs path.
func ParseMountInfo(procPath string) ([]*mountinfo.Info, error) {
	f, err := os.Open(filepath.Join(procPath, "1", "mountinfo"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return mountinfo.GetMountsFromReader(f, nil)
}

// FindMountByMountpoint returns the mount entry matching the given mountpoint exactly.
func FindMountByMountpoint(mounts []*mountinfo.Info, mountpoint string) *mountinfo.Info {
	for _, m := range mounts {
		if m.Mountpoint == mountpoint {
			return m
		}
	}
	return nil
}

// FindMountUnder returns the first mount whose mountpoint is exactly dir or
// is a direct child of dir. This handles CSI drivers (e.g. Ceph RBD) that
// stage the block device under a subdirectory of the globalmount path, such
// as: .../globalmount/<volumeHandle>
func FindMountUnder(mounts []*mountinfo.Info, dir string) *mountinfo.Info {
	prefix := dir + "/"
	for _, m := range mounts {
		if m.Mountpoint == dir || (len(m.Mountpoint) > len(prefix) &&
			m.Mountpoint[:len(prefix)] == prefix &&
			!strings.Contains(m.Mountpoint[len(prefix):], "/")) {
			return m
		}
	}
	return nil
}

// IsNetworkFS returns true if the filesystem type is a network filesystem (NFS, CIFS, etc.).
func IsNetworkFS(fsType string) bool {
	switch fsType {
	case "nfs", "nfs4", "cifs", "smbfs", "glusterfs", "ceph", "lustre", "9p":
		return true
	}
	return false
}
