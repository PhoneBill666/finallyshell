package app

import (
	"bytes"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"finallyshell/internal/config"

	"github.com/egoist/mygo/plugins/terminal"
)

// Commands typed in a session's terminal, as well as those sent from the
// command bar, go into the host's history. The app cannot tell what the
// shell makes of the keys, with its completion, its own history and line
// editing, so it reads the line on the screen as Enter ends it: the
// prompt and the command after it. The shell's echo of what was typed
// may come after Enter is pressed, the more so the farther the server, so
// the line is read once the echo of the Enter itself has come
// (enterConn). A command of several lines, as a quoted text, a heredoc or
// lines ending with a backslash, goes on on lines that start with bash's
// and zsh's "> " (PS2), and is kept whole.

// enterWait is how long after Enter the output's first newline is taken
// for its echo; later, the Enter went to a program that echoes none.
const enterWait = 5 * time.Second

// typedSettle is how long after a command's line the app waits for the
// shell to ask for more ("> "), before it keeps the command.
const typedSettle = time.Second

// enterConn is the connection of a shell's terminal that reads the line
// Enter ended: after Enter, a Read stops at the output's first newline,
// the echo of the Enter, and the next Read, once the terminal has shown
// it, reads the screen's last line and passes it to line, from the
// terminal's goroutine.
type enterConn struct {
	io.ReadWriteCloser
	term  atomic.Pointer[terminal.Terminal]
	enter atomic.Int64 // when Enter was pressed last, in Unix nanoseconds; 0 once echoed
	line  func(string)
	rest  []byte // output read and not passed on yet
	read  bool   // the last Read ended at the echo of an Enter
}

// newEnterConn wraps rw, keeping its Resize, as the terminal tells the
// server its size through it.
func newEnterConn(rw io.ReadWriteCloser, line func(string)) (*enterConn, io.ReadWriteCloser) {
	c := &enterConn{ReadWriteCloser: rw, line: line}
	if r, ok := rw.(interface{ Resize(cols, rows int) error }); ok {
		return c, enterResizeConn{c, r}
	}
	return c, c
}

type enterResizeConn struct {
	*enterConn
	r interface{ Resize(cols, rows int) error }
}

func (c enterResizeConn) Resize(cols, rows int) error { return c.r.Resize(cols, rows) }

// pressed tells c that Enter was pressed in the terminal.
func (c *enterConn) pressed() { c.enter.Store(time.Now().UnixNano()) }

// Read reads the output, as the terminal does, one Read after the
// other, each read shown before the next Read.
func (c *enterConn) Read(p []byte) (int, error) {
	if c.read {
		c.read = false
		if t := c.term.Load(); t != nil {
			c.line(lastLine(t.Text()))
		}
	}
	var n int
	var err error
	if len(c.rest) > 0 {
		n = copy(p, c.rest)
		c.rest = c.rest[n:]
	} else {
		n, err = c.ReadWriteCloser.Read(p)
	}
	at := c.enter.Load()
	if at == 0 || err != nil {
		return n, err
	}
	if time.Since(time.Unix(0, at)) > enterWait {
		c.enter.CompareAndSwap(at, 0)
		return n, err
	}
	i := bytes.IndexByte(p[:n], '\n')
	if i < 0 {
		return n, err
	}
	// Enters typed ahead are left to the shell: their echoes come
	// after the first one's output.
	c.enter.CompareAndSwap(at, 0)
	c.read = true
	c.rest = append(append([]byte(nil), p[i+1:n]...), c.rest...)
	return i + 1, nil
}

// promptCommand returns the command typed after the shell's prompt on
// line: what follows the first "$ ", "# " or "% ", the ends of bash's,
// root's and zsh's prompts. A prompt that starts with ">", as Python's
// ">>> ", or has quotes in it, as a program's output, is none.
func promptCommand(line string) (string, bool) {
	end := -1
	for _, m := range []string{"$ ", "# ", "% "} {
		if i := strings.Index(line, m); i >= 0 && (end < 0 || i < end) {
			end = i
		}
	}
	if end < 0 {
		return "", false
	}
	if prompt := line[:end]; strings.HasPrefix(prompt, ">") || strings.ContainsAny(prompt, `'"`) {
		return "", false
	}
	return strings.TrimSpace(line[end+2:]), true
}

// continuedLine returns the text of a line of the shell's "> " prompt,
// PS2, which goes on with the command of the lines before.
func continuedLine(line string) (string, bool) {
	if line == ">" {
		return "", true
	}
	rest, ok := strings.CutPrefix(line, "> ")
	return rest, ok
}

// typedLine takes the line Enter ended in p, one of s's panes: a
// command's first line, after a prompt, or its next one, after "> ".
func (a *App) typedLine(s *Session, p *pane, line string) {
	if !a.historyOn() {
		p.typed = nil
		return
	}
	if rest, ok := continuedLine(line); ok && p.typed != nil {
		p.typed = append(p.typed, rest)
		a.settleTypedLater(s, p)
		return
	}
	a.keepTyped(s, p)
	if cmd, ok := promptCommand(line); ok && cmd != "" {
		p.typed = []string{cmd}
		a.settleTypedLater(s, p)
	}
}

// settleTypedLater keeps p's command once the shell has had the time to
// ask for more of it.
func (a *App) settleTypedLater(s *Session, p *pane) {
	time.AfterFunc(typedSettle, func() { a.post(func() { a.settleTyped(s, p) }) })
}

// settleTyped keeps the command typed in p, one of s's panes, unless the
// shell asks for more of it, its last line "> ".
func (a *App) settleTyped(s *Session, p *pane) {
	if p.typed == nil {
		return
	}
	if t := p.term; t != nil {
		if _, more := continuedLine(lastLine(t.Text())); more {
			return
		}
	}
	a.keepTyped(s, p)
}

// keepTyped adds the command typed in p, one of s's panes, to the host's
// history.
func (a *App) keepTyped(s *Session, p *pane) {
	lines := p.typed
	p.typed = nil
	if cmd := strings.TrimSpace(strings.Join(lines, "\n")); len(lines) > 0 && a.historyOn() && config.Keepable(cmd) {
		a.addHistory(s.historyID(), cmd)
	}
}
