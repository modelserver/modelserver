package types

// PaginationParams holds common query parameters for paginated list endpoints.
type PaginationParams struct {
	Page    int    `json:"page"`
	PerPage int    `json:"per_page"`
	Sort    string `json:"sort"`
	Order   string `json:"order"` // "asc" or "desc"
}

// DefaultPagination returns a PaginationParams with sensible defaults.
func DefaultPagination() PaginationParams {
	return PaginationParams{Page: 1, PerPage: 20, Sort: "created_at", Order: "desc"}
}

// Offset returns the SQL OFFSET value derived from the current page and per-page size.
func (p PaginationParams) Offset() int { return (p.Page - 1) * p.PerPage }

// Limit returns the effective per-page limit, capped at 100 and floored at 20.
func (p PaginationParams) Limit() int {
	if p.PerPage > 100 {
		return 100
	}
	if p.PerPage <= 0 {
		return 20
	}
	return p.PerPage
}

// ListResponse wraps a list of items with pagination metadata.
type ListResponse[T any] struct {
	Data []T  `json:"data"`
	Meta Meta `json:"meta"`
}

// Meta carries pagination metadata returned alongside list responses.
type Meta struct {
	Total   int `json:"total"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

// DataResponse wraps a single item in a standard envelope.
type DataResponse[T any] struct {
	Data T `json:"data"`
}

// ErrorResponse is the standard error envelope returned by the API.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides a machine-readable code alongside a human-readable message.
type ErrorDetail struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}
