package main

import (
	"os"
	"path/filepath"
	"testing"
)

const samples = "../samples"

func load(t *testing.T, pvcs, scs string) (*Result, error) {
	t.Helper()
	p, err := loadList(filepath.Join(samples, pvcs), "pvcs")
	if err != nil {
		return nil, err
	}
	s, err := loadList(filepath.Join(samples, scs), "storageclasses")
	if err != nil {
		return nil, err
	}
	return Check(p, s)
}

func TestGolden(t *testing.T) {
	for _, c := range []struct{ name, verdict string }{{"ocp-prod", "CRIT"}, {"ocp-lab", "CRIT"}, {"ocp-stage", "WARN"}, {"ocp-dev", "OK"}} {
		res, err := load(t, c.name+".pvcs.json", c.name+".storageclasses.json")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, verdict := Render(c.name, res)
		want, err := os.ReadFile(filepath.Join(samples, "expected", c.name+".report"))
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) || verdict != c.verdict {
			t.Errorf("%s: verdict %s want %s\n--- got\n%s--- want\n%s", c.name, verdict, c.verdict, got, want)
		}
	}
}

func TestBadInputIsRefused(t *testing.T) {
	for _, c := range [][2]string{
		{"bad/not-a-list.json", "ocp-dev.storageclasses.json"},
		{"ocp-dev.pvcs.json", "bad/not-a-list.json"},
		{"bad/pvcs-bad-phase.json", "ocp-dev.storageclasses.json"},
		{"ocp-dev.pvcs.json", "bad/sc-bad-mode.json"},
		{"nope.json", "ocp-dev.storageclasses.json"},
	} {
		if _, err := load(t, c[0], c[1]); err == nil {
			t.Errorf("%v: want an input error", c)
		} else if _, ok := err.(*InputError); !ok {
			t.Errorf("%v: want *InputError, got %T", c, err)
		}
	}
}

func claim(sc any, phase string, ann obj) obj {
	md := obj{"name": "a", "namespace": "n"}
	if ann != nil {
		md["annotations"] = ann
	}
	spec := obj{}
	if sc != nil {
		spec["storageClassName"] = sc
	}
	return obj{"kind": "PersistentVolumeClaim", "metadata": md, "spec": spec, "status": obj{"phase": phase}}
}

func class(name, mode, prov string, def bool) obj {
	md := obj{"name": name}
	if def {
		md["annotations"] = obj{defaultAnn: "true"}
	}
	c := obj{"kind": "StorageClass", "metadata": md, "provisioner": prov}
	if mode != "" {
		c["volumeBindingMode"] = mode
	}
	return c
}

func classes(t *testing.T, p obj, scs ...obj) []string {
	t.Helper()
	res, err := Check([]obj{p}, scs)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range res.Rows {
		out = append(out, r.Class)
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestBindingModeTable(t *testing.T) {
	gp := class("gp3", "", "x.csi", false)
	gpw := class("gp3", wffc, "x.csi", true)
	eq(t, classes(t, claim("gp3", "Pending", nil), gp), "PROVISIONFAILED")                        // missing mode = Immediate
	eq(t, classes(t, claim("gp3", "Pending", nil), gpw), "WAITFORCONSUMER")                       // expected, INFO
	eq(t, classes(t, claim("gp3", "Pending", obj{selectedAnn: "node-01"}), gpw), "SELECTEDSTUCK") // node chosen, still Pending
	eq(t, classes(t, claim(nil, "Pending", nil), gpw), "WAITFORCONSUMER")                         // default applies to a missing name
	eq(t, classes(t, claim("", "Pending", nil), gpw), "NOPV")                                     // but not to ""
	eq(t, classes(t, claim(nil, "Pending", nil), gp), "NOSC")                                     // no default at all
	eq(t, classes(t, claim("ghost", "Bound", nil), gp))                                           // Bound is never flagged
	eq(t, classes(t, claim("ghost", "Pending", nil), gp), "UNKNOWNSC")
	eq(t, classes(t, claim("gp3", "Lost", nil), gp), "LOST")
}

func TestClassFindings(t *testing.T) {
	res, err := Check(nil, []obj{class("a", "", "x", true), class("b", "", "x", true), class("l", "", noProv, false)})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range res.Rows {
		got = append(got, r.Subject+" "+r.Class)
	}
	eq(t, got, "sc/a MULTIDEFAULT", "sc/b MULTIDEFAULT", "sc/l LOCALIMMEDIATE")
}

func TestInfoKeepsVerdictOK(t *testing.T) {
	res, _ := Check([]obj{claim("gp3", "Pending", nil)}, []obj{class("gp3", wffc, "x", false)})
	if _, v := Render("x", res); v != "OK" {
		t.Fatalf("verdict %s", v)
	}
}
