package bandwidth

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parseHumanReadableBandwidth parses human-readable bandwidth specifications
// Examples:
// - "5M" or "5MB" = 5 megabytes per second = 5*1024*1024 bytes
// - "5m" or "5Mb" = 5 megabits per second = 5*1024*1024/8 bytes
// - "5K" or "5KB" = 5 kilobytes per second = 5*1024 bytes
// - "5k" or "5Kb" = 5 kilobits per second = 5*1024/8 bytes
// - "5G" or "5GB" = 5 gigabytes per second = 5*1024*1024*1024 bytes
// - "5g" or "5Gb" = 5 gigabits per second = 5*1024*1024*1024/8 bytes
// - "500" = 500 bytes per second
// - "1.5M" = 1.5 megabytes per second
func parseHumanReadableBandwidth(input string) (int, error) {
	// Trim whitespace
	input = strings.TrimSpace(input)

	// Simple case: just a number (bytes per second)
	if matched, _ := regexp.MatchString(`^\d+$`, input); matched {
		return strconv.Atoi(input)
	}

	// Regular expression to match a number followed by a unit
	// Captures: [1]=number, [2]=unit
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KkMmGgTt][Bb]?)$`)
	matches := re.FindStringSubmatch(input)

	if matches == nil {
		return 0, fmt.Errorf("invalid bandwidth format: %s (expected format like '5M', '10KB', etc.)", input)
	}

	// Parse the number part
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in bandwidth: %s", matches[1])
	}

	// Extract the unit and determine if it's bits or bytes
	unit := strings.ToLower(matches[2])
	unitChar := unit[0]
	isBits := len(unit) > 1 && unit[1] == 'b' && unit != "b"

	// Calculate bytes based on unit prefix (k, m, g, t)
	var multiplier float64
	switch unitChar {
	case 'k':
		multiplier = 1024
	case 'm':
		multiplier = 1024 * 1024
	case 'g':
		multiplier = 1024 * 1024 * 1024
	case 't':
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit prefix in bandwidth: %s", unit)
	}

	bytes := value * multiplier

	// Convert bits to bytes if needed
	if isBits {
		bytes /= 8
	}

	return int(bytes), nil
}
