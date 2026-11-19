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

// A text of several lines pasted in the terminal goes into the history
// as pasted, its indentation and tabs as they were, though the screen
// shows a tab as blanks and no "> " before its lines; with text typed
// before it, as the screen shows it. One pasted in another program, as
// Python's ">>> ", does not. The echo of the paste may come after Enter.
func TestPastedHistory(t *testing.T) {
	a, p, s, tt, _, srv := barServerSrv(t)
	tt.ClickAt(760, 260) // in the terminal
	id := s.historyID()
	input := func(suffix string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		p.until(t, "the shell's input", func() bool {
			return strings.HasSuffix(srv.ShellInput(0), suffix) || time.Now().After(deadline)
		})
		if got := srv.ShellInput(0); !strings.HasSuffix(got, suffix) {
			t.Fatalf("the shell read %q, want it to end with %q", got, suffix)
		}
	}
	// The shell asks for bracketed pastes, as bash 5.1+ does.
	srv.ShellOutput(0, "\x1b[?2004h\r\ntester@web-1:~$ ")
	p.until(t, "the prompt", func() bool { return strings.Count(s.term().Text(), "tester@web-1:~$") == 2 })
	paste := func(text, echo string) {
		t.Helper()
		tt.SetClipboard(text)
		k := a.shortcut(scPaste)
		tt.Key(k.Mods, k.Key)
		input("\x1b[201~")
		srv.ShellOutput(0, echo)
	}
	enter := func(out string) {
		t.Helper()
		tt.Key(0, ui.KeyEnter)
		input("\r")
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

	script := "python3 -c '\nclass H:\n    def f(self):\n\tprint(1)\nH().f()\n'\n"
	// Enter before the echo of the paste, as from a far server: its
	// newlines come before the Enter's.
	paste(script, "")
	enter("python3 -c '\r\nclass H:\r\n    def f(self):\r\n        print(1)\r\nH().f()\r\n'\r\n1\r\ntester@web-1:~$ ")
	kept(strings.TrimSuffix(script, "\n"))

	tt.Type("echo start && ")
	input("echo start && ")
	srv.ShellOutput(0, "echo start && ")
	paste("cat <<'EOF'\n  indented\nEOF", "cat <<'EOF'\r\n  indented\r\nEOF")
	enter("\r\nstart\r\n  indented\r\ntester@web-1:~$ ")
	kept("echo start && cat <<'EOF'\n  indented\nEOF")

	tt.Type("python3")
	enter("python3\r\nPython 3.13\r\n>>> ")
	kept("python3")
	paste("a = 1\nb = 2", "a = 1\r\n... b = 2")
	enter("\r\n>>> ")
	tt.Type("exit()")
	enter("exit()\r\ntester@web-1:~$ ")
	tt.Type("uptime")
	enter("uptime\r\n up 1 day\r\ntester@web-1:~$ ")
	kept("uptime")
	want := []string{strings.TrimSuffix(script, "\n"), "echo start && cat <<'EOF'\n  indented\nEOF", "python3", "uptime"}
	if got := a.hostHistory(id).cmds; !slices.Equal(got, want) {
		t.Errorf("history %q,\nwant %q", got, want)
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
