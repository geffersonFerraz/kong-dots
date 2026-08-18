package kong

import (
	"context"
	"sync"
)

// State is a full snapshot of the entities the tool manages, keyed by kind.
type State map[string][]Entity

// Snapshot reads every managed kind from the Admin API concurrently.
func (c *Client) Snapshot(ctx context.Context) (State, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		state    = State{}
		firstErr error
	)
	for _, kind := range Kinds {
		wg.Add(1)
		go func(kind string) {
			defer wg.Done()
			items, err := c.List(ctx, kind)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			state[kind] = items
		}(kind)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	for _, kind := range Kinds {
		if state[kind] == nil {
			state[kind] = []Entity{}
		}
	}
	return state, nil
}
