// Package raw defines embedder-free caller-supplied vector contracts.
package raw

import (
	"encoding/json"
	"time"
)

const (
	MaxNameBytes          = 128
	MaxSpaceIDBytes       = 256
	MaxIDBytes            = 256
	MaxMetadataKeys       = 32
	MaxMetadataKeyBytes   = 64
	MaxMetadataValueBytes = 1024
	DefaultListLimit      = 50
	MaxListLimit          = 100
	DefaultTopK           = 10
	MaxTopK               = 100
	MaxQueryTextBytes     = 4096
	MaxBodyBytes          = 8 << 20
	MaxDimensions         = 2000
	MaxBatch              = 100
	MaxTextBytes          = 64 << 10
	MaxMetadataBytes      = 16 << 10
	MaxFilterPredicates   = 8
	MaxDeleteMatches      = 1000
	MaxCursorBytes        = 2048
	SchemaVersion         = 1
)

type LexicalConfig struct {
	Enabled    bool   `json:"enabled"`
	TextConfig string `json:"text_config"`
}

// Nil fields mean omitted, not an explicit false or empty configuration.
type LexicalRequest struct {
	Enabled    *bool   `json:"enabled,omitempty"`
	TextConfig *string `json:"text_config,omitempty"`
}
type CreateCollectionRequest struct {
	Name             string          `json:"name"`
	Dimensions       int             `json:"dimensions"`
	Metric           string          `json:"metric"`
	EmbeddingSpaceID string          `json:"embedding_space_id"`
	Lexical          *LexicalRequest `json:"lexical,omitempty"`
}
type Collection struct {
	CollectionID     string        `json:"collection_id"`
	Name             string        `json:"name"`
	Kind             string        `json:"kind"`
	Backend          string        `json:"backend"`
	Dimensions       int           `json:"dimensions"`
	Metric           string        `json:"metric"`
	EmbeddingSpaceID string        `json:"embedding_space_id"`
	Lexical          LexicalConfig `json:"lexical"`
	SchemaVersion    int           `json:"schema_version"`
	CreatedAt        time.Time     `json:"created_at"`
}

