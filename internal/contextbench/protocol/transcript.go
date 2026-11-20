package protocol

import (
	"regexp"
	"strconv"
	"strings"
)

// The transcript text the prompts carry, one block per message:
//
//	[turn 3] SUBJECT (show): the message
//	[turn 3] PERSON: the reply
//
// Turn 0 is the person's first message. A message may span lines; a block runs to the next block header.

type View int

const (
	// ViewSubject: the subject's own view. Turn 0 is left out (the brief is in the scenario input) and the
	// person's tags are never shown.
	ViewSubject View = iota
	// ViewFull: person, tagger and judge: every turn, text only.
	ViewFull
)

func RenderTranscript(turns []Turn, v View) string {
	var b strings.Builder
	for _, t := range turns {
		if t.Turn == 0 && v == ViewSubject {
			continue
		}
		if t.Subject != nil {
			b.WriteString("[turn " + strconv.Itoa(t.Turn) + "] SUBJECT (" + t.Subject.Move + "): " + t.Subject.Message + "\n")
		}
		if t.Person != nil {
			b.WriteString("[turn " + strconv.Itoa(t.Turn) + "] PERSON: " + t.Person.Reply + "\n")
		}
	}
	if b.Len() == 0 {
		return "(nothing yet)\n"
	}
	return b.String()
}

type Line struct {
	Turn int
	Who  string
	Move string
	Text string
}

var lineRe = regexp.MustCompile(`^\[turn (\d+)\] (SUBJECT \((\w+)\)|PERSON): ?(.*)$`)

func ParseTranscript(text string) []Line {
	var out []Line
	for _, raw := range strings.Split(text, "\n") {
		if m := lineRe.FindStringSubmatch(raw); m != nil {
			n, _ := strconv.Atoi(m[1])
			l := Line{Turn: n, Text: m[4]}
			if strings.HasPrefix(m[2], "SUBJECT") {
				l.Who, l.Move = "SUBJECT", m[3]
			} else {
				l.Who = "PERSON"
			}
			out = append(out, l)
		} else if len(out) > 0 {
			out[len(out)-1].Text += "\n" + raw
		}
	}
	for i := range out {
		out[i].Text = strings.TrimRight(out[i].Text, "\n")
	}
	return out
}

func Words(s string) int { return len(strings.Fields(s)) }
