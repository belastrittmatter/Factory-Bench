<scenario>
<first_run>{{.FirstRun}}</first_run>
<brief>
{{.Brief}}
</brief>
<attachments>
{{.Attachments}}
</attachments>
<project_md>
{{.Project}}
</project_md>
<living_specs>
{{.LivingSpecs}}
</living_specs>
</scenario>

<transcript>
{{.Transcript}}</transcript>

{{if eq .TurnsLeft 0}}No turns are left. This is your last turn: you must finish now. Reply with done: true and the change folder in `change.files`; the message is your goodbye.{{else}}You have {{.TurnsLeft}} more turn(s) after this one. Reply with your next move and message. Reply with done: true and the change folder only when you are ready to hand over.{{end}}

Reply format (JSON, enforced by the schema): `move` is the move you make this turn, `message` is exactly what the person sees, `done` ends the conversation, `change.files` is the change folder as a list of `{path, content}` and is required when `done` is true (ignored otherwise). The person never sees the change folder before you finish.
{{.Repair}}
