package modelcatalog

import "sort"

// Suggest returns up to `max` names from the catalog (canonicals + aliases)
// within Levenshtein distance `maxDist` of `raw`, ranked by distance
// ascending then alphabetically. Used to populate the `suggestions` field
// on the unsupported_model error response.
func Suggest(c Catalog, raw string, maxDist, max int) []string {
	snap := c.Snapshot()
	type cand struct {
		name string
		d    int
	}
	var out []cand
	consider := func(candidate string) {
		d := levenshtein(raw, candidate)
		if d <= maxDist {
			out = append(out, cand{name: candidate, d: d})
		}
	}
	for _, m := range snap {
		consider(m.Name)
		for _, a := range m.Aliases {
			consider(a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].d != out[j].d {
			return out[i].d < out[j].d
		}
		return out[i].name < out[j].name
	})
	if len(out) > max {
		out = out[:max]
	}
	names := make([]string, len(out))
	for i, c := range out {
		names[i] = c.name
	}
	return names
}

// levenshtein is a small, allocation-light implementation good enough for
// model-name typos. Two-row DP; O(min(len)) memory.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := 0; j <= len(rb); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}
