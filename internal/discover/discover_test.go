package discover

import (
	"reflect"
	"testing"
)

func TestDetectDatasets(t *testing.T) {
	got := DetectDatasets(`We evaluate on ETTh1, etth2 and the Weather dataset; ETTh10 is not one.`)
	want := []string{"ETTh1", "ETTh2", "Weather"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := DetectDatasets("no benchmarks"); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
