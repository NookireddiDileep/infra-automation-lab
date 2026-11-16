package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const header = "# disktriage snapshot v1"

var (
	realFSTypes = map[string]bool{"xfs": true, "ext4": true, "ext3": true, "btrfs": true, "vfat": true, "nfs": true, "nfs4": true}
	rotatedRE   = regexp.MustCompile(`(\.gz|\.xz|\.bz2|\.[0-9]+|-[0-9]{8})$`)
)

// FS is one "fs MOUNT FSTYPE USED_KB AVAIL_KB IUSED IFREE" line.
type FS struct {
	Mount, FSType                 string
	UsedKB, AvailKB, IUsed, IFree int64
}

// DU is one "du KB PATH" line.
type DU struct {
	KB   int64
	Path string
}

// Deleted is one "deleted PID INODE KB COMM PATH" line: a deleted file still held open.
type Deleted struct {
	PID, Inode, KB int64
	Comm, Path     string
}

// Rotated is one "rotated AGE_DAYS KB PATH" line: a rotated log, a safe cleanup candidate.
type Rotated struct {
	AgeDays, KB int64
	Path        string
}

// Snapshot is a parsed v1 snapshot.
type Snapshot struct {
	Host    string
	FS      []FS
	DU      []DU
	Deleted []Deleted
	Rotated []Rotated
}

// ParseSnapshot reads a v1 snapshot. Malformed known lines are an error; unknown keywords are ignored.
func ParseSnapshot(r io.Reader, name string) (*Snapshot, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	if !sc.Scan() || sc.Text() != header {
		return nil, fmt.Errorf("%s: not a disktriage v1 snapshot", name)
	}
	s := &Snapshot{Host: "unknown"}
	n := 1
	for sc.Scan() {
		n++
		line := sc.Text()
		if strings.HasPrefix(line, "# host=") {
			s.Host = strings.TrimPrefix(strings.Fields(line)[1], "host=")
			continue
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, _, _ := strings.Cut(line, " ")
		if err := s.parseLine(kind, line); err != nil {
			return nil, fmt.Errorf("%s line %d: malformed %q line: %q", name, n, kind, line)
		}
	}
	return s, sc.Err()
}

var errMalformed = errors.New("malformed")

func ints(fields ...string) ([]int64, error) {
	out := make([]int64, len(fields))
	for i, f := range fields {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil || v < 0 {
			return nil, errMalformed
		}
		out[i] = v
	}
	return out, nil
}

func (s *Snapshot) parseLine(kind, line string) error {
	switch kind {
	case "fs":
		f := strings.Fields(line)
		if len(f) != 7 {
			return errMalformed
		}
		v, err := ints(f[3:]...)
		if err != nil {
			return err
		}
		s.FS = append(s.FS, FS{f[1], f[2], v[0], v[1], v[2], v[3]})
	case "du":
		f := strings.SplitN(line, " ", 3)
		if len(f) != 3 {
			return errMalformed
		}
		v, err := ints(f[1])
		if err != nil {
			return err
		}
		s.DU = append(s.DU, DU{v[0], f[2]})
	case "deleted":
		f := strings.SplitN(line, " ", 6)
		if len(f) != 6 {
			return errMalformed
		}
		v, err := ints(f[1], f[2], f[3])
		if err != nil {
			return err
		}
		s.Deleted = append(s.Deleted, Deleted{v[0], v[1], v[2], f[4], f[5]})
	case "rotated":
		f := strings.SplitN(line, " ", 4)
		if len(f) != 4 {
			return errMalformed
		}
		v, err := ints(f[1], f[2])
		if err != nil {
			return err
		}
		s.Rotated = append(s.Rotated, Rotated{v[0], v[1], f[3]})
	}
	return nil
}

// ---------- live collection ----------

func listMounts() ([][2]string, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out [][2]string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !realFSTypes[f[2]] {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		if !seen[mp] {
			seen[mp] = true
			out = append(out, [2]string{mp, f[2]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}

// duDepth1 is `du -xk -d 1 MOUNT`: allocated KB per first-level directory, one filesystem, hard links once.
func duDepth1(mount string) []DU {
	var root syscall.Stat_t
	if syscall.Lstat(mount, &root) != nil {
		return nil
	}
	entries, err := os.ReadDir(mount)
	if err != nil {
		return nil
	}
	var out []DU
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		top := filepath.Join(mount, e.Name())
		var st syscall.Stat_t
		if syscall.Lstat(top, &st) != nil || st.Dev != root.Dev {
			continue // another filesystem mounted here
		}
		seen := map[uint64]bool{}
		var blocks int64
		_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable: skip, like du's "Permission denied"
			}
			var s syscall.Stat_t
			if syscall.Lstat(p, &s) != nil || s.Dev != root.Dev {
				if d != nil && d.IsDir() && p != top {
					return filepath.SkipDir
				}
				return nil
			}
			if !seen[s.Ino] {
				seen[s.Ino] = true
				blocks += s.Blocks
			}
			return nil
		})
		out = append(out, DU{blocks * 512 / 1024, top})
	}
	return out
}

func scanDeleted() []Deleted {
	pids, _ := filepath.Glob("/proc/[0-9]*")
	found := map[[2]int64]Deleted{}
	for _, pdir := range pids {
		pid, err := strconv.ParseInt(filepath.Base(pdir), 10, 64)
		if err != nil {
			continue
		}
		commB, err := os.ReadFile(pdir + "/comm")
		if err != nil {
			continue
		}
		comm := strings.ReplaceAll(strings.TrimSpace(string(commB)), " ", "_")
		fds, err := os.ReadDir(pdir + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			p := pdir + "/fd/" + fd.Name()
			target, err := os.Readlink(p)
			if err != nil || !strings.HasSuffix(target, " (deleted)") || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "/memfd:") {
				continue
			}
			var st syscall.Stat_t
			if syscall.Stat(p, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
				continue
			}
			found[[2]int64{pid, int64(st.Ino)}] = Deleted{pid, int64(st.Ino), st.Blocks * 512 / 1024, comm, strings.TrimSuffix(target, " (deleted)")}
		}
	}
	out := make([]Deleted, 0, len(found))
	for _, d := range found {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PID != out[j].PID {
			return out[i].PID < out[j].PID
		}
		return out[i].Inode < out[j].Inode
	})
	return out
}

