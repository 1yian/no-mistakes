package quotarouting

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type Record struct {
	Version    int        `json:"version"`
	Enabled    bool       `json:"enabled"`
	Configured []string   `json:"configured"`
	Order      []string   `json:"order"`
	Choice     string     `json:"choice"`
	Reason     string     `json:"reason"`
	Candidates []Evidence `json:"candidates,omitempty"`
	Switches   []Switch   `json:"switches,omitempty"`
}

type Switch struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
}

const RecordVersion = 1

func NewRecord(enabled bool, configured, order []types.AgentName, selection Selection) *Record {
	record := &Record{
		Version:    RecordVersion,
		Enabled:    enabled,
		Configured: agentNamesText(configured),
		Order:      agentNamesText(order),
		Choice:     string(selection.Choice),
		Reason:     selection.Reason,
		Candidates: selection.Evidence,
	}
	return record
}

func NewFailureRecord(enabled bool, configured []types.AgentName, reason string) *Record {
	return &Record{
		Version:    RecordVersion,
		Enabled:    enabled,
		Configured: agentNamesText(configured),
		Order:      agentNamesText(configured),
		Choice:     firstAgentName(configured),
		Reason:     reason,
	}
}

func (r *Record) AppendSwitch(from, to types.AgentName, reason string, at time.Time) *Record {
	next := *r
	next.Switches = append(append([]Switch{}, r.Switches...), Switch{
		From:   string(from),
		To:     string(to),
		Reason: reason,
		At:     at.Unix(),
	})
	return &next
}

func ParseRecord(payload string) (*Record, error) {
	if strings.TrimSpace(payload) == "" {
		return nil, nil
	}
	var record Record
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return nil, fmt.Errorf("parse quota routing record: %w", err)
	}
	return &record, nil
}

func (r *Record) OrderedAgents(resolved []types.AgentName) []types.AgentName {
	position := make(map[types.AgentName]int, len(r.Order))
	for i, name := range r.Order {
		position[types.CanonicalAgentName(types.AgentName(name))] = i
	}
	// Agents the record does not name (configured after the run started, or
	// never a candidate because no provider maps them) sort after every
	// named one in their resolved relative order - never beside the head,
	// which a plain zero default would give them.
	unranked := len(r.Order)
	rank := func(name types.AgentName) int {
		if pos, ok := position[types.CanonicalAgentName(name)]; ok {
			return pos
		}
		return unranked
	}
	out := append([]types.AgentName{}, resolved...)
	sort.SliceStable(out, func(a, b int) bool {
		return rank(out[a]) < rank(out[b])
	})
	return out
}

func (r *Record) CurrentAgent() string {
	if len(r.Switches) > 0 {
		return r.Switches[len(r.Switches)-1].To
	}
	return r.Choice
}

func agentNamesText(names []types.AgentName) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, string(name))
	}
	return out
}

func firstAgentName(names []types.AgentName) string {
	if len(names) == 0 {
		return ""
	}
	return string(names[0])
}
