package workflows

import (
	_ "embed"
	"fmt"
	"strings"
	"time"
)

//go:embed prompts/author_system.md
var authorSystem string

//go:embed prompts/example_script.py
var exampleScript string

func systemPrompt() string {
	return authorSystem + "```python\n" + strings.TrimRight(exampleScript, "\n") + "\n```\n"
}

func userPrompt(in AuthorInputs, t *Trigger) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n\nSources (use these keys in INPUT['sources']):\n", in.Goal)
	for _, s := range in.Sources {
		fmt.Fprintf(&b, "\n## %s\n", s.Key)
		fmt.Fprintf(&b, "name: %s · kind: %s · records: %d", s.Name, s.Kind, s.Records)
		if s.Records > 0 {
			fmt.Fprintf(&b, " · first: %s · last: %s", s.FirstAt.UTC().Format(time.RFC3339), s.LastAt.UTC().Format(time.RFC3339))
		}
		b.WriteString("\n")
		if s.Profile.Summary != "" {
			fmt.Fprintf(&b, "summary: %s\n", s.Profile.Summary)
		}
		if len(s.Profile.Fields) == 0 {
			b.WriteString("fields: none profiled\n")
			continue
		}
		b.WriteString("fields (path · role · unit · aggregation · description):\n")
		for _, f := range s.Profile.Fields {
			fmt.Fprintf(&b, "- %s · %s · %s · %s · %s\n", f.Path, f.Role, orDash(f.Unit), orDash(f.Aggregation), f.Description)
		}
	}
	if in.PrevScript != nil {
		reason := "(no reason given)"
		if t != nil && t.Reason != "" {
			reason = t.Reason
		}
		fmt.Fprintf(&b, "\nThe previous script is below. The reviewer asked for a new one because: %s\n\n```python\n%s\n```\n",
			reason, strings.TrimRight(*in.PrevScript, "\n"))
	}
	return b.String()
}

func retryBlock(script, forAuthor string) string {
	return fmt.Sprintf("\nYour script failed when run on the current data:\n\n%s\n\nThe script that failed:\n\n```python\n%s\n```\n\nFix it and reply with the complete corrected script.\n",
		forAuthor, strings.TrimRight(script, "\n"))
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}
