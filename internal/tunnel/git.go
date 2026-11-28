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

// maxDataReply bounds data-framed JSON replies (diffs, file contents).
const maxDataReply = 64 << 20

// GitData runs a git action whose reply is data-framed JSON (GitDiff, GitLog).
func (s *Session) GitData(ctx context.Context, req GitRequest) (GitReply, error) {
	c, err := s.Open(ctx, Open{Op: OpGit, Git: &req})
	if err != nil {
		return GitReply{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var out GitReply
	if err := ReadJSONData(c, &out, maxDataReply); err != nil {
		return GitReply{}, unsupported(err)
	}
	return out, nil
}

// ReadText reads a file inside the home for a viewer or editor: at most
// maxBytes of it, and with startLine/endLine > 0 only those lines.
func (s *Session) ReadText(ctx context.Context, path string, maxBytes int64, startLine, endLine int, deny ...string) (TextFile, error) {
	c, err := s.Open(ctx, Open{Op: OpFSReadText, Path: path, MaxBytes: maxBytes, StartLine: startLine, EndLine: endLine, Deny: deny})
	if err != nil {
		return TextFile{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var out TextFile
	if err := ReadJSONData(c, &out, maxDataReply); err != nil {
		return TextFile{}, unsupported(err)
	}
	return out, nil
}

// WriteText replaces a file inside the home with content atomically (an
// existing file keeps its mode) and describes the written file.
func (s *Session) WriteText(ctx context.Context, path string, content []byte, deny ...string) (TextFile, error) {
	c, err := s.Open(ctx, Open{Op: OpFSWriteText, Path: path, Deny: deny})
	if err != nil {
		return TextFile{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	errc := make(chan error, 1)
	var out TextFile
	go func() { errc <- ReadJSON(c, &out) }()
	if _, err := DataWriter(c).Write(content); err != nil {
		_ = c.Close()
		if rerr := <-errc; rerr != nil {
			return TextFile{}, unsupported(rerr)
		}
		return TextFile{}, err
	}
	if err := WriteFrame(c, FrameEOF, nil); err != nil {
		return TextFile{}, err
	}
	if err := <-errc; err != nil {
		return TextFile{}, unsupported(err)
	}
	return out, nil
}

// SearchFiles searches file paths below base (inside the home) for query.
func (s *Session) SearchFiles(ctx context.Context, base, query string, limit int) (FileSearchReply, error) {
	c, err := s.Open(ctx, Open{Op: OpFSSearch, Path: base, Query: query, Limit: limit})
	if err != nil {
		return FileSearchReply{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var out FileSearchReply
	if err := ReadJSONData(c, &out, maxDataReply); err != nil {
		return FileSearchReply{}, unsupported(err)
	}
	return out, nil
}
