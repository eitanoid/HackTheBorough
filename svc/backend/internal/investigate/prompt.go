package investigate

import (
	_ "embed"
	"fmt"
	"strings"
	"time"
)

//go:embed prompts/investigator_system.md
var investigatorSystem string

func systemPrompt() string { return investigatorSystem }

// userPrompt is also stored as the workflow's insight_prompt (the
// investigator's brief), so it is a pure function of the inputs.
func userPrompt(in Inputs) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", in.Goal)
	if in.InsightPrompt != "" {
		fmt.Fprintf(&b, "\nWhat the user cares about (from the statistical workflow): %s\n", in.InsightPrompt)
	}
	b.WriteString("\nSources (use these keys in INPUT['sources']):\n")
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
	b.WriteString("\nLeads from the statistical workflow (already shown to the user; test them further, do not restate them):\n")
	if len(in.Leads) == 0 {
		b.WriteString("none: the statistical workflow found nothing that survived correction.\n")
	}
	for _, l := range in.Leads {
		fmt.Fprintf(&b, "- %s · %s · series: %s · direction: %s", l.Key, l.Kind, strings.Join(l.Series, " / "), l.Direction)
		if l.HasQ {
			fmt.Fprintf(&b, " · q=%.3g", l.Q)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}
