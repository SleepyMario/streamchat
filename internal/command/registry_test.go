package command

import (
	"reflect"
	"testing"
)

func TestStreamchatTopLevelPrefixSuggestions(t *testing.T) {
	registry := Streamchat()
	for _, test := range []struct {
		prefix string
		want   []string
	}{
		{prefix: "c", want: []string{"category", "clean", "clear"}},
		{prefix: "cl", want: []string{"clean", "clear"}},
		{prefix: "cle", want: []string{"clean", "clear"}},
		{prefix: "clea", want: []string{"clean", "clear"}},
		{prefix: "b", want: []string{"ban"}},
		{prefix: "o", want: []string{"open"}},
	} {
		if got := registry.Suggest(nil, test.prefix); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("prefix=%q got=%v want=%v", test.prefix, got, test.want)
		}
	}
}

func TestStreamchatHierarchicalSuggestions(t *testing.T) {
	registry := Streamchat()
	platforms := []string{"kick", "youtube", "twitch"}
	for _, name := range []string{"clear", "ban", "timeout", "open"} {
		if got := registry.Suggest([]string{name}, ""); !reflect.DeepEqual(got, platforms) {
			t.Fatalf("command=%q got=%v", name, got)
		}
	}
	if got := registry.Suggest([]string{"clean"}, ""); !reflect.DeepEqual(got, []string{"streamchat", "kick"}) {
		t.Fatalf("clean=%v", got)
	}
	if got := registry.Suggest([]string{"clear"}, "K"); !reflect.DeepEqual(got, []string{"kick"}) {
		t.Fatalf("case-insensitive prefix=%v", got)
	}
	if got := registry.Suggest(nil, "kk"); len(got) != 0 {
		t.Fatalf("retired command returned: %v", got)
	}
}

func TestRegistrySupportsDynamicChildrenWithoutChangingStaticTree(t *testing.T) {
	registry := New(Entry{Name: "ban", Dynamic: func(prefix string) []string {
		return []string{"Alice", "Bob"}
	}})
	if got := registry.Suggest([]string{"ban"}, "a"); !reflect.DeepEqual(got, []string{"alice"}) {
		t.Fatalf("dynamic=%v", got)
	}
}
