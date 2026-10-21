package agent

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// RiskLevel is the risk of one group's change.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedLow   RiskLevel = "med_low"
	RiskMedium   RiskLevel = "medium"
	RiskMedHigh  RiskLevel = "med_high"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// RiskLargeLines is the changed-line count above which a group's level is at least med_high.
const RiskLargeLines = 400

// Rank orders the levels from 0 (low) to 5 (critical); an unknown level ranks as critical.
func (l RiskLevel) Rank() int {
	switch l {
	case RiskLow:
		return 0
	case RiskMedLow:
		return 1
	case RiskMedium:
		return 2
	case RiskMedHigh:
		return 3
	case RiskHigh:
		return 4
	}
	return 5
}

// ChangedFile is one path of the diff with its line counts.
type ChangedFile struct {
	Path    string
	Added   int
	Removed int
}

// RiskInput is everything Classify reads: facts about the diff, never an agent's opinion.
type RiskInput struct {
	Files     []ChangedFile // every changed path with its added and removed line counts
	Verified  bool          // Go checked that every change in an allow-listed file is a comment
	Proven    bool          // a reviewer finding was proven in a previous round
	PublicAPI bool          // Go found a change to a public interface in the diff
}

// RiskDecision is the level and the rule that set it.
type RiskDecision struct {
	Level  RiskLevel `json:"level"`
	Reason string    `json:"reason"` // the rule that set it, shown on the pull request
}

// RiskRoute is what a risk level decides about the checks a group gets.
type RiskRoute struct {
	Reviewer bool       // false at low and med_low; Quality Assurance runs at every level
	Deep     []DeepKind // the deep checks, in the order Go runs them
}

var (
	// dependencyFiles are base names (lower case) of dependency manifests and lockfiles.
	dependencyFiles = []string{
		"go.mod", "go.sum", "go.work", "go.work.sum", "package.json", "package-lock.json", "npm-shrinkwrap.json",
		"yarn.lock", "pnpm-lock.yaml", "cargo.toml", "cargo.lock", "pyproject.toml", "poetry.lock", "uv.lock",
		"pipfile", "pipfile.lock", "gemfile", "gemfile.lock", "composer.json", "composer.lock",
	}
	// ciFiles are base names (lower case) of CI definitions outside a workflows folder.
	ciFiles = []string{"jenkinsfile", ".gitlab-ci.yml", "azure-pipelines.yml"}
	// criticalDirs are folder names (lower case) whose every file is critical.
	criticalDirs = []string{"deploy", "deployment", "deployments", "migration", "migrations", ".circleci"}
	// docExts are extensions (lower case) of documentation files.
	docExts = []string{".md", ".mdx", ".rst", ".txt", ".adoc"}
	// configExts are extensions (lower case) of configuration files.
	configExts = []string{".json", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf"}
	// testDirs are folder names (lower case) that hold only test material.
	testDirs = []string{"testdata", "tests", "__tests__"}
)

// Classify sets the risk level from the diff at landing.
func Classify(in RiskInput) RiskDecision {
	if len(in.Files) == 0 {
		return RiskDecision{Level: RiskMedium, Reason: "no changed file to measure, so medium (fail closed)"}
	}
	top := RiskDecision{Level: RiskLow}
	topRank := -1
	lines := 0
	for _, f := range in.Files {
		lines += f.Added + f.Removed
		l, rule := fileRisk(f.Path, in.Verified)
		if l.Rank() > topRank {
			topRank = l.Rank()
			top = RiskDecision{Level: l, Reason: fmt.Sprintf("%s: %s", f.Path, rule)}
		}
	}
	switch {
	case top.Level.Rank() >= RiskMedHigh.Rank():
		return top
	case lines > RiskLargeLines:
		return raised(top, fmt.Sprintf("%d changed lines is over %d", lines, RiskLargeLines))
	case in.Proven:
		return raised(top, "a reviewer finding was proven")
	case in.PublicAPI:
		return raised(top, "a public interface changed")
	}
	return top
}

// Route returns the reviewer and deep-check route of a level; an unknown level gets the fullest route.
func Route(l RiskLevel) RiskRoute {
	return RiskRoute{
		Reviewer: l.Rank() >= RiskMedium.Rank(),
		Deep:     DeepPlan(l),
	}
}

// raised lifts a decision to med_high and keeps the rule that set the lower level in the reason.
func raised(d RiskDecision, why string) RiskDecision {
	return RiskDecision{Level: RiskMedHigh, Reason: fmt.Sprintf("%s; %s, so med_high", d.Reason, why)}
}

// fileRisk returns the level of one path and the rule that set it; a path no rule matches is medium.
func fileRisk(p string, verified bool) (RiskLevel, string) {
	p = strings.ToLower(strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, "\\", "/")), "./"))
	dir, base := path.Split(p)
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	ext := path.Ext(base)
	switch {
	case strings.HasPrefix(p, ".github/workflows/") || strings.Contains(p, "/.github/workflows/") ||
		slices.Contains(ciFiles, base):
		return RiskCritical, "CI workflow file"
	case base == "makefile" || base == "gnumakefile" || ext == ".mk":
		return RiskCritical, "Makefile"
	case base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") || ext == ".dockerfile":
		return RiskCritical, "Dockerfile"
	}
	for _, s := range segs {
		if slices.Contains(criticalDirs, s) {
			return RiskCritical, s + " folder"
		}
	}
	if slices.Contains(dependencyFiles, base) || (strings.HasPrefix(base, "requirements") && ext == ".txt") {
		return RiskHigh, "dependency file"
	}
	switch {
	case slices.Contains(docExts, ext) || base == "license" || base == "notice":
		return RiskLow, "documentation file"
	case isTestPath(segs, base, ext):
		return RiskMedLow, "test-only file"
	case slices.Contains(configExts, ext) || strings.HasPrefix(base, ".golangci") || base == ".editorconfig":
		return RiskMedLow, "config file outside CI and build"
	case verified:
		return RiskLow, "code with comment-only changes"
	}
	return RiskMedium, "code or a path no rule matches"
}

// isTestPath reports whether a path is test material: a test folder or a test file name of Go, Python or JavaScript.
func isTestPath(segs []string, base, ext string) bool {
	for _, s := range segs {
		if slices.Contains(testDirs, s) {
			return true
		}
	}
	name := strings.TrimSuffix(base, ext)
	switch ext {
	case ".go":
		return strings.HasSuffix(name, "_test")
	case ".py":
		return strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test")
	}
	return strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".spec")
}
