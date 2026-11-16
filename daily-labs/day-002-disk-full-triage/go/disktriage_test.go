package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const samples = "../samples"

var defaults = Thresholds{Warn: 85, Crit: 95, RetainDays: 7}

func TestGoldenReports(t *testing.T) {
	cases := []struct {
		node string
		code int
	}{{"node-01", 2}, {"node-02", 0}, {"node-03", 1}}
	for _, c := range cases {
		t.Run(c.node, func(t *testing.T) {
			rep, err := loadReport(filepath.Join(samples, c.node+".snap"), defaults)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(samples, "expected", c.node+".report"))
			if err != nil {
				t.Fatal(err)
			}
			if rep.Text() != string(want) {
				t.Errorf("report mismatch\n--- got\n%s--- want\n%s", rep.Text(), want)
			}
			if rep.ExitCode() != c.code {
				t.Errorf("exit code %d, want %d", rep.ExitCode(), c.code)
			}
		})
	}
}

func TestCeilPct(t *testing.T) {
	cases := []struct{ part, total, want int64 }{{87, 100, 87}, {871, 1000, 88}, {0, 0, 0}, {1, 3, 34}, {5, 5, 100}}
	for _, c := range cases {
		if got := ceilPct(c.part, c.total); got != c.want {
			t.Errorf("ceilPct(%d,%d) = %d, want %d", c.part, c.total, got, c.want)
		}
	}
}

func TestOwnerMountUsesPathComponents(t *testing.T) {
	mounts := []string{"/", "/boot", "/boot/efi", "/data"}
	cases := map[string]string{
		"/database":     "/", // string prefix of /data, but not inside it
		"/data":         "/data",
		"/data/models":  "/data",
		"/boot/efi/EFI": "/boot/efi",
		"/boot/grub2":   "/boot",
	}
	for path, want := range cases {
		if got := ownerMount(path, mounts); got != want {
			t.Errorf("ownerMount(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSharedDeletedInodeFreedOnce(t *testing.T) {
	rep, err := loadReport(filepath.Join(samples, "node-01.snap"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	want := "after cleanup 63% (deleted-open 5120 MiB + rotated 1736 MiB)"
	for _, d := range rep.Findings[0].Details {
		if d == want {
			return
		}
	}
	t.Errorf("missing %q in %v", want, rep.Findings[0].Details)
}

func TestThresholdFlags(t *testing.T) {
	if c := run([]string{"report", "-w", "98", "-c", "99", filepath.Join(samples, "node-01.snap")}); c != 0 {
		t.Errorf("exit %d, want 0", c)
	}
	rep, _ := loadReport(filepath.Join(samples, "node-01.snap"), Thresholds{Warn: 85, Crit: 88, RetainDays: 7})
	if rep.count("CRIT") != 3 {
		t.Errorf("crit=%d, want 3", rep.count("CRIT"))
	}
}

func TestFleetWorstExit(t *testing.T) {
	res, err := runFleet(samples, defaults, 3)
	if err != nil || len(res) != 3 {
		t.Fatalf("fleet: %v, %d results", err, len(res))
	}
	if res[0].report.Host != "node-01" || res[0].report.Result() != "CRIT" {
		t.Errorf("unexpected first result %+v", res[0])
	}
	if c := run([]string{"fleet", "-j", "2", samples}); c != 2 {
		t.Errorf("fleet exit %d, want 2", c)
	}
}

func TestRejectsBadInput(t *testing.T) {
	for _, text := range []string{"hello\n", header + "\nfs / xfs 1 2\n", header + "\ndu big /x\n"} {
		if _, err := ParseSnapshot(strings.NewReader(text), "bad"); err == nil {
			t.Errorf("expected error for %q", text)
		}
	}
	if c := run([]string{"report", "/nonexistent"}); c != 3 {
		t.Errorf("exit %d, want 3", c)
	}
}

func TestLiveSnapshot(t *testing.T) {
	text, err := TakeSnapshot(101, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSnapshot(strings.NewReader(text), "live")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.FS) == 0 {
		t.Fatal("no real filesystems found")
	}
	if r := Analyse(s, Thresholds{Warn: 101, Crit: 101, RetainDays: 7}); r.Result() != "OK" {
		t.Errorf("live report not OK:\n%s", r.Text())
	}
}
