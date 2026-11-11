package quotarouting

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Snapshot struct {
	SchemaVersion int        `json:"schemaVersion"`
	Providers     []Provider `json:"providers"`
}

type Provider struct {
	Provider       string   `json:"provider"`
	AccountKey     string   `json:"accountKey"`
	AccountKeys    []string `json:"accountKeys,omitempty"`
	AccountLocator *struct {
		DelegateEligible bool `json:"delegateEligible"`
	} `json:"accountLocator,omitempty"`
	QuotaSemantics QuotaSemantics `json:"quotaSemantics"`
}

type QuotaSemantics struct {
	Status                string                  `json:"status"`
	EffectiveAvailability []EffectiveAvailability `json:"effectiveAvailability"`
}

type EffectiveAvailability struct {
	Scope                     string   `json:"scope"`
	Status                    string   `json:"status"`
	EffectivePercentRemaining *float64 `json:"effectivePercentRemaining"`
	Runway                    *Runway  `json:"runway"`
	Selection                 *struct {
		Status        string   `json:"status"`
		SpendPriority *float64 `json:"spendPriority"`
	} `json:"selection"`
}

type Runway struct {
	Status              string   `json:"status"`
	UsableRunwaySeconds *float64 `json:"usableRunwaySeconds"`
}

func ParseSnapshot(data []byte) (*Snapshot, error) {
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("parse quota-axi snapshot: %w", err)
	}
	if snapshot.SchemaVersion != 5 && snapshot.SchemaVersion != 6 {
		return nil, fmt.Errorf("unsupported quota-axi snapshot schemaVersion %d (supported: 5, 6)", snapshot.SchemaVersion)
	}
	if snapshot.Providers == nil {
		return nil, fmt.Errorf("quota-axi snapshot has no providers array")
	}
	seen := map[string]bool{}
	for _, provider := range snapshot.Providers {
		if strings.TrimSpace(provider.Provider) == "" {
			return nil, fmt.Errorf("quota-axi snapshot has a provider row without a provider label")
		}
		if snapshot.SchemaVersion == 6 && strings.TrimSpace(provider.AccountKey) == "" {
			return nil, fmt.Errorf("provider %s has no accountKey in a schema-6 snapshot", provider.Provider)
		}
		keys := provider.AccountKeys
		if provider.AccountKey != "" {
			keys = append([]string{provider.AccountKey}, keys...)
		}
		rowKeys := map[string]bool{}
		for _, account := range keys {
			if rowKeys[account] {
				continue
			}
			rowKeys[account] = true
			key := provider.Provider + "\x00" + account
			if seen[key] {
				return nil, fmt.Errorf("provider %s repeats accountKey %s", provider.Provider, account)
			}
			seen[key] = true
		}
	}
	return &snapshot, nil
}
