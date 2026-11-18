package app

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A command typed in the terminal goes into the host's history, read
// off the screen once the shell has echoed it, however late the echo
// comes; one of several lines whole, its lines after the first after the
// shell's "> "; a password, which the shell does not echo, not at all.
func TestTypedHistory(t *testing.T) {
	a, p, s, tt, _, srv := barServerSrv(t)
	tt.ClickAt(760, 260) // in the terminal
	id := s.historyID()
	sent := ""
	// typeLine types line and Enter, and waits for the shell to read them;
	// the shell answers with out only then, as a far server does.
	typeLine := func(line, out string) {
		t.Helper()
		tt.Type(line)
		tt.Key(0, ui.KeyEnter)
		sent += line + "\r"
		deadline := time.Now().Add(5 * time.Second)
		p.until(t, "the shell's input", func() bool {
			return srv.ShellInput(0) == sent || time.Now().After(deadline)
		})
		if got := srv.ShellInput(0); got != sent {
			t.Fatalf("the shell read %q, want %q", got, sent)
		}
		srv.ShellOutput(0, out)
	}
	kept := func(cmd string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		p.until(t, "the history", func() bool {
			return slices.Contains(a.hostHistory(id).cmds, cmd) || time.Now().After(deadline)
		})
		if got := a.hostHistory(id).cmds; !slices.Contains(got, cmd) {
			t.Fatalf("history %q, want %q in it", got, cmd)
		}
	}

	typeLine("ls -la /tmp", "ls -la /tmp\r\ntotal 0\r\ntester@web-1:~$ ")
	kept("ls -la /tmp")

	typeLine("python3 -c '", "python3 -c '\r\n> ")
	typeLine("print(1)", "print(1)\r\n> ")
	typeLine("'", "'\r\n1\r\ntester@web-1:~$ ")
	kept("python3 -c '\nprint(1)\n'")

	typeLine("sudo true", "sudo true\r\n[sudo] password for tester: ")
	typeLine("secret", "\r\ntester@web-1:~$ ")
	typeLine("df -h", "df -h\r\ntester@web-1:~$ ")
	kept("df -h")
	if got := a.hostHistory(id).cmds; slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, "secret") }) {
		t.Errorf("the password went into the history: %q", got)
	}
	if got, want := a.hostHistory(id).cmds, []string{"ls -la /tmp", "python3 -c '\nprint(1)\n'", "sudo true", "df -h"}; !slices.Equal(got, want) {
		t.Errorf("history %q, want %q", got, want)
	}
}

func TestPromptCommand(t *testing.T) {
	for _, c := range []struct {
		line, cmd string
		ok        bool
	}{
		{"phonebill@wuxi-server1:~$ docker ps -a", "docker ps -a", true},
		{"root@web-1:/etc# systemctl restart nginx", "systemctl restart nginx", true},
		{"phonebill@mac finallyshell % go test ./...", "go test ./...", true},
		{"[tester@web-1 ~]$ echo $ HOME # not a prompt", "echo $ HOME # not a prompt", true},
		{"$ ls", "ls", true},
		{"phonebill@wuxi-server1:~$", "", false}, // nothing typed: trimmed
		{">>> print('$ x')", "", false},
		{`mysql> select "a$ b";`, "", false},
		{"[sudo] password for tester:", "", false},
	} {
		if cmd, ok := promptCommand(c.line); cmd != c.cmd || ok != c.ok {
			t.Errorf("%q: %q %v, want %q %v", c.line, cmd, ok, c.cmd, c.ok)
		}
	}
	for line, want := range map[string]string{"> print(1)": "print(1)", ">": "", ">  -p 80:80 \\": " -p 80:80 \\"} {
		if got, ok := continuedLine(line); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", line, got, ok, want)
		}
	}
	if _, ok := continuedLine(">>> 1"); ok {
		t.Errorf(">>> taken for PS2")
	}
}
