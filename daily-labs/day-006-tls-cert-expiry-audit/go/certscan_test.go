package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const now = "2026-11-20T00:00:00Z"

func samples(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{"..", "samples"}, parts...)...)
}

func TestGolden(t *testing.T) {
	for _, tc := range []struct {
		host string
		rc   int
	}{{"node-01", 2}, {"node-02", 0}, {"node-03", 1}} {
		t.Run(tc.host, func(t *testing.T) {
			rep, err := auditFile(samples(t, tc.host+".certs"), now, 30, 14)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(samples(t, "expected", tc.host+".report"))
			if got := rep.Render(); got != string(want) {
				t.Errorf("report differs\n--- got\n%s--- want\n%s", got, want)
			}
			if rep.ExitCode() != tc.rc {
				t.Errorf("exit %d, want %d", rep.ExitCode(), tc.rc)
			}
		})
	}
}

func TestBadInventoriesAreInputErrors(t *testing.T) {
	bad, _ := filepath.Glob(samples(t, "bad", "*.certs"))
	if len(bad) == 0 {
		t.Fatal("no bad fixtures found")
	}
	for _, f := range bad {
		if _, err := auditFile(f, now, 30, 14); !errors.Is(err, ErrInput) {
			t.Errorf("%s: want ErrInput, got %v", f, err)
		}
	}
}

func TestBoundaries(t *testing.T) {
	for _, tc := range []struct{ na, want string }{
		{"2026-12-21T00:00:00Z", ""},
		{"2026-12-20T00:00:00Z", "WARN file:/x.pem expires in 30d "},
		{"2026-12-04T00:00:00Z", "CRIT file:/x.pem expires in 14d "},
		{"2026-11-20T00:00:00Z", "CRIT file:/x.pem expires in 0d "},
		{"2026-11-19T23:59:59Z", "CRIT file:/x.pem expired 1d ago "}, // floor, not truncation
	} {
		rep, err := Evaluate("h", []Cert{{"file:/x.pem", tc.na, "CN=a"}}, now, 30, 14)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if len(rep.Lines) != 0 || rep.OK != 1 {
				t.Errorf("%s: want OK, got %v", tc.na, rep.Lines)
			}
			continue
		}
		if len(rep.Lines) != 1 || rep.Lines[0][:len(tc.want)] != tc.want {
			t.Errorf("%s: got %v, want prefix %q", tc.na, rep.Lines, tc.want)
		}
	}
}

func TestReloadNeededOnlyWhenEndpointIsOlder(t *testing.T) {
	older, newer := "2027-01-01T00:00:00Z", "2027-06-01T00:00:00Z"
	stale, _ := Evaluate("h", []Cert{{"file:/x", newer, "CN=a"}, {"endpoint:h:443", older, "CN=a"}}, now, 30, 14)
	if stale.Warn != 1 {
		t.Errorf("stale endpoint: warn=%d, want 1", stale.Warn)
	}
	ahead, _ := Evaluate("h", []Cert{{"file:/x", older, "CN=a"}, {"endpoint:h:443", newer, "CN=a"}}, now, 30, 14)
	if ahead.Warn != 0 {
		t.Errorf("endpoint ahead of file: warn=%d, want 0", ahead.Warn)
	}
}

func TestThresholdOrderAndImpossibleDate(t *testing.T) {
	if _, err := Evaluate("h", nil, now, 10, 10); !errors.Is(err, ErrInput) {
		t.Error("equal thresholds must be rejected")
	}
	if _, err := toEpoch("2026-02-30T00:00:00Z"); !errors.Is(err, ErrInput) {
		t.Error("2026-02-30 must be rejected")
	}
}

func TestFleetExitCodeIsWorstHost(t *testing.T) {
	if rc := run([]string{"fleet", "--now", now, "-j", "3", samples(t)}); rc != 2 {
		t.Errorf("fleet exit %d, want 2", rc)
	}
}
