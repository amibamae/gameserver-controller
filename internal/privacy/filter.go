// Package privacy provides log filtering for sensitive data.
package privacy

import (
	"regexp"
	"strings"
)

// FilterLog filters a log entry by removing or redacting sensitive information.
func FilterLog(log []byte) []byte {
	var result strings.Builder

	for _, line := range strings.Split(string(log), "\n") {
		line = redactIP(line)
		line = redactUUID(line)
		line = redactSHAHashes(line)
		line = redactBase64Strings(line)

		result.WriteString(line)
	}

	return []byte(result.String())
}

// redactIP replaces IPv4 and IPv6 addresses with [REDACTED].
func redactIP(line string) string {
	ipRegex := regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b|` +
		`\b(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}\b|` +
		`\b(?:[0-9a-fA-F]{1,4}:){1,7}:\b|` +
		`\b(?:[0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}\b|` +
		`\b(?:[0-9a-fA-F]{1,4}:){1,5}\(([\d.]+)\)\b|` +
		`\b(?:[0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,2}\b|` +
		`\b(?:[0-9a-fA-F]{1,4}:){1,3}\(([\d.]+)\)|` +
		`\b(?:[0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,3}\b|` +
		`\b[0-9a-fA-F]{1,4}(:[0-9a-fA-F]{1,4}){1,4}\b|` +
		`\b([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}\b`)
	return ipRegex.ReplaceAllString(line, "[REDACTED]")
}

// redactUUID replaces UUIDs with the pattern xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx with [REDACTED].
func redactUUID(line string) string {
	uuidRegex := regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	return uuidRegex.ReplaceAllString(line, "[REDACTED]")
}

// redactSHAHashes replaces SHA-256/384 hashes (64-96 hex chars) with [REDACTED].
func redactSHAHashes(line string) string {
	shaRegex := regexp.MustCompile(`\b([a-fA-F0-9]{64,96})\b`)
	return shaRegex.ReplaceAllString(line, "[REDACTED]")
}

// redactBase64Strings replaces base64-encoded strings (minimum 20 chars) with [REDACTED].
func redactBase64Strings(line string) string {
	base64Regex := regexp.MustCompile(`\b([A-Za-z0-9+/]{20,})=\??( |\n|$)`)
	return base64Regex.ReplaceAllString(line, "[REDACTED]")
}
