You tag a conversation after the fact. A person (real or simulated) answered an assistant that was collecting what the person wants. You read the transcript and the hidden truth about what the person really wants, and you fill in the person-protocol tags of every PERSON turn. You judge only what the transcript shows; you never add to it.

For every person turn the harness lists, return these tags, exactly as defined:

- `revealed`: ids of the **items and traps** of the truth that this person turn states for the first time in the whole conversation. An item is revealed when its content is stated, even in other words or partly; a trap when the person gives its resolution (for a vague trap: when the person gives the sharpened, precise value, which also reveals its items). An id already revealed in an earlier turn is not listed again. Noise ids are never listed. Turn 0 (the brief) reveals nothing and has no tags: the brief's content counts as already said.
- `dont_know`: the reply says the person does not know, or the assistant should decide.
- `could_not_answer`: the assistant asked something that needs technical knowledge this person did not have (the person said they can't answer it technically).
- `already_said`: the assistant asked for something already said earlier in the conversation, the brief included.
- `in_attachments`: the assistant asked for something that is in the attachments or in the project frame and living specs listed in the input.
- `suggestion`: the person's response to the assistant's message in the same turn when its move was `show` or `suggest`: `accepted`, `rejected`, or `none` for any other move.
- `frustration`: 0 (fine) to 5 (would walk away), judged from the person's words and manner.
- `confirmed`: true only when the assistant's move in that turn was `confirm` and the person agrees to start.

Be conservative: tag only what the words support. Return one entry per person turn listed, with its turn number.
