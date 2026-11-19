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
//
// A text of several lines pasted in the terminal, as bash 5.1+ and zsh
// take it with bracketed pastes, shows its lines as they are, with no
// "> " before them: as Enter ends it, the lines up from the last to the
// prompt's are the command. When they hold the paste, the command is
// kept as pasted, its indentation and tabs as they were, or, with text
// typed before or after the paste, as the screen shows it
// (pastedCommand). The shell's echo of the paste may come after Enter
// is pressed too, its newlines before the Enter's: after a paste, the
// screen is read at each newline until it holds the paste.

// enterWait is how long after Enter the output's first newline is taken
// for its echo; later, the Enter went to a program that echoes none.
const enterWait = 5 * time.Second

// typedSettle is how long after a command's line the app waits for the
// shell to ask for more ("> "), before it keeps the command.
const typedSettle = time.Second

// enterConn is the connection of a shell's terminal that reads the line
// Enter ended: after Enter, a Read stops at the output's first newline,
// the echo of the Enter, and the next Read, once the terminal has shown
// it, reads the screen's text and passes it to screen, with the text of
// several lines pasted before the Enter, from the terminal's goroutine.
// After such a paste it stops at each newline, until the screen holds
// the paste.
type enterConn struct {
	io.ReadWriteCloser
	term   atomic.Pointer[terminal.Terminal]
	enter  atomic.Int64           // when Enter was pressed last, in Unix nanoseconds; 0 once echoed
	paste  atomic.Pointer[string] // the text of several lines pasted since Enter, or nil
	pasted atomic.Pointer[string] // that of the last Enter
	screen func(text, pasted string)
	rest   []byte // output read and not passed on yet
	read   int64  // the Enter whose echo the last Read may have ended at, or 0
}

// newEnterConn wraps rw, keeping its Resize, as the terminal tells the
// server its size through it.
func newEnterConn(rw io.ReadWriteCloser, screen func(text, pasted string)) (*enterConn, io.ReadWriteCloser) {
	c := &enterConn{ReadWriteCloser: rw, screen: screen}
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
func (c *enterConn) pressed() {
	c.pasted.Store(c.paste.Swap(nil))
	c.enter.Store(time.Now().UnixNano())
}

// pastedText tells c of a text pasted in the terminal, on the main
// thread: one of several lines is the command's, or part of it, and one
// of a line takes the place of one before.
func (c *enterConn) pastedText(text string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if text = strings.Trim(text, "\n"); strings.Contains(text, "\n") {
		c.paste.Store(&text)
	} else {
		c.paste.Store(nil)
	}
}

// look reads the screen as an Enter's echo may have come: after a paste,
// only once the screen holds it.
func (c *enterConn) look(at int64) {
	t := c.term.Load()
	if t == nil {
		return
	}
	text := t.Text()
	pasted := ""
	if p := c.pasted.Load(); p != nil {
		pasted = *p
		if _, ok := pastedCommand(text, pasted); !ok {
			return // its echo is not all there yet
		}
	}
	// Enters typed ahead are left to the shell: their echoes come after
	// the first one's output.
	if c.enter.CompareAndSwap(at, 0) {
		c.screen(text, pasted)
	}
}

// Read reads the output, as the terminal does, one Read after the
// other, each read shown before the next Read.
func (c *enterConn) Read(p []byte) (int, error) {
	if at := c.read; at != 0 {
		c.read = 0
		c.look(at)
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
	c.read = at
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

// typedLine takes the screen's text as Enter ended a line in p, one of
// s's panes: its last line is a command's first line, after a prompt,
// or its next one, after "> "; after a paste of several lines, the lines
// up from it to the prompt's may be the command (pastedCommand).
func (a *App) typedLine(s *Session, p *pane, text, pasted string) {
	if !a.historyOn() {
		p.typed = nil
		return
	}
	if cmd, ok := pastedCommand(text, pasted); ok {
		a.keepTyped(s, p)
		p.typed = []string{cmd}
		a.settleTypedLater(s, p)
		return
	}
	line := lastLine(text)
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

// pastedLinesMore is how many lines more than the paste's the command
// pasted may take on the screen: the prompt's, and lines typed before
// or after the paste.
const pastedLinesMore = 10

// pastedCommand returns the command of several lines pasted in the
// terminal, as the screen's text shows it as Enter ends it: the line of
// the nearest prompt up from the last, as promptCommand reads it, and the
// lines after, when they hold the paste, as pastedText keeps it. When
// they are the paste alone, it is the paste itself, as typed tabs show as
// blanks on the screen. A paste in another program, as Python's ">>> "
// and "... ", is none.
func pastedCommand(text, pasted string) (string, bool) {
	if !strings.Contains(pasted, "\n") {
		return "", false
	}
	want := squeeze(pasted)
	lines := lastLines(text, strings.Count(pasted, "\n")+1+pastedLinesMore)
	for i := len(lines) - 1; i >= 0; i-- {
		first, ok := promptCommand(lines[i])
		if !ok {
			continue
		}
		cmd := strings.Join(append([]string{first}, lines[i+1:]...), "\n")
		switch got := squeeze(cmd); {
		case got == want:
			return pasted, true
		case strings.Contains(got, want):
			return cmd, true
		}
	}
	return "", false
}

// squeeze returns s without its blanks and newlines, for the screen's
// text and a paste to compare: the screen shows a tab as blanks, and a
// line longer than the terminal on several.
func squeeze(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// lastLines returns the last n lines of text, past its blank ones at the
// end.
func lastLines(text string, n int) []string {
	text = strings.TrimRight(text, " \t\r\n")
	start := len(text)
	for range n {
		i := strings.LastIndexByte(text[:start], '\n')
		if i < 0 {
			start = 0
			break
		}
		start = i
	}
	return strings.Split(strings.TrimPrefix(text[start:], "\n"), "\n")
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
