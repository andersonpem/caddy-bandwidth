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
func parseHumanReadableBandwidth(input string) (int, error) {
	// Trim whitespace and convert to uppercase for easier handling
	input = strings.TrimSpace(input)

	// Simple case: just a number
	if matched, _ := regexp.MatchString(`^\d+$`, input); matched {
		return strconv.Atoi(input)
	}

	// Regular expression to match a number followed by a unit
	// Captures: [1]=number, [2]=unit
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KkMmGg][Bb]?)$`)
	matches := re.FindStringSubmatch(input)

	if matches == nil {
		return 0, fmt.Errorf("invalid bandwidth format: %s", input)
	}

	// Parse the number part
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in bandwidth: %s", matches[1])
	}

	// Parse the unit part
	unit := strings.ToLower(matches[2])
	isBits := strings.HasSuffix(unit, "b") && unit != "kb" && unit != "mb" && unit != "gb"

	// Calculate bytes based on unit
	var bytes float64
	switch unit[0] {
	case 'k':
		bytes = value * 1024
	case 'm':
		bytes = value * 1024 * 1024
	case 'g':
		bytes = value * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit in bandwidth: %s", unit)
	}

	// If the unit is bits, convert to bytes
	if isBits {
		bytes = bytes / 8
	}

	return int(bytes), nil
}
