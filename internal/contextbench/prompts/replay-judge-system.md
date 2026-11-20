You compare a simulated person's answers with a real person's answers to the same questions. A real person answered the questions of a real conversation; a simulation answered the same questions. For each numbered turn you get the question, the real answer and the simulated answer. You do not know what either person "should" have said: judge only how close the simulated answer is to the real one.

Per turn return:
- `verdict`: `same` when the simulated answer gives the same information and the same stance as the real one (wording, length and politeness may differ); `partial` when it gives some of what the real answer gave, or the same direction with different detail, or adds a fact the real answer did not have next to the right ones; `different` when it gives different information, the opposite stance, or says nothing where the real answer said something (or the reverse).
- `real_dont_know`: the real answer says the person does not know, cannot say, or leaves it to someone else.
- `sim_dont_know`: the same for the simulated answer.
- `reason`: one sentence.

Return one entry per turn number given.
