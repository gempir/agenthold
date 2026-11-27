package session

import "testing"

func TestSessionCwd(t *testing.T) {
	for _, c := range []struct{ home, rel, want string }{
		{"", "", HomeDir},
		{"/h", "", "/h"},
		{"/h", " . ", "/h"},
		{"/h", "projects/demo/", "/h/projects/demo"},
		{"/h", "a/../b", "/h/b"},
		{"/h", "/etc", "/h"},
		{"/h", "..", "/h"},
		{"/h", "../x", "/h"},
		{"/h", "a/../../x", "/h"},
		{"", "p", HomeDir + "/p"},
	} {
		if got := sessionCwd(c.home, c.rel); got != c.want {
			t.Errorf("sessionCwd(%q, %q) = %q, want %q", c.home, c.rel, got, c.want)
		}
	}
}

func TestRelInHome(t *testing.T) {
	for _, c := range []struct {
		home, p, want string
		ok            bool
	}{
		{"/h", "/h/projects/demo/x.go", "projects/demo/x.go", true},
		{"/h", "projects/demo/x.go", "projects/demo/x.go", true},
		{"/h", "/h", ".", true},
		{"/h", "/etc/passwd", "", false},
		{"/h", "../x", "", false},
		{"/h", "/hx/y", "", false},
	} {
		got, ok := relInHome(c.home, c.p)
		if got != c.want || ok != c.ok {
			t.Errorf("relInHome(%q, %q) = %q %v, want %q %v", c.home, c.p, got, ok, c.want, c.ok)
		}
	}
}

func TestCommandDir(t *testing.T) {
	for _, c := range []struct{ command, cwd, want string }{
		{"cd /home/agent/agenthold && git status", "", "/home/agent/agenthold"},
		{"cd ~/agenthold/web && bun test", "", "agenthold/web"},
		{"cd $HOME/repo; make", "", "repo"},
		{`cd "my repo" && ls`, "", "my repo"},
		{"/bin/bash -lc 'cd /home/agent/x && go test ./...'", "/home/agent", "/home/agent/x"},
		{"go test ./...", "/home/agent/y", "/home/agent/y"},
		{"cd /a && cd b", "", "/a/b"},
		{"cd agenthold", "", "agenthold"},
		{"cd - && ls", "", ""},
		{"cd $DIR && ls", "", ""},
		{"echo abcd /tmp", "", ""},
		{"str_replace", "", ""},
	} {
		if got := commandDir(c.command, c.cwd); got != c.want {
			t.Errorf("commandDir(%q, %q) = %q, want %q", c.command, c.cwd, got, c.want)
		}
	}
}
