package store

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"time"
)

// memoryFormatVersion is stamped into every MemoryData.Version on save and
// kept stable across deer-flow / eino-cli so writers from either side stay
// interoperable.
const memoryFormatVersion = "1.0"

// MemoryData is the on-disk shape; JSON tags are 1:1 with deer-flow so the
// same file can be read by both Go and Python tooling.
type MemoryData struct {
	Version     string      `json:"version"`
	LastUpdated string      `json:"lastUpdated"`
	User        UserContext `json:"user"`
	History     History     `json:"history"`
	Facts       []Fact      `json:"facts"`
}

// Section is a free-form summary of one memory dimension; UpdatedAt is empty
// until the first LLM-driven update overwrites the section.
type Section struct {
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type UserContext struct {
	WorkContext     Section `json:"workContext"`
	PersonalContext Section `json:"personalContext"`
	TopOfMind       Section `json:"topOfMind"`
}

type History struct {
	RecentMonths       Section `json:"recentMonths"`
	EarlierContext     Section `json:"earlierContext"`
	LongTermBackground Section `json:"longTermBackground"`
}

// Fact is a single discrete memory item; ID is stable across rewrites so the
// updater can target it via factsToRemove. Kind / ExpiresAt distinguish
// long-term ("enduring") from one-shot ("episodic") facts; legacy data
// without these fields is treated as enduring (zero-value friendly).
type Fact struct {
	ID          string  `json:"id"`
	Content     string  `json:"content"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	Kind        string  `json:"kind,omitempty"`
	ExpiresAt   string  `json:"expiresAt,omitempty"`
	SourceError string  `json:"sourceError,omitempty"`
	CreatedAt   string  `json:"createdAt,omitempty"`
	Source      string  `json:"source,omitempty"`
}

// FactKind* enumerate the lifecycle classes; bare strings (rather than a
// typed enum) keep JSON omitempty trivial and tolerate older payloads.
const (
	FactKindEnduring = "enduring"
	FactKindEpisodic = "episodic"
)

// IsExpired reports whether an episodic fact's ExpiresAt is past nowISO.
// Enduring or empty-Kind facts always return false. ISO-8601 strings sort
// in time order so plain string comparison is sufficient.
func (f Fact) IsExpired(nowISO string) bool {
	if f.Kind != FactKindEpisodic || f.ExpiresAt == "" {
		return false
	}
	return f.ExpiresAt < nowISO
}

// GetEmptyMemoryData mirrors deer-flow create_empty_memory(): a freshly stamped
// MemoryData with Version locked and LastUpdated set to now.
func GetEmptyMemoryData() MemoryData {
	return MemoryData{
		Version:     memoryFormatVersion,
		LastUpdated: utcNowISO(),
	}
}

// CoerceConfidence clamps any float (incl. NaN/±Inf) into the [0,1] range the
// renderer and updater both rely on.
func CoerceConfidence(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// NewFactID returns "fact_" + 8 hex chars from crypto/rand. crypto/rand over
// math/rand to avoid the shared-source race when multiple goroutines create
// facts in parallel.
func NewFactID() string {
	var buf [4]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		return "fact_" + utcNowISO()
	}
	return "fact_" + hex.EncodeToString(buf[:])
}

func utcNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}
