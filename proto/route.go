// Package proto defines protocol-agnostic MQTT topic routing types. Product-specific
// layouts implement TopicParser in their own modules.
package proto

// Route is a parsed MQTT topic identity. Interpretation of Domain/Channel is protocol-specific.
type Route struct {
	Domain  string // e.g. edge | cloud
	Version string // e.g. v1
	PKey    string
	SN      string
	Channel string // e.g. dp, tm, set, call — protocol-defined
}

// TopicParser parses broker topics into Route. Implement for each on-wire convention.
type TopicParser interface {
	Parse(topic string) (Route, error)
}
