package types

import "time"

// IpProfile represents an IP address profile with request statistics
type IpProfile struct {
	IP        string    `json:"ip"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	ReqCount  int64     `json:"req_count"`
}

// IpSummary represents a summary of activity for a specific IP address
type IpSummary struct {
	UA       []map[string]any `json:"ua"`
	Paths    []map[string]any `json:"paths"`
	Timeline []TimelinePoint  `json:"timeline"`
	Classes  *ClassCounts     `json:"classes,omitempty"`
}

// TimelinePoint represents a single point in a timeline with timestamp and count
type TimelinePoint struct {
	T time.Time `json:"t"`
	C int64     `json:"c"`
}

// ClassCounts holds counts of different request classifications
type ClassCounts struct {
	PreviewBot      int64 `json:"preview_bot"`
	Scanner         int64 `json:"scanner"`
	RealUserPreview int64 `json:"real_user_preview"`
	ClickUser       int64 `json:"click_user"`
}

// IpSizes represents various size metrics for IP-related data
type IpSizes struct {
	Geo struct {
		Bytes   int64 `json:"bytes"`
		Present bool  `json:"present"`
	} `json:"geo"`
	Requests struct {
		Rows int64 `json:"rows"`
	} `json:"requests"`
	UA struct {
		Count int64 `json:"count"`
	} `json:"ua"`
	Summary struct {
		Bytes int64 `json:"bytes"`
	} `json:"summary"`
	All struct {
		BytesEstimate int64 `json:"bytes_estimate"`
		Estimated     bool  `json:"estimated"`
	} `json:"all"`
}
