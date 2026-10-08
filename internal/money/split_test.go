package money

import "testing"

func TestSplitEvenlyExact(t *testing.T) {
	got := SplitEvenly(FromRupiah(300), 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, m := range got {
		if m != FromRupiah(100) {
			t.Errorf("part %d = %s, want Rp 100", i, m)
		}
	}
}
