package agent

import (
	"slices"
	"strings"
	"testing"
)

// files builds a diff of paths with one added line each.
func files(paths ...string) []ChangedFile {
	out := make([]ChangedFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, ChangedFile{Path: p, Added: 1})
	}
	return out
}

func TestClassifyLevelTable(t *testing.T) {
	tests := []struct {
		name     string
		in       RiskInput
		want     RiskLevel
		reasonIn string
	}{
		{"markdown doc", RiskInput{Files: files("README.md")}, RiskLow, "README.md"},
		{"docs folder", RiskInput{Files: files("docs/guide.rst")}, RiskLow, "documentation"},
		{"comment-only code verified", RiskInput{Files: files("internal/a/a.go"), Verified: true}, RiskLow, "comment"},
		{"code not verified as comment", RiskInput{Files: files("internal/a/a.go")}, RiskMedium, "a.go"},
		{"go test file", RiskInput{Files: files("internal/a/a_test.go")}, RiskMedLow, "test"},
		{"testdata", RiskInput{Files: files("internal/a/testdata/x.json")}, RiskMedLow, "test"},
		{"python test", RiskInput{Files: files("pkg/test_a.py")}, RiskMedLow, "test"},
		{"js spec", RiskInput{Files: files("web/a.spec.ts")}, RiskMedLow, "test"},
		{"yaml config", RiskInput{Files: files("config/app.yaml")}, RiskMedLow, "config"},
		{"golangci config", RiskInput{Files: files(".golangci.yml")}, RiskMedLow, "config"},
		{"code", RiskInput{Files: files("internal/a/a.go")}, RiskMedium, "code"},
		{"go.mod", RiskInput{Files: files("go.mod")}, RiskHigh, "dependency"},
		{"go.sum nested", RiskInput{Files: files("tools/go.sum")}, RiskHigh, "dependency"},
		{"package.json", RiskInput{Files: files("web/package.json")}, RiskHigh, "dependency"},
		{"Cargo.lock", RiskInput{Files: files("Cargo.lock")}, RiskHigh, "dependency"},
		{"requirements", RiskInput{Files: files("requirements-dev.txt")}, RiskHigh, "dependency"},
		{"workflow", RiskInput{Files: files(".github/workflows/ci.yml")}, RiskCritical, "workflow"},
		{"makefile", RiskInput{Files: files("Makefile")}, RiskCritical, "Makefile"},
		{"nested makefile", RiskInput{Files: files("sub/makefile")}, RiskCritical, "Makefile"},
		{"mk include", RiskInput{Files: files("build/rules.mk")}, RiskCritical, "Makefile"},
		{"dockerfile", RiskInput{Files: files("Dockerfile")}, RiskCritical, "Dockerfile"},
		{"dockerfile suffix", RiskInput{Files: files("svc/Dockerfile.dev")}, RiskCritical, "Dockerfile"},
		{"deploy folder", RiskInput{Files: files("deploy/cdk/app.ts")}, RiskCritical, "deploy"},
		{"migration folder", RiskInput{Files: files("db/migrations/001.sql")}, RiskCritical, "migration"},
		{"unmatched path fails closed", RiskInput{Files: files("weird.xyz")}, RiskMedium, "weird.xyz"},
		{"no files fails closed", RiskInput{}, RiskMedium, "no changed file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in)
			if got.Level != tc.want {
				t.Fatalf("level = %q, want %q (reason %q)", got.Level, tc.want, got.Reason)
			}
			if !strings.Contains(got.Reason, tc.reasonIn) {
				t.Fatalf("reason %q lacks %q", got.Reason, tc.reasonIn)
			}
		})
	}
}

func TestClassifyHighestFileWins(t *testing.T) {
	in := RiskInput{Files: files("README.md", "internal/a/a_test.go", "internal/a/a.go", "go.mod")}
	if got := Classify(in); got.Level != RiskHigh || !strings.Contains(got.Reason, "go.mod") {
		t.Fatalf("got %+v, want high naming go.mod", got)
	}
	in.Files = append(in.Files, ChangedFile{Path: "Makefile", Added: 1})
	if got := Classify(in); got.Level != RiskCritical {
		t.Fatalf("got %+v, want critical", got)
	}
}

