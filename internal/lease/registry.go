package lease

import "context"

// Registry is the storage of lease records; the file registry implements these five calls.
type Registry interface {
	Create(ctx context.Context, l Lease) error
	Get(ctx context.Context, repo, group string) (Lease, error)
	Swap(ctx context.Context, l Lease, rev int) (int, error)
	Delete(ctx context.Context, repo, group string, rev int) error
	List(ctx context.Context, repo string) ([]Lease, error)
}

// RepoLocker is implemented by a registry that can hold one repository's grants still across processes.
type RepoLocker interface {
	LockRepo(ctx context.Context, repo string) (unlock func(), err error)
}
