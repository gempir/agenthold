package session

import (
	"regexp"
	"slices"
	"strings"
)

// Some harnesses encode the reasoning effort in the model id ("gpt-x/high",
// "provider/model/xhigh") instead of reporting it as a thought_level option.
// None of the pinned harnesses does so by default (OpenCode only when a
// client opts in), but the picker should not list one entry per effort when
// one does: the catalog shows the base model and an effort option, and the
// two are joined again when the model is set on the harness.

// VariantEffortID is the id of the effort option made from model id suffixes.
const VariantEffortID = "model-effort"

var effortSuffixes = map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// variantSplit is a model option whose ids end in effort levels.
type variantSplit struct {
	raw ConfigOption
	// efforts are the levels of each base model, in the harness's order.
	efforts map[string][]string
	// listed: the base id is itself a value of the raw option.
	listed map[string]bool
	// first is the first variant of each base, for its name and description.
	first map[string]ConfigValue
}

func variantParts(value string) (base, effort string, ok bool) {
	i := strings.LastIndex(value, "/")
	if i <= 0 || i == len(value)-1 || !effortSuffixes[value[i+1:]] {
		return "", "", false
	}
	return value[:i], value[i+1:], true
}

// splitVariants reports the effort variants of a raw model option. A base
// model counts when at least two of its variants are listed, so a model that
// merely ends in "/high" is not taken apart.
func splitVariants(mo ConfigOption) (*variantSplit, bool) {
	s := &variantSplit{raw: mo, efforts: map[string][]string{}, listed: map[string]bool{}, first: map[string]ConfigValue{}}
	for _, v := range mo.Values {
		if base, effort, ok := variantParts(v.Value); ok {
			if _, seen := s.first[base]; !seen {
				s.first[base] = v
			}
			s.efforts[base] = append(s.efforts[base], effort)
		}
	}
	for base, e := range s.efforts {
		if len(e) < 2 {
			delete(s.efforts, base)
		}
	}
	if len(s.efforts) == 0 {
		return nil, false
	}
	for _, v := range mo.Values {
		if _, ok := s.efforts[v.Value]; ok {
			s.listed[v.Value] = true
		}
	}
	return s, true
}

var variantNameSuffix = regexp.MustCompile(`(?i)\s*[(/\-]\s*(none|minimal|low|medium|high|x-?high|extra high|max)\s*\)?$`)

// view is the model option as the catalog shows it: base models only.
func (s *variantSplit) view() ConfigOption {
	out := s.raw
	out.Values = nil
	seen := map[string]bool{}
	for _, v := range s.raw.Values {
		base, _, isVariant := variantParts(v.Value)
		if isVariant && s.efforts[base] != nil {
			if seen[base] {
				continue
			}
			seen[base] = true
			if !s.listed[base] {
				b := s.first[base]
				b.Value = base
				b.Name = strings.TrimSpace(variantNameSuffix.ReplaceAllString(b.Name, ""))
				out.Values = append(out.Values, b)
			}
			continue
		}
		if s.efforts[v.Value] != nil {
			if seen[v.Value] {
				continue
			}
			seen[v.Value] = true
		}
		out.Values = append(out.Values, v)
	}
	if base, _, ok := variantParts(s.raw.Current); ok && s.efforts[base] != nil {
		out.Current = base
	}
	return out
}

// effortOption is the effort option of a base model.
func (s *variantSplit) effortOption(base string) ConfigOption {
	o := ConfigOption{ID: VariantEffortID, Name: "Reasoning effort", Category: "thought_level", Kind: KindEffort, Type: "select"}
	if b, e, ok := variantParts(s.raw.Current); ok && b == base {
		o.Current = e
	}
	for _, e := range s.efforts[base] {
		o.Values = append(o.Values, ConfigValue{Value: e, Name: effortName(e)})
	}
	return o
}

// compose is the raw model value for a base model and a chosen effort. ok is
// false when the effort is not one of the model's levels. Without an effort
// the value is the base itself when the harness lists it (its default
// effort), else the variant the session is on, else the first variant.
func (s *variantSplit) compose(base, effort, current string) (value string, ok bool) {
	levels := s.efforts[base]
	if levels == nil {
		return base, true
	}
	if slices.Contains(levels, effort) {
		return base + "/" + effort, true
	}
	ok = effort == ""
	if s.listed[base] {
		return base, ok
	}
	if b, _, isVariant := variantParts(current); isVariant && b == base {
		return current, ok
	}
	return base + "/" + levels[0], ok
}

func effortName(e string) string {
	switch e {
	case "xhigh":
		return "Extra High"
	case "":
		return e
	}
	return strings.ToUpper(e[:1]) + e[1:]
}
