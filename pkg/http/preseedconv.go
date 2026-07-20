package http

import "strings"

type preseedDirective struct {
	owner    string
	template string
	dtype    string
	value    string
	raw      string
}

// parsePreseed splits a flat d-i preseed into logical directives. It joins
// backslash line-continuations, drops blank lines and '#' comments, and splits
// each logical line into owner/template/type/value. A line that does not fit the
// `owner template type [value]` grammar is preserved verbatim as a passthrough
// directive (template == "") so nothing is ever dropped.
func parsePreseed(src []byte) []preseedDirective {
	var out []preseedDirective
	var buf strings.Builder
	joining := false
	for _, raw := range strings.Split(string(src), "\n") {
		line := raw
		// A physical line ending in '\' continues onto the next.
		if strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") {
			trimmed := strings.TrimRight(line, " \t")
			buf.WriteString(strings.TrimSuffix(trimmed, "\\"))
			buf.WriteByte(' ')
			joining = true
			continue
		}
		buf.WriteString(line)
		logical := buf.String()
		buf.Reset()
		joining = false
		out = appendDirective(out, logical)
	}
	if joining { // trailing '\' with no final line
		out = appendDirective(out, buf.String())
	}
	return out
}

// appendDirective normalizes and classifies one logical line. Blank/comment
// lines contribute nothing; a well-formed line becomes a mapped directive;
// anything else is preserved as a passthrough (template == "").
func appendDirective(out []preseedDirective, logical string) []preseedDirective {
	collapsed := strings.Join(strings.Fields(logical), " ")
	if collapsed == "" || strings.HasPrefix(collapsed, "#") {
		return out
	}
	fields := strings.SplitN(collapsed, " ", 4)
	if len(fields) < 3 {
		return append(out, preseedDirective{raw: collapsed})
	}
	// Template must contain "/" to be a valid preseed directive
	if !strings.Contains(fields[1], "/") {
		return append(out, preseedDirective{raw: collapsed})
	}
	d := preseedDirective{owner: fields[0], template: fields[1], dtype: fields[2], raw: collapsed}
	if len(fields) == 4 {
		d.value = fields[3]
	}
	return append(out, d)
}
