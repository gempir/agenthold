package sandbox

import "context"

// BootFilesFunc returns files written into a sandbox before each start.
type BootFilesFunc = func(ctx context.Context, agentID string) (map[string][]byte, error)

// MergeBootFiles combines several sources of boot files (nil ones are
// skipped). A later source wins for a path both return; the first error
// fails the start.
func MergeBootFiles(fns ...BootFilesFunc) BootFilesFunc {
	return func(ctx context.Context, agentID string) (map[string][]byte, error) {
		out := map[string][]byte{}
		for _, fn := range fns {
			if fn == nil {
				continue
			}
			files, err := fn(ctx, agentID)
			if err != nil {
				return nil, err
			}
			for p, b := range files {
				out[p] = b
			}
		}
		return out, nil
	}
}
