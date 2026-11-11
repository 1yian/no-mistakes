package intent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/kunchenguid/no-mistakes/internal/db"
)

type Cache interface {
	Get(key string) (string, bool)
	Put(key, summary, agentName, sessionID string)
}

type dbCache struct {
	db *db.DB
}

func NewDBCache(database *db.DB) Cache {
	if database == nil {
		return memCache{}
	}
	return &dbCache{db: database}
}

func (c *dbCache) Get(key string) (string, bool) {
	entry, err := c.db.GetIntentCache(key)
	if err != nil || entry == nil {
		return "", false
	}
	return entry.Summary, true
}

func (c *dbCache) Put(key, summary, agentName, sessionID string) {
	_ = c.db.PutIntentCache(db.IntentCacheEntry{
		CacheKey:  key,
		Summary:   summary,
		AgentName: agentName,
		SessionID: sessionID,
	})
}

type memCache map[string]string

func NewMemCache() Cache { return memCache{} }

func (c memCache) Get(key string) (string, bool) { v, ok := c[key]; return v, ok }
func (c memCache) Put(key, summary, _, _ string) { c[key] = summary }

func cacheKeyFor(s *Session) string {
	h := sha256.New()
	h.Write([]byte(s.AgentName))
	h.Write([]byte{'|'})
	h.Write([]byte(s.SessionID))
	h.Write([]byte{'|'})
	h.Write([]byte(s.LastMsgKey))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.Itoa(len(s.Messages))))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func summaryCacheKeyFor(s *Session, change SummaryContext) string {
	data, _ := json.Marshal(struct {
		Change     ChangeContext
		Transcript string
	}{change.Change, buildSummaryTranscript(s, change)})
	h := sha256.New()
	h.Write([]byte("change-summary-v2|" + cacheKeyFor(s)))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))[:32]
}
