package filters

import (
	"fmt"
	"strconv"
	"strings"
)

func compare(lhs, rhs string, op operator) (bool, error) {

	if li, err := strconv.ParseInt(lhs, 10, 64); err == nil {
		if ri, err := strconv.ParseInt(rhs, 10, 64); err == nil {
			return compareInt(li, ri, op), nil
		}
	}

	if lf, err := strconv.ParseFloat(lhs, 64); err == nil {
		if rf, err := strconv.ParseFloat(rhs, 64); err == nil {
			return compareFloat(lf, rf, op), nil
		}
	}

	if ls, err := parseSize(lhs); err == nil {
		if rs, err := parseSize(rhs); err == nil {
			return compareFloat(ls, rs, op), nil
		}
	}

	return false, fmt.Errorf("cannot compare %q and %q", lhs, rhs)
}

func parseSize(s string) (float64, error) {
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}

	number := s[:i]
	unit := strings.TrimSpace(s[i:])

	if number == "" {
		return 0, fmt.Errorf("not a size: %q", s)
	}

	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("not a size: %q", s)
	}

	multiplier, err := sizeMultiplier(unit)
	if err != nil {
		return 0, err
	}

	return value * multiplier, nil
}

func sizeMultiplier(unit string) (float64, error) {
	switch strings.ToLower(unit) {
	case "b":
		return 1, nil
	case "kb":
		return 1000, nil
	case "mb":
		return 1000 * 1000, nil
	case "gb":
		return 1000 * 1000 * 1000, nil
	case "tb":
		return 1000 * 1000 * 1000 * 1000, nil
	case "kib":
		return 1024, nil
	case "mib":
		return 1024 * 1024, nil
	case "gib":
		return 1024 * 1024 * 1024, nil
	case "tib":
		return 1024 * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unrecognized size unit %q", unit)
	}
}

func compareInt(lhs, rhs int64, op operator) bool {
	switch op {
	case operatorGreater:
		return lhs > rhs
	case operatorGreaterEqual:
		return lhs >= rhs
	case operatorLess:
		return lhs < rhs
	case operatorLessEqual:
		return lhs <= rhs
	default:
		return false
	}
}

func compareFloat(lhs, rhs float64, op operator) bool {
	switch op {
	case operatorGreater:
		return lhs > rhs
	case operatorGreaterEqual:
		return lhs >= rhs
	case operatorLess:
		return lhs < rhs
	case operatorLessEqual:
		return lhs <= rhs
	default:
		return false
	}
}
