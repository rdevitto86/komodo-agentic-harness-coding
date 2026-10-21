package agent

import "testing"

// A sign-off is void once any test file differs from the one it was stamped with.
func TestSignoffVoided(t *testing.T) {
	files := map[string][]byte{"a_test.go": []byte("package a"), "b_test.go": []byte("package b")}
	s := Signoff{Tree: "t1", TestsHash: TestsHash(files)}

	if s.Voided(TestsHash(map[string][]byte{"b_test.go": []byte("package b"), "a_test.go": []byte("package a")})) {
		t.Error("same files voided the sign-off")
	}

	files["b_test.go"] = []byte("package b // edited")
	if !s.Voided(TestsHash(files)) {
		t.Error("an edited test file did not void the sign-off")
	}

	delete(files, "b_test.go")
	if !s.Voided(TestsHash(files)) {
		t.Error("a removed test file did not void the sign-off")
	}
}

// The hash covers the path as well as the content, and not the map order.
func TestTestsHashCoversPaths(t *testing.T) {
	a := TestsHash(map[string][]byte{"a_test.go": []byte("x")})
	b := TestsHash(map[string][]byte{"b_test.go": []byte("x")})
	if a == b || a == "" {
		t.Errorf("hashes %q %q", a, b)
	}
	// "ab"+"c" and "a"+"bc" must not collide.
	if TestsHash(map[string][]byte{"ab": []byte("c")}) == TestsHash(map[string][]byte{"a": []byte("bc")}) {
		t.Error("path and content boundaries collide")
	}
}

// Test files are found by the same rule the risk levels use.
func TestIsTestFile(t *testing.T) {
	for p, want := range map[string]bool{
		"internal/a/a_test.go": true, "a/testdata/x.json": true, "web/app.spec.ts": true, "a/test_x.py": true,
		"internal/a/a.go": false, "README.md": false,
	} {
		if got := IsTestFile(p); got != want {
			t.Errorf("IsTestFile(%q) = %v", p, got)
		}
	}
}
