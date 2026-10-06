package money

import (
	"math"
	"testing"
)

func TestParseExactAndRange(t *testing.T) {
	valid := map[string]int64{"0.00": 0, "00.01": 1, "27600.00": 2760000, "4977.05": 497705, "92233720368547758.07": math.MaxInt64}
	for value, want := range valid {
		got, err := Parse(value)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
	for _, value := range []string{"", "1", "1.1", "1.001", "1,00", "-1.00", "+1.00", "1e3", " 1.00", "1.00 ", "92233720368547758.08", "NaN", "١.00"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestApprovedCostFormats(t *testing.T) {
	if got := Cost(2760000, 497705, "included", "22"); got != "27 600 руб. 00 коп., в т. ч. НДС 22% — 4 977 руб. 05 коп." {
		t.Fatal(got)
	}
	if got := Cost(240900, 0, "none", ""); got != "2 409 руб. 00 коп., без НДС" {
		t.Fatal(got)
	}
	if got := Cost(100, 0, "included", "0"); got != "1 руб. 00 коп., в т. ч. НДС 0% — 0 руб. 00 коп." {
		t.Fatal(got)
	}
	if got := Format(math.MaxInt64); got != "92 233 720 368 547 758 руб. 07 коп." {
		t.Fatal(got)
	}
}

func TestRateAndSum(t *testing.T) {
	for input, want := range map[string]string{"22": "22", "0": "0", "22.00": "22", "7.50": "7.5", "999.99": "999.99"} {
		got, err := ParseRate(input)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", input, got, err)
		}
	}
	for _, value := range []string{"", "1000", "22.001", "-1", "22.", "1e2"} {
		if _, err := ParseRate(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if _, err := Add(math.MaxInt64, 1); err == nil {
		t.Fatal("overflow accepted")
	}
	if got, err := Add(1, 99); err != nil || got != 100 {
		t.Fatal(got, err)
	}
}
