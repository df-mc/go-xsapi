package internal

import (
	"github.com/df-mc/go-xsapi"
	"net/http"
)

func SetTransport(client *http.Client, src xsapi.TokenSource) {
	var (
		hasTransport bool
		base         = client.Transport
	)
	if base != nil {
		_, hasTransport = base.(*xsapi.Transport)
	}
	if !hasTransport {
		client.Transport = &xsapi.Transport{
			Source: src,
			Base:   base,
		}
	}
}
