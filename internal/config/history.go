package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// The command bar's history (internal/app/commandbar.go) is kept per
// host, in history/<host ID>.txt in Dir(): one command per line, the
// oldest first, each command once, a command sent again moved to the
// end. It has no limit on the number of commands, as FinalShell's, and is
// cut from the oldest only once it passes MaxHistoryBytes. The format is
// kept this plain so that sync could carry it later. A command of several
// lines is written on one line, its newlines as historyNewline.

// MaxHistoryBytes is how large a host's history file grows before its
// oldest commands are dropped.
const MaxHistoryBytes = 10 << 20

// historyNewline stands for a newline of a command in a history file: a
// control character, which no command kept has otherwise (Keepable), so
// it reads back as nothing else. A version of the app from before
// commands of several lines were kept leaves such a line out.
const historyNewline = "\x1e"

// historyMu keeps the history files' reads and writes one at a time: the
// app writes them on goroutines.
var historyMu sync.Mutex

// HistoryDir is the folder of the history files.
func HistoryDir() string { return filepath.Join(Dir(), "history") }

// historyPath is the file of a host's history. Host IDs are the app's
// own ("h" and digits), but one from another device's sync or a
// hand-edited hosts.json could hold anything, so any byte that is not a
// plain file name's is written as %xx.
func historyPath(hostID string) string {
	var b strings.Builder
	for i := 0; i < len(hostID); i++ {
		c := hostID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_',
			c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02x", c)
		}
	}
	return filepath.Join(HistoryDir(), b.String()+".txt")
}

// LoadHistory reads a host's history, the oldest command first: nil when
// it has none.
func LoadHistory(hostID string) ([]string, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	return loadHistory(hostID)
}

