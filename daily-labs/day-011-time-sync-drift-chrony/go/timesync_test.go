package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func samples(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{"..", "samples"}, parts...)...)
}

func TestGolden(t *testing.T) {
	for _, tc := range []struct {
		host string
		rc   int
	}{{"node-01", 2}, {"node-02", 0}, {"node-03", 1}, {"node-04", 2}, {"node-05", 1}} {
		t.Run(tc.host, func(t *testing.T) {
			rep, err := auditFile(samples(t, tc.host+".chrony"), 10, 100)
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
	bad, _ := filepath.Glob(samples(t, "bad", "*.chrony"))
	if len(bad) == 0 {
		t.Fatal("no bad fixtures found")
	}
	for _, f := range bad {
		if _, err := auditFile(f, 10, 100); !errors.Is(err, ErrInput) {
			t.Errorf("%s: want ErrInput, got %v", f, err)
		}
	}
}

func srcs(n int) []Source {
	s := []Source{{"*", "a", "377"}, {"+", "b", "377"}, {"+", "c", "377"}}
	return s[:n]
}

func TestOffsetBoundaries(t *testing.T) {
	for _, tc := range []struct {
		ns   int64
		want string
	}{
		{10_000_000, ""}, // exactly warn: strict ">" keeps it OK
		{10_000_001, "WARN offset 10.000ms fast (warn 10ms)"},
		{100_000_000, "WARN offset 100.000ms fast (warn 10ms)"},
		{100_000_001, "CRIT offset 100.000ms fast (crit 100ms)"},
		{1_999_999_999, "CRIT offset 1999.999ms fast (crit 100ms)"}, // truncated, not rounded to 2000.000
	} {
		rep, err := Evaluate(Inventory{Host: "h", Leap: "Normal", Stratum: 2, Direction: "fast", OffsetNs: tc.ns, Sources: srcs(3)}, 10, 100)
		if err != nil {
			t.Fatal(err)
		}
		want := []string(nil)
		if tc.want != "" {
			want = []string{tc.want}
		}
		if !slices.Equal(rep.Lines, want) {
			t.Errorf("%d ns: got %v, want %v", tc.ns, rep.Lines, want)
		}
	}
}

func TestUnsynchronisedSuppressesOffsetAndStratum(t *testing.T) {
	rep, _ := Evaluate(Inventory{Host: "h", Leap: "Not synchronised", Stratum: 0, Direction: "slow", OffsetNs: 9e8, Sources: srcs(3)}, 10, 100)
	if !slices.Equal(rep.Lines, []string{"CRIT unsynchronised (leap status: Not synchronised)"}) {
		t.Errorf("got %v", rep.Lines)
	}
}

func TestFalsetickerUnreachableAndQuorum(t *testing.T) {
	inv := Inventory{Host: "h", Leap: "Normal", Stratum: 2, Direction: "fast",
		Sources: []Source{{"*", "a", "377"}, {"x", "b", "377"}, {"?", "c", "0"}}}
	rep, _ := Evaluate(inv, 10, 100)
	if !slices.Equal(rep.Lines, []string{"WARN source b falseticker", "WARN source c unreachable (reach 0)"}) {
		t.Errorf("got %v", rep.Lines)
	}
	inv.Sources = inv.Sources[:2]
	rep, _ = Evaluate(inv, 10, 100)
	if rep.Warn != 2 { // falseticker + only 2 sources
		t.Errorf("two sources: warn=%d, want 2 (%v)", rep.Warn, rep.Lines)
	}
}

func TestThresholdOrder(t *testing.T) {
	if _, err := Evaluate(Inventory{}, 100, 100); !errors.Is(err, ErrInput) {
		t.Error("equal thresholds must be rejected")
	}
}

func TestFleetExitCodeIsWorstHost(t *testing.T) {
	if rc := run([]string{"fleet", "-j", "3", samples(t)}); rc != 2 {
		t.Errorf("fleet exit %d, want 2", rc)
	}
}
