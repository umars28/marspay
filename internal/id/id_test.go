package id

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestNewHasPrefixAndLength(t *testing.T) {
	got := New("txn")
	if !strings.HasPrefix(got, "txn_") {
		t.Errorf("New(\"txn\") = %q, want txn_ prefix", got)
	}
	if body := strings.TrimPrefix(got, "txn_"); len(body) != Length {
		t.Errorf("body length = %d, want %d", len(body), Length)
	}
	if Prefix(got) != "txn" {
		t.Errorf("Prefix(%q) = %q, want %q", got, Prefix(got), "txn")
	}
}

func TestULIDUsesCrockfordAlphabetOnly(t *testing.T) {
	for i := 0; i < 500; i++ {
		for _, c := range ULID() {
			if !strings.ContainsRune(alphabet, c) {
				t.Fatalf("character %q is not in the Crockford alphabet", c)
			}
		}
	}
}

func TestULIDIsUnique(t *testing.T) {
	const n = 20_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		v := ULID()
		if _, dup := seen[v]; dup {
			t.Fatalf("duplicate id %q after %d draws", v, i)
		}
		seen[v] = struct{}{}
	}
}

func TestULIDSortsByTime(t *testing.T) {
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, ULID())
		time.Sleep(2 * time.Millisecond)
	}

	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)

	for i := range ids {
		if ids[i] != sorted[i] {
			t.Fatalf("ids are not lexicographically time-ordered:\ngot    %v\nsorted %v", ids, sorted)
		}
	}
}
