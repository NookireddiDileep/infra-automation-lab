package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const samples = "../samples"

func TestGoldenReports(t *testing.T) {
	cases := map[string]int{"node-01": 2, "node-02": 0, "node-03": 1}
	for node, code := range cases {
		t.Run(node, func(t *testing.T) {
			rep, err := comparePair(filepath.Join(samples, node+"-pre.txt"), filepath.Join(samples, node+"-post.txt"), 90)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(filepath.Join(samples, "expected", node+".report"))
			if rep.Text() != string(want) {
				t.Errorf("report mismatch\n--- got\n%s--- want\n%s", rep.Text(), want)
			}
			if rep.ExitCode() != code {
				t.Errorf("exit code %d, want %d", rep.ExitCode(), code)
			}
		})
	}
}

func TestFleetSummaryAndWorstExit(t *testing.T) {
	res, err := runFleet(samples, 90, 3)
	if err != nil || len(res) != 3 {
		t.Fatalf("fleet: %v, %d results", err, len(res))
	}
	if res[0].host != "node-01" || res[0].report.Result() != "CRIT" {
		t.Errorf("unexpected first result %+v", res[0])
	}
	if code := run([]string{"fleet", "-j", "2", samples}); code != 2 {
		t.Errorf("fleet exit %d, want 2 (worst host is CRIT)", code)
	}
}

func TestRejectsNonSnapshot(t *testing.T) {
	if _, err := ParseSnapshot(strings.NewReader("hello\n"), "bad"); err == nil {
		t.Fatal("expected error")
	}
	if code := run([]string{"compare", "/nonexistent", filepath.Join(samples, "node-01-post.txt")}); code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
}

func TestParseProcNetTCP(t *testing.T) {
	text := "  sl local rem st\n   0: 00000000:0016 00000000:0000 0A 0\n   1: 0100007F:1F90 0100007F:9C40 01 0\n"
	got := parseProcNetTCP(text)
	if len(got) != 1 || got[0] != 22 {
		t.Errorf("got %v, want [22]", got)
	}
}

func TestLiveSnapshotSelfCompare(t *testing.T) {
	text, err := TakeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSnapshot(strings.NewReader(text), "live")
	if err != nil {
		t.Fatal(err)
	}
	if r := Compare(s, s, 101); r.Result() != "OK" {
		t.Errorf("self-compare not OK:\n%s", r.Text())
	}
}
