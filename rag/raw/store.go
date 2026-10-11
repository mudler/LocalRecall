package raw

import "context"

// Store owns raw persistence only, never embeddings, assets or sources.
// Create's bool reports newly created. Close drains/cancels owned resources.
type Store interface {
	Capabilities(context.Context) (Capabilities, error)
	CreateCollection(context.Context, CreateCollectionRequest) (Collection, bool, error)
	ListCollections(context.Context, ListCollectionsRequest) (CollectionPage, error)
	GetCollection(context.Context, string) (Collection, error)
	Upsert(context.Context, string, UpsertRequest) (UpsertResponse, error)
	GetRecords(context.Context, string, GetRecordsRequest) (GetRecordsResponse, error)
	ListRecords(context.Context, string, ListRecordsRequest) (RecordPage, error)
	DeleteRecords(context.Context, string, DeleteRecordsRequest) (DeleteRecordsResponse, error)
	Reset(context.Context, string) error
	DeleteCollection(context.Context, string) error
	Search(context.Context, string, SearchRequest) (SearchResponse, error)
	Close()
}

const (
	CodeValidation    = "VALIDATION_ERROR"
	CodeCursor        = "INVALID_CURSOR"
	CodeUnauthorized  = "UNAUTHORIZED"
	CodeNotFound      = "COLLECTION_NOT_FOUND"
	CodeConflict      = "COLLECTION_CONFIG_CONFLICT"
	CodeSpaceMismatch = "EMBEDDING_SPACE_MISMATCH"
	CodeBodyLimit     = "BODY_TOO_LARGE"
	CodeDeleteBound   = "DELETE_BOUND_EXCEEDED"
	CodeUnsupported   = "NOT_IMPLEMENTED"
	CodeUnavailable   = "UNAVAILABLE"
	CodeTimeout       = "TIMEOUT"
	CodeInternal      = "INTERNAL_ERROR"
)

// Error contains only safe public information; never place driver errors,
// SQL, credentials or caller payloads in Message. Retryable is not replay safety.
type Error struct {
	Code      string `json:"code"`
	Status    int    `json:"status"`
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}

func (e *Error) Error() string { return e.Message }
func NewError(code string) *Error {
	e := &Error{Code: code}
	switch code {
	case CodeValidation:
		e.Status = 400
		e.Message = "Invalid request"
	case CodeCursor:
		e.Status = 400
		e.Message = "Invalid or mismatched cursor"
	case CodeUnauthorized:
		e.Status = 401
		e.Message = "Authentication required"
	case CodeNotFound:
		e.Status = 404
		e.Message = "Collection not found"
	case CodeConflict:
		e.Status = 409
		e.Message = "Collection configuration conflicts"
	case CodeSpaceMismatch:
		e.Status = 409
		e.Message = "Embedding space does not match"
	case CodeBodyLimit:
		e.Status = 413
		e.Message = "Request body too large"
	case CodeDeleteBound:
		e.Status = 422
		e.Message = "Delete selection exceeds limit"
	case CodeUnsupported:
		e.Status = 501
		e.Message = "Capability not implemented"
	case CodeUnavailable:
		e.Status = 503
		e.Retryable = true
		e.Message = "Required dependency unavailable"
	case CodeTimeout:
		e.Status = 504
		e.Retryable = true
		e.Message = "Operation timed out"
	default:
		e.Code = CodeInternal
		e.Status = 500
		e.Message = "Internal error"
	}
	return e
}
