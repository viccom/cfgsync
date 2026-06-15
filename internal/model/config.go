package model

// Config is the persisted user configuration snapshot.
type Config struct {
	Version   int64  `json:"version"`
	Payload   string `json:"payload"`
	UpdatedAt int64  `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

// PutConfigRequest is the body of PUT /api/v1/config.
type PutConfigRequest struct {
	Version   int64  `json:"version"`
	Payload   string `json:"payload"`
	UpdatedBy string `json:"updated_by"`
}

// ConflictResponse is the body of a 409 Conflict.
type ConflictResponse struct {
	Error            string `json:"error"`
	CurrentVersion   int64  `json:"current_version"`
	CurrentPayload   string `json:"current_payload"`
	CurrentUpdatedAt int64  `json:"current_updated_at"`
	CurrentUpdatedBy string `json:"current_updated_by"`
}
