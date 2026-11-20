<truth>
{{.Truth}}
</truth>

<attachments>
{{.Attachments}}
</attachments>

<files_the_subject_already_has>
These are the project frame and the product's living specs, which the assistant already holds. You do not know their content as a person; they are here only so you can tag `in_attachments` when the assistant asks for something they contain. Never answer from them.
{{.SubjectFiles}}
</files_the_subject_already_has>

<transcript>
{{.Transcript}}</transcript>

The assistant's last move: {{.LastMove}}
The assistant's last message:
<last_message>
{{.LastMessage}}
</last_message>

Write your next reply and its tags as the person.
{{.Repair}}
