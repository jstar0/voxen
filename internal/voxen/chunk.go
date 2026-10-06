package voxen

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Speech tags are written as inline [tag] or wrapping <tag>…</tag>. They are never
// split in half, and a wrapper that has to straddle a chunk boundary is closed at
// the end of one chunk and reopened at the start of the next.
var tagPattern = regexp.MustCompile(`\[[A-Za-z][A-Za-z-]*\]|</?[A-Za-z][A-Za-z-]*>`)

const (
	unitText = iota
	unitInline
	unitOpen
	unitClose
)

type unit struct {
	s    string
	kind int
	name string
}

func tokenize(text string) []unit {
	var units []unit
	last := 0
	for _, loc := range tagPattern.FindAllStringIndex(text, -1) {
		if loc[0] > last {
			units = append(units, sentences(text[last:loc[0]])...)
		}
		tag := text[loc[0]:loc[1]]
		switch {
		case strings.HasPrefix(tag, "["):
			units = append(units, unit{s: tag, kind: unitInline})
		case strings.HasPrefix(tag, "</"):
			units = append(units, unit{s: tag, kind: unitClose, name: tag[2 : len(tag)-1]})
		default:
			units = append(units, unit{s: tag, kind: unitOpen, name: tag[1 : len(tag)-1]})
		}
		last = loc[1]
	}
	if last < len(text) {
		units = append(units, sentences(text[last:])...)
	}
	return units
}

func isTerminator(r rune) bool {
	return strings.ContainsRune("。！？!?；;…\n", r)
}

func isTrailer(r rune) bool {
	return strings.ContainsRune("”’」』)）\"' \t\r\n", r)
}

// sentences splits plain text after sentence-ending punctuation.
func sentences(s string) []unit {
	var units []unit
	runes := []rune(s)
	start := 0
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		end := isTerminator(r)
		if r == '.' && (i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n') {
			end = true
		}
		if !end {
			continue
		}
		for i+1 < len(runes) && (isTerminator(runes[i+1]) || isTrailer(runes[i+1])) {
			i++
		}
		units = append(units, unit{s: string(runes[start : i+1])})
		start = i + 1
	}
	if start < len(runes) {
		units = append(units, unit{s: string(runes[start:])})
	}
	return units
}

// hardSplit cuts an over-long sentence at a comma or space when one is close to
// the limit, otherwise exactly at the limit.
func hardSplit(s string, max int) []string {
	runes := []rune(s)
	var parts []string
	for len(runes) > max {
		cut := max
		for i := max - 1; i >= max/2; i-- {
			if strings.ContainsRune("，,、；; \t", runes[i]) {
				cut = i + 1
				break
			}
		}
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

// splitText divides text into chunks of at most max characters, cutting on
// sentence boundaries. max <= 0 disables splitting. Opening tags are held back
// until text follows them, so a wrapper is never sent empty.
func splitText(text string, max int) []string {
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	curLen := 0
	hasText := false
	var stack []string   // wrappers written into the current chunk and still open
	var pending []string // wrappers opened but not yet written (no text inside yet)
	flush := func(final bool) {
		if !hasText {
			return
		}
		s := cur.String()
		if !final {
			for i := len(stack) - 1; i >= 0; i-- {
				s += "</" + stack[i] + ">"
			}
		}
		if s = strings.TrimSpace(s); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
		curLen, hasText = 0, false
		if !final {
			for _, name := range stack {
				cur.WriteString("<" + name + ">")
				curLen += len(name) + 2
			}
		}
	}
	add := func(s string) {
		cur.WriteString(s)
		curLen += utf8.RuneCountInString(s)
	}
	pendingLen := func() int {
		n := 0
		for _, name := range pending {
			n += len(name) + 2
		}
		return n
	}
	place := func(n int, isText bool, s string) {
		if hasText && curLen+pendingLen()+n > max {
			flush(false)
		}
		for _, name := range pending {
			add("<" + name + ">")
			stack = append(stack, name)
		}
		pending = nil
		add(s)
		if isText && strings.TrimSpace(s) != "" {
			hasText = true
		}
	}
	for _, u := range tokenize(text) {
		n := utf8.RuneCountInString(u.s)
		switch u.kind {
		case unitOpen:
			pending = append(pending, u.name)
		case unitClose:
			if len(pending) > 0 && pending[len(pending)-1] == u.name {
				pending = pending[:len(pending)-1] // wrapper with nothing inside
			} else {
				add(u.s)
				if len(stack) > 0 && stack[len(stack)-1] == u.name {
					stack = stack[:len(stack)-1]
				}
			}
		case unitInline:
			place(n, false, u.s)
		default:
			pieces := []string{u.s}
			if n > max {
				pieces = hardSplit(u.s, max)
			}
			for _, piece := range pieces {
				place(utf8.RuneCountInString(piece), true, piece)
			}
		}
	}
	flush(true)
	if len(chunks) == 0 {
		return []string{text}
	}
	return chunks
}
