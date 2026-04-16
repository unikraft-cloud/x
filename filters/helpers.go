// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package filters

// Keys extracts all field paths referenced by a filter.
// This is useful for determining which fields need to be resolved
// before the filter can be evaluated.
func Keys(filter Filter) [][]string {
	var keys [][]string
	collectKeys(filter, &keys)
	return keys
}

func collectKeys(filter Filter, keys *[][]string) {
	switch f := filter.(type) {
	case All:
		for _, sub := range f {
			collectKeys(sub, keys)
		}
	case Any:
		for _, sub := range f {
			collectKeys(sub, keys)
		}
	default:
		// For leaf filters, we need to extract the field path.
		// The filter will call the adaptor with the field path when matching.
		// We use a fake adaptor to capture the field path.
		_, _ = filter.Match(AdapterFunc(func(fieldpath []string) (string, []string, bool) {
			// Make a copy of the fieldpath to avoid aliasing issues
			pathCopy := make([]string, len(fieldpath))
			copy(pathCopy, fieldpath)
			*keys = append(*keys, pathCopy)
			return "", nil, false
		}))
	}
}

// Restrict evaluates a filter against a set of candidate values for a specific field,
// returning only the values that would match. This is useful for pre-filtering
// before making API calls (e.g., filtering metros before querying each one).
//
// If the filter contains expressions that don't reference the specified key,
// all candidates are returned (since those expressions can't be evaluated here).
//
// For example:
//   - filter "metro==fra", key "metro", candidates ["fra","sfo"] → ["fra"]
//   - filter "metro!=fra", key "metro", candidates ["fra","sfo"] → ["sfo"]
//   - filter "name==foo", key "metro", candidates ["fra","sfo"] → ["fra","sfo"]
func Restrict(filter Filter, key string, candidates []string) []string {
	if filter == nil {
		return candidates
	}
	matched := restrictCollect(filter, key, candidates)
	if matched == nil {
		// Filter doesn't reference this key at all
		return candidates
	}
	// Preserve original order
	result := make([]string, 0, len(matched))
	for _, c := range candidates {
		if matched[c] {
			result = append(result, c)
		}
	}
	return result
}

// restrictCollect returns a map of candidate values that match, or nil if the
// filter doesn't reference the key at all.
func restrictCollect(filter Filter, key string, candidates []string) map[string]bool {
	switch f := filter.(type) {
	case All:
		// AND: intersect results, but if any sub-filter doesn't reference key, ignore it
		var result map[string]bool
		for _, sub := range f {
			subResult := restrictCollect(sub, key, candidates)
			if subResult == nil {
				continue // sub-filter doesn't reference key
			}
			if result == nil {
				result = subResult
			} else {
				// Intersect
				for k := range result {
					if !subResult[k] {
						delete(result, k)
					}
				}
			}
		}
		return result
	case Any:
		// OR: if ANY sub-filter doesn't reference key, we must return all candidates
		var result map[string]bool
		for _, sub := range f {
			subResult := restrictCollect(sub, key, candidates)
			if subResult == nil {
				// This sub-filter doesn't reference key, so we can't filter
				return nil
			}
			if result == nil {
				result = subResult
			} else {
				// Union
				for k, v := range subResult {
					if v {
						result[k] = true
					}
				}
			}
		}
		return result
	default:
		// Leaf filter: evaluate against each candidate
		result := make(map[string]bool)
		referencesKey := false
		for _, candidate := range candidates {
			var found bool
			matched, _ := filter.Match(AdapterFunc(func(fieldpath []string) (string, []string, bool) {
				if len(fieldpath) == 1 && fieldpath[0] == key {
					found = true
					return candidate, nil, true
				}
				return "", nil, false
			}))
			if found {
				referencesKey = true
				result[candidate] = matched
			}
		}
		if !referencesKey {
			return nil
		}
		return result
	}
}
