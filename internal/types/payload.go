package types

import "time"

// PayloadMeta represents metadata about a stored payload file
type PayloadMeta struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Category         string    `json:"category,omitempty"`
	OriginalFilename string    `json:"original_filename"`
	Size             int64     `json:"size"`
	MimeType         string    `json:"mime_type"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// PayloadVariant represents a variant/transformation of a payload
type PayloadVariant struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// PayloadListItem combines payload metadata with its available variants
type PayloadListItem struct {
	Payload  PayloadMeta      `json:"payload"`
	Variants []PayloadVariant `json:"variants"`
}
