package view

import (
	"io"
	"strings"
)

// Choice is one answer to a question: its key (a letter), the word that also selects it, and what it does, for the
// line that lists the choices.
type Choice struct {
	Key, Word, Label string
}

// Ask writes a question and its choices, without a newline, for the answer to follow on the same line: "Delete all
// 5, select one by one, list them all, or none? [a/s/l/N] ". The default is capitalised; it is always the safe
// choice, and Enter or the end of input takes it.
func (r *Renderer) Ask(question string, choices []Choice, def string) {
	keys := make([]string, len(choices))
	for i, c := range choices {
		keys[i] = c.Key
		if c.Key == def {
			keys[i] = strings.ToUpper(c.Key)
		}
	}
	r.prompt(styled(question, bold), plain(" ["+strings.Join(keys, "/")+"] "))
}

// Choose writes a select-mode item's choices on one line, indented under it, for the answer to follow: "d delete ·
// r new retention · s skip · q quit  › ".
func (r *Renderer) Choose(choices []Choice) {
	parts := make([]string, len(choices))
	for i, c := range choices {
		parts[i] = c.Key + " " + c.Label
	}
	r.prompt(spaces(8), plain(strings.Join(parts, r.sep())), plain("  "+r.chevron()+" "))
}

// AskText writes a question asking for a value (a retention), indented under a select-mode item.
func (r *Renderer) AskText(question string) {
	r.prompt(spaces(8), plain(question), plain("  "+r.chevron()+" "))
}

// prompt writes spans with no newline, in one call.
func (r *Renderer) prompt(spans ...span) { io.WriteString(r.o.Out, r.render(spans)) }

// Again says which answers a question takes, after one it did not understand: "  answer a (all), s (select), l
// (list) or n (none)".
func (r *Renderer) Again(choices []Choice) {
	parts := make([]string, len(choices))
	for i, c := range choices {
		parts[i] = c.Key + " (" + c.Word + ")"
	}
	text := strings.Join(parts, ", ")
	if i := strings.LastIndex(text, ", "); i >= 0 {
		text = text[:i] + " or " + text[i+2:]
	}
	r.write(spaces(2), styled("answer "+text, yellow))
}

// Pick matches an answer to a choice: its key or its word, in any case, with spaces around it ignored; a blank
// answer is the default. ok is false for an answer that matches nothing.
func Pick(answer string, choices []Choice, def string) (key string, ok bool) {
	a := strings.ToLower(strings.TrimSpace(answer))
	if a == "" {
		return def, true
	}
	for _, c := range choices {
		if a == c.Key || a == c.Word {
			return c.Key, true
		}
	}
	return "", false
}

// chevron is "›" in colour, ">" plain.
func (r *Renderer) chevron() string { return r.pick("›", ">") }
