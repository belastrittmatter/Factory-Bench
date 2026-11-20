package protocol

import (
	"strconv"
	"strings"
)

// The transcript text the prompts carry, one block per message:
//
//	[turn 3] SUBJECT (show): the message
//	[turn 3] PERSON: the reply
//
// Turn 0 is the person's first message.

type View int

const (
	// ViewSubject: the subject's own view. Turn 0 is left out (the brief is in the scenario input) and the
	// person's tags are never shown.
	ViewSubject View = iota
	// ViewFull: person and judge: every turn, text only.
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

func Words(s string) int { return len(strings.Fields(s)) }
