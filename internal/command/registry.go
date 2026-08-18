package command

import "strings"

// Source can add context-sensitive candidates to a command node later, such
// as recently observed usernames. Static command grammar does not need one.
type Source func(prefix string) []string

type Entry struct {
	Name     string
	Children []Entry
	Dynamic  Source
}

type Registry struct {
	entries []Entry
}

func New(entries ...Entry) *Registry {
	return &Registry{entries: cloneEntries(entries)}
}

// Suggest returns canonical candidates in registry order for the node at path.
func (r *Registry) Suggest(path []string, prefix string) []string {
	entries, dynamic, ok := r.children(path)
	if !ok {
		return nil
	}
	prefix = strings.ToLower(prefix)
	seen := make(map[string]struct{}, len(entries))
	result := make([]string, 0, len(entries))
	add := func(candidate string) {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" || !strings.HasPrefix(candidate, prefix) {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	for _, entry := range entries {
		add(entry.Name)
	}
	if dynamic != nil {
		for _, candidate := range dynamic(prefix) {
			add(candidate)
		}
	}
	return result
}

func (r *Registry) HasChildren(path []string) bool {
	entries, dynamic, ok := r.children(path)
	return ok && (len(entries) > 0 || dynamic != nil)
}

func (r *Registry) children(path []string) ([]Entry, Source, bool) {
	if r == nil {
		return nil, nil, false
	}
	entries := r.entries
	var dynamic Source
	for _, segment := range path {
		matched := false
		for _, entry := range entries {
			if strings.EqualFold(entry.Name, segment) {
				entries = entry.Children
				dynamic = entry.Dynamic
				matched = true
				break
			}
		}
		if !matched {
			return nil, nil, false
		}
	}
	return entries, dynamic, true
}

func cloneEntries(entries []Entry) []Entry {
	cloned := make([]Entry, len(entries))
	for index, entry := range entries {
		cloned[index] = entry
		cloned[index].Name = strings.ToLower(strings.TrimSpace(entry.Name))
		cloned[index].Children = cloneEntries(entry.Children)
	}
	return cloned
}

// Streamchat returns the complete static slash-command grammar currently used
// by the interactive client. Execution remains with the existing parsers.
func Streamchat() *Registry {
	platforms := func() []Entry {
		return []Entry{{Name: "kick"}, {Name: "youtube"}, {Name: "twitch"}}
	}
	return New(
		Entry{Name: "kick"},
		Entry{Name: "twitch"},
		Entry{Name: "category"},
		Entry{Name: "clean", Children: []Entry{{Name: "streamchat"}, {Name: "kick"}}},
		Entry{Name: "clear", Children: platforms()},
		Entry{Name: "title"},
		Entry{Name: "ban", Children: platforms()},
		Entry{Name: "timeout", Children: platforms()},
		Entry{Name: "open", Children: platforms()},
		Entry{Name: "exit"},
		Entry{Name: "quit"},
	)
}
