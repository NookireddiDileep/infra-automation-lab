package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var realFSTypes = map[string]bool{"xfs": true, "ext4": true, "ext3": true, "btrfs": true, "vfat": true, "nfs": true, "nfs4": true}

type mount struct{ path, fstype string }

func listMounts() ([]mount, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	seen := map[mount]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && realFSTypes[f[2]] {
			seen[mount{f[1], f[2]}] = true
		}
	}
	var out []mount
	for m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// parseProcNetTCP returns listening ports from /proc/net/tcp[6] content (state 0A, hex port).
func parseProcNetTCP(text string) []int {
	var ports []int
	for i, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 4 || f[3] != "0A" {
			continue
		}
		idx := strings.LastIndex(f[1], ":")
		if p, err := strconv.ParseInt(f[1][idx+1:], 16, 32); err == nil {
			ports = append(ports, int(p))
		}
	}
	return ports
}

func listServices() []string {
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--plain").Output()
	if err != nil {
		logf("WARN", "systemctl unavailable (container?), no services recorded")
		return nil
	}
	var svcs []string
	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			svcs = append(svcs, f[0])
		}
	}
	return svcs
}

// fsUsedPct uses the same formula as df: used / (used + avail), rounded up.
func fsUsedPct(path string) (int, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	used := (st.Blocks - st.Bfree) * uint64(st.Bsize)
	avail := st.Bavail * uint64(st.Bsize)
	if used+avail == 0 {
		return 0, nil
	}
	return int((used*100 + used + avail - 1) / (used + avail)), nil
}

// TakeSnapshot captures this host's state in the v1 format.
func TakeSnapshot() (string, error) {
	mounts, err := listMounts()
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	host = strings.SplitN(host, ".", 2)[0]
	rel, _ := os.ReadFile("/proc/sys/kernel/osrelease")

	items := map[string]bool{}
	for _, m := range mounts {
		items[fmt.Sprintf("mount %s %s", m.path, m.fstype)] = true
	}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if data, err := os.ReadFile(f); err == nil {
			for _, p := range parseProcNetTCP(string(data)) {
				items[fmt.Sprintf("port tcp/%d", p)] = true
			}
		}
	}
	for _, s := range listServices() {
		items["service "+s] = true
	}
	var sorted []string
	for k := range items {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var b strings.Builder
	fmt.Fprintln(&b, header)
	fmt.Fprintf(&b, "# host=%s taken=%s\n", host, time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&b, "kernel %s\n", strings.TrimSpace(string(rel)))
	for _, s := range sorted {
		fmt.Fprintln(&b, s)
	}
	for _, m := range mounts {
		if pct, err := fsUsedPct(m.path); err == nil {
			fmt.Fprintf(&b, "fsuse %s %d\n", m.path, pct)
		}
	}
	return b.String(), nil
}