// Text omission/null both clear text on replacement; metadata omission becomes {}.
type Record struct {
	ID       string            `json:"id"`
	Vector   []float64         `json:"vector"`
	Text     *string           `json:"text,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}
type CanonicalRecord struct {
	ID       string            `json:"id"`
	Vector   []float32         `json:"vector,omitempty"`
	Text     *string           `json:"text,omitempty"`
	Metadata map[string]string `json:"metadata"`
}
type Filter struct {
	MetadataEq map[string]string `json:"metadata_eq"`
}
type UpsertRequest struct {
	EmbeddingSpaceID string   `json:"embedding_space_id"`
	Records          []Record `json:"records"`
}
type UpsertResponse struct {
	IDs []string `json:"ids"`
}
type GetRecordsRequest struct {
	IDs            []string `json:"ids"`
	IncludeVectors bool     `json:"include_vectors"`
}
type GetRecordsResponse struct {
	Records    []CanonicalRecord `json:"records"`
	MissingIDs []string          `json:"missing_ids"`
}
type ListCollectionsRequest struct {
	Limit  *int    `json:"limit,omitempty"`
	Cursor *string `json:"cursor,omitempty"`
}
type CollectionPage struct {
	Collections []Collection `json:"collections"`
	NextCursor  *string      `json:"next_cursor"`
}
type ListRecordsRequest struct {
	Filter         *Filter `json:"filter,omitempty"`
	Limit          *int    `json:"limit,omitempty"`
	Cursor         *string `json:"cursor,omitempty"`
	IncludeVectors bool    `json:"include_vectors"`
}
type RecordPage struct {
	Records    []CanonicalRecord `json:"records"`
	NextCursor *string           `json:"next_cursor"`
}

// A nil IDs slice means absent. An explicitly empty slice is an invalid selector.
type DeleteRecordsRequest struct {
	IDs    []string `json:"ids,omitempty"`
	Filter *Filter  `json:"filter,omitempty"`
}
type DeleteRecordsResponse struct {
	DeletedCount int `json:"deleted_count"`
}
type ResetRequest struct{}
type ResetResponse struct {
	Reset bool `json:"reset"`
}
type DeleteCollectionResponse struct {
	Deleted bool `json:"deleted"`
}

// Optional scalar pointers distinguish absence from explicit zero/empty values.
// DecodeRequest rejects null except where the contract permits it (text on
// records and cursors). Non-nil empty Vector is present for exclusivity checks.
type SearchRequest struct {
	Mode             string    `json:"mode"`
	Accuracy         *string   `json:"accuracy,omitempty"`
	EmbeddingSpaceID *string   `json:"embedding_space_id,omitempty"`
	Vector           []float64 `json:"vector,omitempty"`
	Text             *string   `json:"text,omitempty"`
	TopK             *int      `json:"top_k,omitempty"`
	Filter           *Filter   `json:"filter,omitempty"`
}
type SearchResult struct {
	ID               string            `json:"id"`
	Text             *string           `json:"text,omitempty"`
	Metadata         map[string]string `json:"metadata"`
	Score            float64           `json:"score"`
	VectorSimilarity *float64          `json:"vector_similarity,omitempty"`
	VectorRank       *int              `json:"vector_rank,omitempty"`
	LexicalRank      *int              `json:"lexical_rank,omitempty"`
}
type FusionParameters struct {
	K                int     `json:"k"`
	VectorWeight     float64 `json:"vector_weight"`
	LexicalWeight    float64 `json:"lexical_weight"`
	CandidatesPerArm int     `json:"candidates_per_arm"`
}
type SearchResponse struct {
	Mode      string            `json:"mode"`
	Accuracy  string            `json:"accuracy"`
	ScoreKind string            `json:"score_kind"`
	Results   []SearchResult    `json:"results"`
	Count     int               `json:"count"`
	Fusion    *FusionParameters `json:"fusion,omitempty"`
}
type Readiness struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}
type IndexCapability struct {
	Strategy  string `json:"strategy"`
	Installed bool   `json:"installed"`
	Readiness
}
type Limits struct {
	BodyBytes          int `json:"body_bytes"`
	Dimensions         int `json:"dimensions"`
	NameBytes          int `json:"name_bytes"`
	SpaceIDBytes       int `json:"space_id_bytes"`
	IDBytes            int `json:"id_bytes"`
	TextBytes          int `json:"text_bytes"`
	MetadataKeys       int `json:"metadata_keys"`
	MetadataKeyBytes   int `json:"metadata_key_bytes"`
	MetadataValueBytes int `json:"metadata_value_bytes"`
	MetadataBytes      int `json:"metadata_bytes"`
	Batch              int `json:"batch"`
	FilterPredicates   int `json:"filter_predicates"`
	DeleteMatches      int `json:"delete_matches"`
	ListDefault        int `json:"list_default"`
	ListMax            int `json:"list_max"`
	CursorBytes        int `json:"cursor_bytes"`
	TopKDefault        int `json:"top_k_default"`
	TopKMax            int `json:"top_k_max"`
	QueryTextBytes     int `json:"query_text_bytes"`
}
type Capabilities struct {
	Backend        string `json:"backend"`
	RawCollections bool   `json:"raw_collections"`
	Readiness
	Modes           []string             `json:"modes"`
	ModeReadiness   map[string]Readiness `json:"mode_readiness"`
	AccuracyOptions []string             `json:"accuracy_options"`
	DefaultAccuracy string               `json:"default_accuracy"`
	Metrics         []string             `json:"metrics"`
	Limits          Limits               `json:"limits"`
	AtomicBatch     bool                 `json:"atomic_batch"`
	Persistent      bool                 `json:"persistent"`
	Indexes         []IndexCapability    `json:"indexes"`
}

func ContractLimits() Limits {
	return Limits{
		BodyBytes: MaxBodyBytes, Dimensions: MaxDimensions, NameBytes: MaxNameBytes,
		SpaceIDBytes: MaxSpaceIDBytes, IDBytes: MaxIDBytes, TextBytes: MaxTextBytes,
		MetadataKeys: MaxMetadataKeys, MetadataKeyBytes: MaxMetadataKeyBytes,
		MetadataValueBytes: MaxMetadataValueBytes, MetadataBytes: MaxMetadataBytes,
		Batch: MaxBatch, FilterPredicates: MaxFilterPredicates, DeleteMatches: MaxDeleteMatches,
		ListDefault: DefaultListLimit, ListMax: MaxListLimit, CursorBytes: MaxCursorBytes,
		TopKDefault: DefaultTopK, TopKMax: MaxTopK, QueryTextBytes: MaxQueryTextBytes,
	}
}

// MarshalJSON preserves the distinction between absent and explicitly empty IDs.
func (r DeleteRecordsRequest) MarshalJSON() ([]byte, error) {
	type plain DeleteRecordsRequest
	var ids *[]string
	if r.IDs != nil {
		ids = &r.IDs
	}
	return json.Marshal(struct {
		plain
		IDs *[]string `json:"ids,omitempty"`
	}{plain: plain(r), IDs: ids})
}

// MarshalJSON preserves vector presence for search-mode exclusivity checks.
func (r SearchRequest) MarshalJSON() ([]byte, error) {
	type plain SearchRequest
	var vector *[]float64
	if r.Vector != nil {
		vector = &r.Vector
	}
	return json.Marshal(struct {
		plain
		Vector *[]float64 `json:"vector,omitempty"`
	}{plain: plain(r), Vector: vector})
}