func TestClassifyVerifiedLowersNothingElse(t *testing.T) {
	in := RiskInput{Files: files("README.md", "internal/a/a.go", "go.mod"), Verified: true}
	if got := Classify(in); got.Level != RiskHigh {
		t.Fatalf("Verified lowered a dependency diff to %q", got.Level)
	}
}

func TestClassifyRaises(t *testing.T) {
	big := []ChangedFile{{Path: "internal/a/a.go", Added: 300, Removed: 101}}
	edge := []ChangedFile{{Path: "internal/a/a.go", Added: 300, Removed: 100}}
	tests := []struct {
		name string
		in   RiskInput
		want RiskLevel
	}{
		{"401 lines", RiskInput{Files: big}, RiskMedHigh},
		{"400 lines stays", RiskInput{Files: edge}, RiskMedium},
		{"401 lines across files", RiskInput{Files: []ChangedFile{
			{Path: "a.go", Added: 200}, {Path: "b.go", Removed: 201},
		}}, RiskMedHigh},
		{"large docs diff", RiskInput{Files: []ChangedFile{{Path: "docs/a.md", Added: 900}}}, RiskMedHigh},
		{"proven finding", RiskInput{Files: files("internal/a/a.go"), Proven: true}, RiskMedHigh},
		{"proven on low", RiskInput{Files: files("README.md"), Proven: true}, RiskMedHigh},
		{"public interface", RiskInput{Files: files("internal/a/a.go"), PublicAPI: true}, RiskMedHigh},
		{"never lowers high", RiskInput{Files: files("go.mod"), Proven: true, PublicAPI: true}, RiskHigh},
		{"never lowers critical", RiskInput{Files: []ChangedFile{{Path: "Makefile", Added: 999}}}, RiskCritical},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in)
			if got.Level != tc.want {
				t.Fatalf("level = %q, want %q (reason %q)", got.Level, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Fatal("empty reason")
			}
		})
	}
}

func TestClassifyAlwaysGivesReason(t *testing.T) {
	for _, p := range []string{"a.md", "a_test.go", "a.go", "go.mod", "Makefile", "x.unknown"} {
		if Classify(RiskInput{Files: files(p)}).Reason == "" {
			t.Fatalf("path %q: empty reason", p)
		}
	}
}

func TestRankOrderAndFailClosed(t *testing.T) {
	order := []RiskLevel{RiskLow, RiskMedLow, RiskMedium, RiskMedHigh, RiskHigh, RiskCritical}
	for i, l := range order {
		if l.Rank() != i {
			t.Fatalf("%s rank = %d, want %d", l, l.Rank(), i)
		}
	}
	if got := RiskLevel("bogus").Rank(); got != RiskCritical.Rank() {
		t.Fatalf("unknown level rank = %d, want the highest", got)
	}
}

func TestDeepPlan(t *testing.T) {
	race := []DeepKind{DeepRace}
	all := []DeepKind{DeepRace, DeepFuzz, DeepMutation, DeepBench}
	tests := []struct {
		l    RiskLevel
		want []DeepKind
	}{
		{RiskLow, race}, {RiskMedLow, race}, {RiskMedium, race},
		{RiskMedHigh, all}, {RiskHigh, all}, {RiskCritical, all},
		{RiskLevel("bogus"), all},
	}
	for _, tc := range tests {
		if got := DeepPlan(tc.l); !slices.Equal(got, tc.want) {
			t.Fatalf("DeepPlan(%q) = %v, want %v", tc.l, got, tc.want)
		}
	}
}

func TestRoute(t *testing.T) {
	tests := []struct {
		l        RiskLevel
		reviewer bool
	}{
		{RiskLow, false}, {RiskMedLow, false}, {RiskMedium, true}, {RiskMedHigh, true},
		{RiskHigh, true}, {RiskCritical, true}, {RiskLevel("bogus"), true},
	}
	for _, tc := range tests {
		r := Route(tc.l)
		if r.Reviewer != tc.reviewer {
			t.Fatalf("Route(%q).Reviewer = %v, want %v", tc.l, r.Reviewer, tc.reviewer)
		}
		if !slices.Equal(r.Deep, DeepPlan(tc.l)) {
			t.Fatalf("Route(%q).Deep = %v, want the DeepPlan", tc.l, r.Deep)
		}
	}
}
