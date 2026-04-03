package copy

import (
	"context"

	"github.com/attaradev/ditto/internal/store"
)

// CopyClient abstracts local (Manager) and remote (HTTPClient) copy operations.
// Both implementations are used by CLI commands via copyClientFromContext.
type CopyClient interface {
	Create(ctx context.Context, opts CreateOptions) (*store.Copy, error)
	Destroy(ctx context.Context, id string) error
	List(ctx context.Context) ([]*store.Copy, error)
}
