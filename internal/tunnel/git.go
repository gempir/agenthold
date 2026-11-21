package tunnel

import "context"

// Git runs a git action in the sandbox.
func (s *Session) Git(ctx context.Context, req GitRequest) (GitReply, error) {
	c, err := s.Open(ctx, Open{Op: OpGit, Git: &req})
	if err != nil {
		return GitReply{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var out GitReply
	if err := ReadJSON(c, &out); err != nil {
		return GitReply{}, unsupported(err)
	}
	return out, nil
}

// HelloInfo asks sandboxd who it is (its version, account and home).
func (s *Session) HelloInfo(ctx context.Context) (Hello, error) {
	c, err := s.Open(ctx, Open{Op: OpHello})
	if err != nil {
		return Hello{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var out Hello
	if err := ReadJSON(c, &out); err != nil {
		return Hello{}, err
	}
	return out, nil
}
