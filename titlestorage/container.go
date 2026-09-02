package titlestorage

import (
	"time"
)

type ContainerFilter struct {
	ContinuationToken string

	// TODO: Figure out how continuation token is specified in requests. It's probably 'continuationToken' query parameter.
}

type ContainerResult struct {
	Containers []Container `json:"blobs"`
	PagingInfo PagingInfo  `json:"pagingInfo"`
}

type PagingInfo struct {
	ContinuationToken string `json:"continuationToken"`
	TotalItems        int    `json:"totalItems"`
}

type Container struct {
	ID             string    `json:"fileName"`
	DisplayName    string    `json:"displayName"`
	ETag           string    `json:"etag"`
	ClientFileTime time.Time `json:"clientFileTime"`
	Size           int64     `json:"size"`
}

type Atom struct {
	Name string `json:"name"`
	ID   string `json:"atom"`
	Size int64  `json:"size"`
}

// Container -> Atom