func scanRotated(logdir string, now time.Time) []Rotated {
	var out []Rotated
	_ = filepath.WalkDir(logdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || !rotatedRE.MatchString(d.Name()) {
			return nil
		}
		var st syscall.Stat_t
		if syscall.Lstat(p, &st) != nil {
			return nil
		}
		age := int64(now.Sub(time.Unix(st.Mtim.Sec, st.Mtim.Nsec)).Hours()) / 24
		out = append(out, Rotated{age, st.Blocks * 512 / 1024, p})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// TakeSnapshot collects a v1 snapshot of this machine. du runs concurrently, one goroutine per hot filesystem.
func TakeSnapshot(duMinPct int64, logdir string) (string, error) {
	now := time.Now()
	mounts, err := listMounts()
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n# host=%s taken=%s\n", header, host, now.UTC().Format("2006-01-02T15:04:05Z"))

	var hot []string
	for _, m := range mounts {
		var sv syscall.Statfs_t
		if syscall.Statfs(m[0], &sv) != nil {
			continue
		}
		bs := int64(sv.Frsize)
		used := (int64(sv.Blocks) - int64(sv.Bfree)) * bs / 1024
		avail := int64(sv.Bavail) * bs / 1024 // what non-root can use, like df
		iused := int64(sv.Files) - int64(sv.Ffree)
		fmt.Fprintf(&b, "fs %s %s %d %d %d %d\n", m[0], m[1], used, avail, iused, int64(sv.Ffree))
		if ceilPct(used, used+avail) >= duMinPct {
			hot = append(hot, m[0])
		}
	}

	results := make([][]DU, len(hot))
	var wg sync.WaitGroup
	for i, m := range hot {
		wg.Add(1)
		go func(i int, m string) { // each goroutine writes only its own slot: no mutex needed
			defer wg.Done()
			results[i] = duDepth1(m)
		}(i, m)
	}
	wg.Wait()
	for _, r := range results {
		for _, d := range r {
			fmt.Fprintf(&b, "du %d %s\n", d.KB, d.Path)
		}
	}
	for _, d := range scanDeleted() {
		fmt.Fprintf(&b, "deleted %d %d %d %s %s\n", d.PID, d.Inode, d.KB, d.Comm, d.Path)
	}
	for _, r := range scanRotated(logdir, now) {
		fmt.Fprintf(&b, "rotated %d %d %s\n", r.AgeDays, r.KB, r.Path)
	}
	return b.String(), nil
}