func loadHistory(hostID string) ([]string, error) {
	data, err := os.ReadFile(historyPath(hostID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []string
	for line := range strings.Lines(string(data)) {
		line = strings.ReplaceAll(strings.TrimRight(line, "\r\n"), historyNewline, "\n")
		if Keepable(line) {
			list = append(list, line)
		}
	}
	return Deduped(list), nil
}

// AddHistory adds cmd to a host's history, as the newest command; one
// already there moves to the end. A command of blanks, or with a control
// character other than tab and newline (an escape sequence), is not kept.
func AddHistory(hostID, cmd string) error {
	if !Keepable(cmd) {
		return nil
	}
	historyMu.Lock()
	defer historyMu.Unlock()
	list, err := loadHistory(hostID)
	if err != nil {
		return err
	}
	list = TrimHistory(AddCommand(list, cmd), MaxHistoryBytes)
	return writeHistory(hostID, list)
}

// writeHistory replaces a host's history file with list, through a
// temporary file, so that a crash leaves the old one or the new one.
func writeHistory(hostID string, list []string) error {
	if err := os.MkdirAll(HistoryDir(), 0o700); err != nil {
		return err
	}
	var b bytes.Buffer
	for _, c := range list {
		b.WriteString(strings.ReplaceAll(c, "\n", historyNewline))
		b.WriteByte('\n')
	}
	p := historyPath(hostID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// DeleteHistory deletes a host's history, as the host is deleted for
// good. A host without one is no error.
func DeleteHistory(hostID string) error {
	historyMu.Lock()
	defer historyMu.Unlock()
	if err := os.Remove(historyPath(hostID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ClearAllHistory deletes the history of every host.
func ClearAllHistory() error {
	historyMu.Lock()
	defer historyMu.Unlock()
	return os.RemoveAll(HistoryDir())
}

// Keepable tells whether cmd can go in a history: not only blanks, and
// with no control character but tabs and the newlines between the lines
// of a command of several.
func Keepable(cmd string) bool {
	return strings.TrimSpace(cmd) != "" && !HasControl(strings.ReplaceAll(cmd, "\n", ""), true)
}

// HasControl tells whether s has a control character: C0 (U+0000 to
// U+001F), DEL or C1 (U+0080 to U+009F), tabs aside with tab set. A
// command with one, inserted in the command bar and sent, would run more
// than it shows (a newline the bar does not show) or drive the terminal
// (an escape sequence), so the history keeps none, but for the newlines
// of a command of several lines, which the bar shows as lines, and the
// bar inserts none.
func HasControl(s string, tab bool) bool {
	for _, r := range s {
		if (r < 0x20 && !(tab && r == '\t')) || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

// AddCommand returns list, the oldest first, with cmd as its newest
// command, taken out of where it was before.
func AddCommand(list []string, cmd string) []string {
	list = slices.DeleteFunc(list, func(c string) bool { return c == cmd })
	return append(list, cmd)
}

// Deduped returns list, the oldest first, with each command once, where
// it was last.
func Deduped(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		if c := list[i]; !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	slices.Reverse(out)
	return out
}

// TrimHistory drops the oldest commands of list until it takes at most
// limit bytes as a file, each command and its newline.
func TrimHistory(list []string, limit int) []string {
	size := 0
	for _, c := range list {
		size += len(c) + 1
	}
	i := 0
	for size > limit && i < len(list) {
		size -= len(list[i]) + 1
		i++
	}
	return list[i:]
}

// ParseShellHistory reads a shell's history file: bash's, with the
// "#<seconds>" lines HISTTIMEFORMAT writes between commands, or, with
// zsh set, zsh's, whose extended format puts ": <start>:<elapsed>;"
// before each command, and which writes the bytes 0x83 to 0x9d, and NUL,
// "metafied": 0x83, then the byte xor 0x20. A command of several lines is
// read as one, its lines joined by newlines (multiLine). Blank lines are
// left out, as are commands with a control character other than tab and
// those newlines (HasControl), so that a file written to put escape
// sequences into the terminal puts nothing there. The result is the
// oldest first, each command once.
func ParseShellHistory(data []byte, zsh bool) []string {
	if zsh {
		data = unmetafy(data)
	}
	var list []string
	var lines []string // the lines of the command being read
	stamped := false   // that command came after a bash time line
	end := func() {
		cmd := strings.Join(lines, "\n")
		if len(lines) > 1 {
			cmd = strings.TrimSpace(cmd)
		}
		if Keepable(cmd) {
			list = append(list, cmd)
		}
		lines = lines[:0]
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		if n := len(lines); n > 0 {
			if stamped && !isTimestamp(line) {
				lines = append(lines, line) // all is one command up to the next time line
				continue
			}
			if next, ok := multiLine(lines[n-1], line, zsh); ok {
				lines[n-1] = next
				lines = append(lines, line)
				continue
			}
			end()
		}
		if isTimestamp(line) {
			stamped = !zsh
			continue
		}
		if cmd, ok := zshCommand(line); ok {
			line = cmd
		}
		lines = append(lines, line)
	}
	if len(lines) > 0 {
		end()
	}
	return Deduped(list)
}

// multiLine tells whether line goes on the command whose last line so
// far is last, and returns last as it reads then. Both shells write a
// newline of a command as it is, and so its next line on a line of its
// own:
//
//   - zsh puts a backslash before it, so a line ending with one goes on
//     on the next, that backslash taken out. A line that starts an entry
//     of the extended format, ": <start>:<elapsed>;", is another command.
//   - bash writes nothing to tell a newline from the end of a command. A
//     line ending with a backslash that escapes nothing, as "docker run
//     \" or a backslash in quotes, goes on on the next, the backslash
//     kept, as typed; a time line, "#<seconds>", is another command.
//     Other commands of several lines, as a quoted text of several lines
//     without backslashes, are read a line a command, as bash's file
//     cannot tell where they end; but with HISTTIMEFORMAT set, bash
//     puts a time line before each command, and ParseShellHistory reads
//     all the lines up to the next one as one command.
func multiLine(last, line string, zsh bool) (string, bool) {
	n := 0
	for i := len(last) - 1; i >= 0 && last[i] == '\\'; i-- {
		n++
	}
	if zsh {
		if _, entry := zshCommand(line); n == 0 || entry {
			return last, false
		}
		return last[:len(last)-1], true
	}
	return last, n%2 == 1 && !isTimestamp(line)
}

// isTimestamp tells whether line is bash's "#1700000000".
func isTimestamp(line string) bool {
	if len(line) < 2 || line[0] != '#' {
		return false
	}
	for _, c := range line[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// zshCommand returns the command of a line of zsh's extended history,
// ": 1700000000:0;ls -l".
func zshCommand(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, ": ")
	if !ok {
		return "", false
	}
	meta, cmd, ok := strings.Cut(rest, ";")
	if !ok {
		return "", false
	}
	start, elapsed, ok := strings.Cut(meta, ":")
	if !ok || !digits(start) || !digits(elapsed) {
		return "", false
	}
	return cmd, true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// unmetafy undoes zsh's metafication: 0x83 followed by a byte xor 0x20.
// In a file of zsh's every 0x83 is one, as an 0x83 of the text is
// metafied too.
func unmetafy(data []byte) []byte {
	if bytes.IndexByte(data, 0x83) < 0 {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == 0x83 && i+1 < len(data) {
			i++
			out = append(out, data[i]^0x20)
			continue
		}
		out = append(out, data[i])
	}
	return out
}
