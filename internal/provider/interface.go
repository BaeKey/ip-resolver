package provider

import (
	"context"
	"ip-resolver/internal/model"
)

type IPProvider interface {
	Fetch(ctx context.Context, ip string) (*model.IPInfo, error)
	Name() string
}
