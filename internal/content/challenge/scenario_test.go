package challenge

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeTagsCanonicalizesAndSorts(t *testing.T) {
	got, err := NormalizeTags([]string{" K8S ", "中文", "linux", "k8s", "中文"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"k8s", "linux", "中文"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized tags = %#v, want %#v", got, want)
	}
}

func TestNormalizeTagsRejectsUnsupportedAndExcessValues(t *testing.T) {
	if _, err := NormalizeTags([]string{"bad_tag"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported tag error = %v", err)
	}
	values := make([]string, MaxScenarioTags+1)
	for i := range values {
		values[i] = "tag" + string(rune('a'+i))
	}
	if _, err := NormalizeTags(values); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("excess tag error = %v", err)
	}
}

func TestNormalizeTagsCountsUniqueCanonicalValues(t *testing.T) {
	values := make([]string, MaxScenarioTags+1)
	for index := range values {
		values[index] = "K8S"
	}
	got, err := NormalizeTags(values)
	if err != nil || !reflect.DeepEqual(got, []string{"k8s"}) {
		t.Fatalf("NormalizeTags = %#v, %v", got, err)
	}
}
